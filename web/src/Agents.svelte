<script lang="ts">
	import { isAlive, jobOver, runOver, type AgentRun, type Job, type Proc, type Project } from './lib/api';
	import { ago } from './lib/time';

	// The Agents view: one card per run, so you can see at a glance which agents
	// are working, which one is the orchestrator and what every other session is
	// doing. The jobs of a run are laid out in waves — everything in one column
	// can run at the same time, and a column waits for the one before it — which
	// is the shape the orchestrator actually planned.

	type Props = {
		runs: AgentRun[]; // newest first
		projects: Project[];
		procs: Proc[];
		selectedJob: string | null; // "<run>:<job>" or "<run>:" for the orchestrator
		now: number;
		onselect: (key: string) => void;
		onopen: (procId: string) => void; // show this session's terminal in the dock
		onstop: (p: Proc) => void;
		oncancel: (r: AgentRun) => void;
		onproject: (rel: string) => void;
		ontask: (rel: string, task: number) => void;
	};
	let { runs, projects, procs, selectedJob, now, onselect, onopen, onstop, oncancel, onproject, ontask }: Props = $props();

	const live = $derived(runs.filter((r) => !runOver(r)));
	const past = $derived(runs.filter(runOver));
	const working = $derived(procs.filter((p) => isAlive(p) && (p.kind === 'job' || p.kind === 'orchestrator')).length);
	const projectName = (rel: string) => projects.find((p) => p.rel === rel)?.name ?? rel;
	const proc = (id?: string) => (id ? procs.find((p) => p.id === id) : undefined);

	// Waves: a job sits one column to the right of the furthest job it needs.
	// Jobs in the same column have no dependency on each other, so loods may run
	// them together — that is what the column means.
	function waves(r: AgentRun): Job[][] {
		const jobs = r.jobs ?? [];
		const byId = new Map(jobs.map((j) => [j.id, j]));
		const depth = new Map<string, number>();
		const of = (j: Job, seen = new Set<string>()): number => {
			if (depth.has(j.id)) return depth.get(j.id)!;
			if (seen.has(j.id)) return 0; // a cycle cannot be submitted; do not hang on one
			seen.add(j.id);
			const d = Math.max(0, ...(j.needs ?? []).map((n) => (byId.has(n) ? of(byId.get(n)!, seen) + 1 : 0)));
			depth.set(j.id, d);
			return d;
		};
		const cols: Job[][] = [];
		for (const j of jobs) {
			const d = of(j);
			(cols[d] ??= []).push(j);
		}
		return cols.filter(Boolean);
	}

	/** Why a job is not running yet, or what it reported when it finished. */
	function why(r: AgentRun, j: Job): string {
		if (j.note) return j.note;
		if (j.status === 'running') return j.title;
		if (j.status !== 'pending') return j.title;
		const waiting = (j.needs ?? []).filter((n) => (r.jobs ?? []).find((o) => o.id === n)?.status !== 'done');
		if (waiting.length) return 'waits for ' + waiting.join(', ');
		const holder = (r.jobs ?? []).find((o) => o.status === 'running' && o.exclusive && o.exclusive === j.exclusive);
		if (holder) return `waits for a free ${j.exclusive} (${holder.id} has it)`;
		return 'ready to start';
	}

	function clock(j: Job): string {
		if (j.status === 'running' && j.started_at) return ago(j.started_at, now);
		if (j.ended_at && j.started_at) return ago(j.started_at, Date.parse(j.ended_at));
		return '';
	}

	const tally = (r: AgentRun) => {
		const jobs = r.jobs ?? [];
		const done = jobs.filter((j) => j.status === 'done').length;
		const busy = jobs.filter((j) => j.status === 'running').length;
		return `${done}/${jobs.length} done${busy ? `, ${busy} working` : ''}`;
	};

	function pick(r: AgentRun, j: Job | null) {
		const key = `${r.id}:${j?.id ?? ''}`;
		onselect(key);
		const p = proc(j ? j.proc : r.orchestrator);
		if (p) onopen(p.id);
	}
</script>

