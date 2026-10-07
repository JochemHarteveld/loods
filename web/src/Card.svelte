<script lang="ts">
	import { runState, type ClaudeSummary, type GitHubInfo, type Plan, type Proc, type Project, type Target } from './lib/api';
	import { statusClass, taskProgress } from './lib/plan';
	import { ago, freshness } from './lib/time';

	type Props = {
		p: Project;
		plan: Plan | undefined;
		claude: ClaudeSummary | undefined;
		gh: GitHubInfo | undefined;
		procs: Proc[]; // live processes of this project
		selected: boolean;
		now: number;
		onselect: () => void;
		ondetail: () => void;
		onact: (t: Target | 'run') => void;
	};
	let { p, plan, claude, gh, procs, selected, now, onselect, ondetail, onact }: Props = $props();

	const fresh = $derived(freshness(p.last_activity, now));
	const progress = $derived(taskProgress(plan));
	const ci = $derived(gh?.ci ? runState(gh.ci) : null);
	const serious = $derived(p.warnings?.filter((w) => w.level === 'danger') ?? []);
	const recentClaude = $derived(claude && (claude.live || now - Date.parse(claude.last_at) < 14 * 86_400_000) ? claude : null);
</script>

<!-- svelte-ignore a11y_click_events_have_key_events (keyboard handled globally) -->
<div
	class="card {statusClass(plan?.status)}"
	class:selected
	class:dead={plan?.status === 'dead'}
	data-id={p.rel}
	role="option"
	aria-selected={selected}
	tabindex="-1"
	onclick={onselect}
	ondblclick={ondetail}
