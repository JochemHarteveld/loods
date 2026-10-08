<script lang="ts">
	import Terminal from './Terminal.svelte';
	import type { ProcAction } from './Garage.svelte';
	import { isAlive, type AgentRun, type Proc, type Project } from './lib/api';
	import { ago } from './lib/time';

	// The dock at the bottom of the window: one terminal per agent, whatever
	// project or view you are looking at. Agents keep running while you walk the
	// board, so their terminals live here instead of on a project page, and the
	// tab strip is how you switch between several at once.
	//
	// The tabs are grouped by run, with the orchestrator first and the jobs it
	// handed out next to it, because that is the tree you are actually watching:
	// one session planning, several building.

	type Props = {
		agents: Proc[]; // every Claude session loods started, newest last
		runs: AgentRun[]; // newest first
		projects: Project[];
		selectedId: string | null;
		open: boolean;
		height: number;
		now: number;
		onselect: (id: string) => void;
		onaction: (a: ProcAction, p: Proc) => void;
		ontoggle: () => void;
		onheight: (h: number) => void;
		ongoto: (p: Proc) => void; // jump to the task this agent is on
		onrun: (p: Proc) => void; // jump to this session's run in the Agents view
	};
	let { agents, runs, projects, selectedId, open, height, now, onselect, onaction, ontoggle, onheight, ongoto, onrun }: Props =
		$props();

	let terminal = $state<ReturnType<typeof Terminal>>();
	export function focus() {
		terminal?.focus();
	}

	const selected = $derived(agents.find((a) => a.id === selectedId) ?? agents[agents.length - 1] ?? null);
	const projectName = (rel?: string) => (rel ? (projects.find((p) => p.rel === rel)?.name ?? rel) : '');
	const label = (p: Proc) =>
		p.kind === 'orchestrator' ? projectName(p.project) : p.kind === 'job' ? (p.role || p.job || 'job') : `${projectName(p.project)} #${p.task}`;
	const failed = (p: Proc) => p.status === 'exited' && !p.stopped && p.exit_code !== 0;
	const running = $derived(agents.filter(isAlive).length);

	// One group per run (orchestrator first, then its jobs), then the sessions
	// that are on a single task and belong to no run.
	type Group = { run?: AgentRun; procs: Proc[] };
	const groups = $derived.by(() => {
		const out: Group[] = [];
		const taken = new Set<string>();
		for (const r of runs) {
			const mine = agents.filter((a) => a.run_id === r.id);
			if (!mine.length) continue;
			mine.sort((a, b) => (a.kind === 'orchestrator' ? -1 : b.kind === 'orchestrator' ? 1 : 0));
			mine.forEach((a) => taken.add(a.id));
			out.push({ run: r, procs: mine });
		}
		const loose = agents.filter((a) => !taken.has(a.id));
		if (loose.length) out.push({ procs: loose });
		return out;
	});

	function statusLine(p: Proc) {
		const where = p.kind === 'job' ? `${projectName(p.project)} · ${p.role || p.job}` : p.kind === 'orchestrator' ? 'orchestrator' : '';
		return where ? `${where} — ${statusText(p)}` : statusText(p);
	}

	function statusText(p: Proc) {
		switch (p.status) {
			case 'running':
				return `working ${ago(p.started_at, now)}`;
			case 'stopping':
				return 'stopping…';
			case 'orphan':
				return 'orphan · from an earlier loods';
			default:
				return `${p.stopped ? 'stopped' : p.exit_code === 0 ? 'finished' : `failed (${p.exit_code})`} · ${ago(p.ended_at, now)} ago`;
		}
	}

	// Drag the top edge to resize. The height lives in the parent, so it survives
	// switching views and collapsing the dock.
	function startResize(e: PointerEvent) {
		e.preventDefault();
		const startY = e.clientY;
		const from = height;
		const move = (ev: PointerEvent) => onheight(Math.min(window.innerHeight * 0.8, Math.max(140, from + (startY - ev.clientY))));
		const up = () => {
			removeEventListener('pointermove', move);
			removeEventListener('pointerup', up);
		};
		addEventListener('pointermove', move);
		addEventListener('pointerup', up);
	}
</script>

