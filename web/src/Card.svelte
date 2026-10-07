<script lang="ts">
	import type { Project, Target } from './lib/api';
	import { ago, freshness } from './lib/time';

	type Props = {
		p: Project;
		selected: boolean;
		now: number;
		onselect: () => void;
		ondetail: () => void;
		onact: (t: Target) => void;
	};
	let { p, selected, now, onselect, ondetail, onact }: Props = $props();

	const fresh = $derived(freshness(p.last_activity, now));
</script>

<!-- svelte-ignore a11y_click_events_have_key_events (keyboard handled globally) -->
<div
	class="card"
	class:selected
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
		<time title={p.last_activity}>{ago(p.last_activity, now)}</time>
	</header>

	{#if p.kind === 'git'}
		<div class="branch">
			<span class="glyph">⎇</span>
			<span class="name">{p.branch === 'HEAD' ? 'no commits' : p.branch}</span>
			{#if p.ahead}<span class="sync up" title="ahead of {p.upstream}">↑{p.ahead}</span>{/if}
			{#if p.behind}<span class="sync down" title="behind {p.upstream} (last fetch)">↓{p.behind}</span>{/if}
		</div>
	{/if}

	<div class="flags">
		{#if p.kind !== 'git'}<span class="flag danger">not in git</span>{/if}
		{#if p.kind === 'git' && !p.has_remote}<span class="flag danger">no remote</span>{/if}
		{#if p.dirty_files}<span class="flag warn" title="uncommitted files">✎ {p.dirty_files}</span>{/if}
		{#if p.unpushed_commits}<span class="flag warn" title="commits on no remote branch">⇡ {p.unpushed_commits} unpushed</span>{/if}
		{#if p.stashes}<span class="flag" title="stashes">⧉ {p.stashes}</span>{/if}
		{#if (p.branches ?? 0) > 1}<span class="flag" class:info={(p.branches ?? 0) >= 6} title="local branches">⎇ {p.branches}</span>{/if}
		{#if p.worktrees}<span class="flag info" title="linked worktrees">⌥ {p.worktrees} wt</span>{/if}
	</div>

	<footer>
		<div class="stack">
			{#each p.stack ?? [] as s (s)}<span>{s}</span>{/each}
		</div>
		<div class="actions">
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
	.sync.down {
		color: var(--danger);
		background: var(--danger-soft);
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