>
	<header>
		<span class="dot {fresh}" title="last activity"></span>
		<h3>{p.name}</h3>
		{#if plan?.priority}<span class="prio p{plan.priority}" title="priority">P{plan.priority}</span>{/if}
		{#if plan?.status}<span class="status">{plan.status}</span>{/if}
		<time title={p.last_activity}>{ago(p.last_activity, now)}</time>
	</header>

	{#if plan?.next}
		<p class="next" title={plan.next}><span>→</span> {plan.next}</p>
	{/if}

	{#if p.kind === 'git'}
		<div class="branch">
			<span class="glyph">⎇</span>
			<span class="name">{p.branch === 'HEAD' ? 'no commits' : p.branch}</span>
			{#if p.ahead}<span class="sync up" title="ahead of {p.upstream}">↑{p.ahead}</span>{/if}
			{#if p.behind}<span class="sync down" title="behind {p.upstream} (last fetch)">↓{p.behind}</span>{/if}
			{#if ci}
				<span class="ci {ci}" title="{gh?.ci?.workflow}: {gh?.ci?.status} {gh?.ci?.conclusion}">{ci === 'pass' ? '✓' : ci === 'fail' ? '✗' : '●'}</span>
			{/if}
		</div>
	{/if}

	{#if procs.length}
		<div class="running">
			{#each procs as r (r.id)}
				<span class="proc {r.status}" title={r.run}>
					<span class="pdot"></span>{r.name}{#each r.ports ?? [] as port (port)}<span class="port">:{port}</span>{/each}
					{#if r.warning}<span class="pwarn">!</span>{/if}
				</span>
			{/each}
		</div>
	{/if}

	<div class="flags">
		{#each serious as w (w.text)}<span class="flag danger" title={w.text}>⚠ {w.kind === 'env-tracked' ? 'secret committed' : w.text}</span>{/each}
		{#if p.kind !== 'git'}<span class="flag danger">not in git</span>{/if}
		{#if p.kind === 'git' && !p.has_remote}<span class="flag danger">no remote</span>{/if}
		{#if p.dirty_files}<span class="flag warn" title="uncommitted files">✎ {p.dirty_files}</span>{/if}
		{#if p.unpushed_commits}<span class="flag warn" title="commits on no remote branch">⇡ {p.unpushed_commits} unpushed</span>{/if}
		{#if p.stashes}<span class="flag" title="stashes">⧉ {p.stashes}</span>{/if}
		{#if (p.branches ?? 0) > 1}<span class="flag" class:info={(p.branches ?? 0) >= 6} title="local branches">⎇ {p.branches}</span>{/if}
		{#if p.worktrees}<span class="flag info" title="linked worktrees">⌥ {p.worktrees} wt</span>{/if}
		{#if gh?.prs.length}<span class="flag info" title={gh.prs.map((pr) => `#${pr.number} ${pr.title}`).join('\n')}>⇄ {gh.prs.length} PR{gh.prs.length === 1 ? '' : 's'}</span>{/if}
		{#if progress[1]}<span class="flag" class:ok={progress[0] === progress[1]} title="tasks done">☑ {progress[0]}/{progress[1]}</span>{/if}
	</div>

	{#if recentClaude}
		<div class="claude" class:live={recentClaude.live} title="last Claude session: {recentClaude.last_title}">
			<span class="spark">✻</span>
			{#if recentClaude.live}<b>Claude now</b>{:else}<b>{ago(recentClaude.last_at, now)}</b>{/if}
			<span class="ctitle">{recentClaude.last_title}</span>
		</div>
	{/if}

	<footer>
		<div class="stack">
			{#each p.stack ?? [] as s (s)}<span>{s}</span>{/each}
		</div>
		<div class="actions">
			{#if p.commands?.length && !procs.length}
				<button class="run" title="Run {p.default_command} (r)" onclick={(e) => (e.stopPropagation(), onact('run'))}>▶ run</button>
			{/if}
			<button title="Open in VS Code (c)" onclick={(e) => (e.stopPropagation(), onact('code'))}>code</button>
			<button title="Terminal here (t)" onclick={(e) => (e.stopPropagation(), onact('terminal'))}>term</button>
			{#if p.web_url}
				<button title="Open remote (g)" onclick={(e) => (e.stopPropagation(), onact('github'))}>git↗</button>
			{/if}
		</div>
	</footer>
</div>

<style>
	.card {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 12px 14px;
		background: var(--card);
		border: 1px solid var(--line);
		border-radius: 10px;
		box-shadow: var(--shadow);
		cursor: default;
		outline: none;
		min-height: 128px;
		scroll-margin: 64px 0 48px;
		transition:
			border-color 0.1s,
			background 0.1s;
	}
	.card:hover {
		background: var(--card-hover);
	}
	.card.dead {
		opacity: 0.55;
	}
	.prio {
		flex: none;
		font: 600 10.5px var(--mono);
		padding: 0 5px;
		border-radius: 4px;
		background: var(--chip);
		color: var(--muted);
	}
	.prio.p1 {
		background: var(--accent-soft);
		color: var(--accent);
	}
	.status {
		flex: none;
		font-size: 11px;
		color: var(--st);
		font-weight: 600;
	}
	.next {
		margin: 0;
		font-size: 13px;
		line-height: 1.35;
		display: -webkit-box;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		-webkit-box-orient: vertical;
		overflow: hidden;
	}
	.next span {
		color: var(--accent);
		font-weight: 600;
	}
	.claude {
		display: flex;
		align-items: baseline;
		gap: 5px;
		font-size: 11.5px;
		color: var(--faint);
		min-width: 0;
	}
	.claude b {
		flex: none;
		font-weight: 600;
		font-family: var(--mono);
		color: var(--muted);
	}
	.claude.live b,
	.claude.live .spark {
		color: var(--accent);
	}
	.claude.live .spark {
		animation: spin 3s linear infinite;
		display: inline-block;
	}
	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
	.ctitle {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		min-width: 0;
	}
	.card.selected {
		border-color: var(--accent);
		box-shadow:
			0 0 0 1px var(--accent),
			var(--shadow);
	}

	header {
		display: flex;
		align-items: center;
		gap: 8px;
	}
	h3 {
		margin: 0;
		font-size: 15px;
		font-weight: 600;
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	time {
		font: 12px var(--mono);
		color: var(--muted);
	}
	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex: none;
	}
	.dot.hot {
		background: var(--hot);
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--hot) 20%, transparent);
	}
	.dot.warm {
		background: var(--warm);
	}
	.dot.cold {
		background: var(--cold);
	}

	.branch {
		display: flex;
		align-items: center;
		gap: 6px;
		font: 12.5px var(--mono);
		min-width: 0;
	}
	.branch .glyph {
		color: var(--faint);
	}
	.branch .name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		min-width: 0;
	}
	.sync {
		font-size: 11.5px;
		padding: 0 4px;
		border-radius: 4px;
	}
	.sync.up {
		color: var(--info);
		background: var(--info-soft);
	}
	.ci {
		font-size: 12px;
		font-weight: 700;
	}
	.ci.pass {
		color: var(--hot);
	}
	.ci.fail {
		color: var(--danger);
	}
	.ci.pending {
		color: var(--warm);
	}
	.sync.down {
		color: var(--danger);
		background: var(--danger-soft);
	}

	.running {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
	}
	.proc {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		font: 11.5px var(--mono);
		padding: 1px 7px;
		border-radius: 10px;
		background: color-mix(in srgb, var(--running) 14%, transparent);
		color: var(--running);
	}
	.proc.stopping {
		background: var(--accent-soft);
		color: var(--warm);
	}
	.proc.orphan {
		background: color-mix(in srgb, var(--orphan) 14%, transparent);
		color: var(--orphan);
	}
	.pdot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: currentColor;
	}
	.port {
		color: var(--info);
	}
	.pwarn {
		color: var(--danger);
		font-weight: 700;
	}
	.actions .run {
		color: var(--running);
	}
	.flags {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
		min-height: 20px;
	}
	.flag {
		font-size: 11.5px;
		padding: 1px 7px;
		border-radius: 10px;
		background: var(--chip);
		color: var(--muted);
	}
	.flag.warn {
		background: var(--accent-soft);
		color: var(--accent);
	}
	.flag.danger {
		background: var(--danger-soft);
		color: var(--danger);
	}
	.flag.ok {
		color: var(--hot);
	}
	.flag.info {
		background: var(--info-soft);
		color: var(--info);
	}

	footer {
		margin-top: auto;
		display: flex;
		align-items: flex-end;
		justify-content: space-between;
		gap: 8px;
	}
	.stack {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
	}
	.stack span {
		font: 10.5px var(--mono);
		color: var(--faint);
		border: 1px solid var(--line);
		padding: 0 5px;
		border-radius: 4px;
	}
	.actions {
		display: flex;
		gap: 4px;
		opacity: 0;
		transition: opacity 0.1s;
	}
	.card:hover .actions,
	.card.selected .actions {
		opacity: 1;
	}
	.actions button {
		font-size: 11.5px;
		padding: 2px 7px;
		border: 1px solid var(--line);
		border-radius: 5px;
		background: var(--panel);
		cursor: pointer;
	}
	.actions button:hover {
		border-color: var(--accent);
		color: var(--accent);
	}
</style>