<section class="dock" class:open style:height={open ? height + 'px' : undefined}>
	{#if open}
		<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
		<div class="grip" role="separator" aria-label="Resize the agent dock" onpointerdown={startResize}></div>
	{/if}
	<header>
		<button class="fold" onclick={ontoggle} title="Show or hide the agent terminals (`)">
			<span class="spark" class:beat={running}>✻</span>
			agents
			<span class="n">{running ? `${running} working` : agents.length}</span>
			<span class="caret">{open ? '▾' : '▴'}</span>
		</button>

		<div class="tabs">
			{#each groups as g, i (g.run?.id ?? 'loose' + i)}
				<div class="group" class:run={g.run}>
					{#each g.procs as a (a.id)}
						<button
							class="tab"
							class:active={open && a.id === selected?.id}
							class:orch={a.kind === 'orchestrator'}
							onclick={() => onselect(a.id)}
							title={statusLine(a)}
						>
							<span class="dot {a.status}" class:failed={failed(a)}></span>
							{#if a.kind === 'orchestrator'}<span class="spark">✻</span>{/if}
							{label(a)}
						</button>
					{/each}
				</div>
			{/each}
		</div>

		{#if open && selected}
			<div class="acts">
				<span class="status">{statusText(selected)}</span>
				{#if selected.task}
					<button class="btn" onclick={() => ongoto(selected)} title="Open the planboard of this task">task #{selected.task}</button>
				{/if}
				{#if selected.run_id}
					<button class="btn" onclick={() => onrun(selected)} title="Show this run in Agents (4)">run</button>
				{/if}
				{#if isAlive(selected)}
					<button class="btn danger" onclick={() => onaction('stop', selected)}>
						{selected.status === 'stopping' ? 'kill now' : 'stop'}
					</button>
				{:else}
					<button class="btn go" onclick={() => onaction('restart', selected)}>run again</button>
					<button class="btn" onclick={() => onaction('remove', selected)}>remove</button>
				{/if}
			</div>
		{/if}
	</header>

	{#if open && selected}
		<div class="termwrap">
			{#key selected.id}
				<Terminal bind:this={terminal} id={selected.id} />
			{/key}
		</div>
		<p class="hint"><kbd>i</kbd> type into it · <kbd>esc</kbd> leave it · <kbd>`</kbd> hide the dock</p>
	{/if}
</section>

<style>
	.dock {
		flex: none;
		display: flex;
		flex-direction: column;
		min-height: 0;
		border-top: 1px solid var(--line);
		background: var(--panel);
	}
	.grip {
		height: 6px;
		margin-top: -4px;
		cursor: ns-resize;
	}
	.grip:hover {
		background: var(--accent-soft);
	}
	header {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 5px 24px;
		min-height: 30px;
	}
	.fold {
		display: flex;
		align-items: center;
		gap: 6px;
		flex: none;
		border: 0;
		background: none;
		color: var(--muted);
		font: 600 11.5px var(--sans);
		text-transform: uppercase;
		letter-spacing: 0.07em;
		cursor: pointer;
		padding: 2px 0;
	}
	.fold:hover {
		color: var(--text);
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
	.n,
	.caret {
		color: var(--faint);
		font-weight: normal;
		text-transform: none;
		letter-spacing: 0;
	}
	.tabs {
		display: flex;
		gap: 4px;
		overflow-x: auto;
		min-width: 0;
		scrollbar-width: none;
	}
	.group {
		display: flex;
		gap: 4px;
		flex: none;
	}
	/* A run reads as one tree: its tabs sit together behind one rule. */
	.group.run {
		padding-left: 7px;
		margin-left: 3px;
		border-left: 1px solid var(--line);
	}
	.tab.orch {
		color: var(--accent);
	}
	.tab {
		display: flex;
		align-items: center;
		gap: 6px;
		flex: none;
		border: 1px solid transparent;
		border-radius: 7px;
		background: none;
		color: var(--muted);
		font: 12px var(--sans);
		padding: 3px 9px;
		cursor: pointer;
	}
	.tab:hover {
		background: var(--chip);
	}
	.tab.active {
		background: var(--card);
		border-color: var(--line);
		color: var(--text);
	}
	.dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--faint);
		flex: none;
	}
	.dot.running,
	.dot.orphan {
		background: var(--running);
	}
	.dot.stopping {
		background: var(--accent);
	}
	.dot.failed {
		background: var(--danger);
	}
	.acts {
		display: flex;
		align-items: center;
		gap: 6px;
		margin-left: auto;
		flex: none;
	}
	.status {
		font-size: 11.5px;
		color: var(--faint);
	}
	.btn {
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
	.btn.go {
		color: var(--running);
	}
	.btn.danger {
		color: var(--danger);
	}
	.termwrap {
		flex: 1;
		min-height: 0;
		border-top: 1px solid var(--line);
		background: var(--term-bg);
	}
	.hint {
		margin: 0;
		padding: 3px 24px 5px;
		font-size: 11px;
		color: var(--faint);
	}
	.hint kbd {
		font: 10.5px var(--mono);
		border: 1px solid var(--line);
		border-radius: 4px;
		padding: 0 3px;
	}
	@media (max-width: 900px) {
		header,
		.hint {
			padding-left: 16px;
			padding-right: 16px;
		}
	}
</style>