<div class="agents">
	<header>
		<h2>Agents</h2>
		<span class="count" class:zero={!working}>{working ? `${working} working` : 'nothing working'}</span>
		<span class="count" class:zero={!live.length}>{live.length} {live.length === 1 ? 'run' : 'runs'} going</span>
		<p class="lead">
			A run is one goal split into jobs. loods starts a job as soon as everything it needs is done, up to the
			run's limit, and never two jobs that hold the same lock.
		</p>
	</header>

	{#if !runs.length}
		<p class="empty">
			No runs yet. Press <kbd>+</kbd> for a new project an orchestrator plans and builds, or
			<kbd>A</kbd> on a planboard task to hand that one task to an agent.
		</p>
	{/if}

	{#each [...live, ...past] as r (r.id)}
		{@const orch = proc(r.orchestrator)}
		<article class="run" class:over={runOver(r)}>
			<header class="runhead">
				<span class="pill {r.status}">{r.status}</span>
				<h3>{r.goal}</h3>
				<button class="proj" onclick={() => onproject(r.project)} title="Open this project's page">
					{projectName(r.project)}
				</button>
				<span class="meta">{r.id} · {tally(r)} · {r.max_parallel} at a time</span>
				{#if !runOver(r)}
					<button class="btn danger" onclick={() => oncancel(r)} title="Stop handing out work and stop its sessions">
						cancel run
					</button>
				{/if}
			</header>

			<!-- The orchestrator first: it is the session that planned all of this. -->
			<div class="row orch" class:active={selectedJob === `${r.id}:`} class:dead={orch && !isAlive(orch)}>
				<button class="open" onclick={() => pick(r, null)} title="Show its terminal in the dock">
					<span class="spark" class:beat={orch && isAlive(orch)}>✻</span>
					<span class="role">orchestrator</span>
					<span class="state">{orch ? (isAlive(orch) ? `planning · ${ago(orch.started_at, now)}` : 'session ended') : 'no session'}</span>
					<span class="what">{r.jobs?.length ? 'planned ' + r.jobs.length + ' jobs' : 'writing the graph…'}</span>
				</button>
				{#if orch && isAlive(orch)}
					<button class="act" onclick={() => onstop(orch)}>stop</button>
				{/if}
			</div>

			{#each waves(r) as col, i (i)}
				<div class="wave">
					<span class="lane">{i === 0 ? 'first' : `after ${i}`}</span>
					<div class="jobs">
						{#each col as j (j.id)}
							{@const p = proc(j.proc)}
							<div class="row job {j.status}" class:active={selectedJob === `${r.id}:${j.id}`}>
								<button class="open" onclick={() => pick(r, j)} title={j.title}>
									<span class="dot {j.status}"></span>
									<span class="role">{j.role || j.id}</span>
									<span class="state">{j.status}{clock(j) ? ` · ${clock(j)}` : ''}</span>
									<span class="what">{why(r, j)}</span>
								</button>
								{#if j.task}
									<button class="act" onclick={() => ontask(r.project, j.task!)} title="Open the planboard of task #{j.task}">
										#{j.task}
									</button>
								{/if}
								{#if j.exclusive}<span class="lock" title="only one job at a time may hold {j.exclusive}">{j.exclusive}</span>{/if}
								{#if p && isAlive(p)}
									<button class="act" onclick={() => onstop(p)}>stop</button>
								{/if}
							</div>
						{/each}
					</div>
				</div>
			{/each}

			{#if !r.jobs?.length}
				<p class="pending">The orchestrator has not submitted a graph yet. Its terminal is in the dock.</p>
			{/if}
		</article>
	{/each}
</div>

<style>
	.agents {
		padding: 18px 24px 28px;
		overflow-y: auto;
	}
	header h2 {
		margin: 0;
		font-size: 15px;
		display: inline;
	}
	.count {
		margin-left: 10px;
		font-size: 11.5px;
		color: var(--muted);
		background: var(--chip);
		border-radius: 10px;
		padding: 2px 8px;
	}
	.count.zero {
		color: var(--faint);
		background: none;
	}
	.lead {
		margin: 6px 0 16px;
		font-size: 12px;
		color: var(--faint);
		max-width: 70ch;
	}
	.empty {
		color: var(--muted);
		font-size: 13px;
	}
	.empty kbd {
		font: 11px var(--mono);
		border: 1px solid var(--line);
		border-radius: 4px;
		padding: 0 3px;
	}
	.run {
		border: 1px solid var(--line);
		border-radius: 12px;
		background: var(--panel);
		padding: 10px 12px 12px;
		margin-bottom: 14px;
		box-shadow: var(--shadow);
	}
	.run.over {
		opacity: 0.72;
	}
	.runhead {
		display: flex;
		align-items: center;
		gap: 9px;
		flex-wrap: wrap;
		margin-bottom: 8px;
	}
	.runhead h3 {
		margin: 0;
		font-size: 13.5px;
		font-weight: 600;
	}
	.pill {
		font: 600 10.5px var(--sans);
		text-transform: uppercase;
		letter-spacing: 0.06em;
		border-radius: 6px;
		padding: 2px 7px;
		background: var(--chip);
		color: var(--muted);
	}
	.pill.running {
		background: var(--accent-soft);
		color: var(--accent);
	}
	.pill.done {
		color: var(--running);
	}
	.pill.failed,
	.pill.cancelled {
		background: var(--danger-soft);
		color: var(--danger);
	}
	.proj {
		border: 1px solid var(--line);
		border-radius: 6px;
		background: var(--card);
		color: var(--text);
		font: 11.5px var(--sans);
		padding: 2px 7px;
		cursor: pointer;
	}
	.proj:hover {
		border-color: var(--accent);
	}
	.meta {
		font: 11.5px var(--mono);
		color: var(--faint);
	}
	.btn {
		margin-left: auto;
		font-size: 11.5px;
		padding: 3px 8px;
		border: 1px solid var(--line);
		border-radius: 6px;
		background: var(--card);
		color: var(--text);
		cursor: pointer;
	}
	.btn:hover {
		border-color: var(--accent);
	}
	.btn.danger {
		color: var(--danger);
	}
	.wave {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		margin-top: 6px;
	}
	.lane {
		flex: none;
		width: 54px;
		padding-top: 7px;
		font: 10.5px var(--mono);
		color: var(--faint);
		text-align: right;
	}
	.jobs {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(340px, 1fr));
		gap: 6px;
		flex: 1;
		min-width: 0;
	}
	.row {
		display: flex;
		align-items: center;
		gap: 6px;
		border: 1px solid var(--line);
		border-radius: 9px;
		background: var(--card);
		color: var(--text);
		font: 12px var(--sans);
		padding: 5px 8px;
		min-width: 0;
	}
	.row:hover {
		background: var(--card-hover);
	}
	/* The row itself is the button; the actions beside it are their own. */
	.open {
		display: flex;
		align-items: center;
		gap: 8px;
		flex: 1;
		min-width: 0;
		border: 0;
		background: none;
		color: inherit;
		font: inherit;
		text-align: left;
		padding: 1px 0;
		cursor: pointer;
	}
	.row.active {
		border-color: var(--accent);
	}
	.row.orch {
		margin-left: 64px;
		background: var(--accent-soft);
		border-color: transparent;
	}
	.row.orch.dead {
		background: var(--chip);
	}
	.row.pending {
		background: none;
		border-style: dashed;
		color: var(--muted);
	}
	.row.blocked,
	.row.cancelled {
		background: none;
		border-style: dashed;
		color: var(--faint);
	}
	.spark {
		color: var(--accent);
		font-size: 13px;
	}
	.spark.beat {
		animation: beat 2s ease-in-out infinite;
	}
	@keyframes beat {
		50% {
			opacity: 0.35;
		}
	}
	.dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--faint);
		flex: none;
	}
	.dot.running {
		background: var(--running);
	}
	.dot.done {
		background: var(--accent);
	}
	.dot.failed {
		background: var(--danger);
	}
	.role {
		font-weight: 600;
		flex: none;
	}
	.state {
		flex: none;
		font: 11.5px var(--mono);
		color: var(--muted);
	}
	.what {
		flex: 1;
		min-width: 0;
		color: var(--muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.act,
	.lock {
		flex: none;
		font: 11px var(--mono);
		border: 1px solid var(--line);
		border-radius: 5px;
		padding: 1px 5px;
		color: var(--muted);
		background: var(--panel);
	}
	.act:hover {
		border-color: var(--accent);
		color: var(--text);
	}
	.lock {
		border-style: dashed;
		border-color: transparent;
		background: var(--chip);
	}
	.pending {
		margin: 8px 0 0 64px;
		font-size: 12px;
		color: var(--faint);
	}
	@media (max-width: 900px) {
		.agents {
			padding: 14px 16px 24px;
		}
		.row.orch,
		.pending {
			margin-left: 0;
		}
		.lane {
			display: none;
		}
	}
</style>
