<script lang="ts">
	import type { Project } from './lib/api';
	import { fixLabel, hints, type Finding } from './lib/hygiene';

	export type Row = { p: Project; f: Finding };

	type Props = {
		rows: Row[];
		selected: number;
		githubStatus: string;
		onselect: (i: number) => void;
		onfix: (r: Row) => void;
		ondetail: (r: Row) => void;
	};
	let { rows, selected, githubStatus, onselect, onfix, ondetail }: Props = $props();

	const counts = $derived({
		danger: rows.filter((r) => r.f.level === 'danger').length,
		warn: rows.filter((r) => r.f.level === 'warn').length,
		info: rows.filter((r) => r.f.level === 'info').length
	});
</script>

<div class="hygiene">
	<header>
		<h2>Hygiene</h2>
		<span class="count danger" class:zero={!counts.danger}>{counts.danger} serious</span>
		<span class="count warn" class:zero={!counts.warn}>{counts.warn} to look at</span>
		<span class="count info" class:zero={!counts.info}>{counts.info} tidy-ups</span>
		{#if githubStatus}<span class="gh" title="PR and CI findings need gh">GitHub: {githubStatus}</span>{/if}
	</header>

	{#if !rows.length}
		<p class="empty">Nothing to clean up. Everything is committed, pushed and tidy.</p>
	{/if}

	<ul role="listbox" aria-label="Findings">
		{#each rows as r, i (r.p.rel + r.f.kind + r.f.text)}
			{#if i === 0 || rows[i - 1].p.rel !== r.p.rel}
				<li class="project">
					<button class="pname" onclick={() => ondetail(r)}>{r.p.name}</button>
					{#if r.p.group}<span class="group">{r.p.group}</span>{/if}
				</li>
			{/if}
			<!-- svelte-ignore a11y_click_events_have_key_events (keyboard handled globally) -->
			<li
				class="row {r.f.level}"
				class:selected={i === selected}
				data-row={i}
				role="option"
				aria-selected={i === selected}
				tabindex="-1"
				onclick={() => onselect(i)}
				ondblclick={() => ondetail(r)}
			>
				<span class="dot"></span>
				<span class="text">{r.f.text}</span>
				{#if r.f.fix}
					<button class="fix" onclick={(e) => (e.stopPropagation(), onfix(r))}>
						{#if i === selected}<kbd>enter</kbd>{/if}
						{fixLabel[r.f.fix]}
					</button>
				{:else if r.f.url}
					<a class="fix link" href={r.f.url} target="_blank" rel="noreferrer" onclick={(e) => e.stopPropagation()}>{hints[r.f.kind] ?? 'open'} ↗</a>
				{:else if hints[r.f.kind]}
					<span class="hint">{hints[r.f.kind]}</span>
				{/if}
			</li>
		{/each}
	</ul>
</div>

<style>
	.hygiene {
		max-width: 980px;
	}
	header {
		display: flex;
		align-items: baseline;
		gap: 14px;
		flex-wrap: wrap;
		margin: 6px 0 14px;
	}
	h2 {
		margin: 0;
		font-size: 12px;
		text-transform: uppercase;
		letter-spacing: 0.08em;
		color: var(--muted);
	}
	.count {
		font-size: 12.5px;
	}
	.count.danger {
		color: var(--danger);
	}
	.count.warn {
		color: var(--accent);
	}
	.count.info {
		color: var(--info);
	}
	.count.zero {
		color: var(--faint);
	}
	.gh {
		margin-left: auto;
		font-size: 12px;
		color: var(--faint);
	}
	.empty {
		color: var(--muted);
		margin-top: 60px;
		text-align: center;
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.project {
		display: flex;
		align-items: baseline;
		gap: 8px;
		margin: 16px 0 4px;
	}
	.project:first-child {
		margin-top: 0;
	}
	.pname {
		border: none;
		background: none;
		padding: 0;
		font-weight: 600;
		font-size: 14px;
		cursor: pointer;
		color: var(--text);
	}
	.pname:hover {
		color: var(--accent);
	}
	.group {
		font-size: 11.5px;
		color: var(--faint);
	}
	.row {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 7px 12px;
		border-radius: 7px;
		border: 1px solid transparent;
		background: var(--card);
		margin-bottom: 3px;
		font-size: 13.5px;
		scroll-margin: 60px;
		cursor: default;
	}
	.row.selected {
		border-color: var(--accent);
	}
	.dot {
		flex: none;
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--info);
	}
	.danger .dot {
		background: var(--danger);
	}
	.warn .dot {
		background: var(--accent);
	}
	.danger .text {
		color: var(--danger);
	}
	.text {
		flex: 1;
		min-width: 0;
		overflow-wrap: anywhere;
	}
	.fix {
		flex: none;
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-size: 12px;
		padding: 3px 10px;
		border: 1px solid var(--line);
		border-radius: 6px;
		background: var(--panel);
		color: var(--text);
		cursor: pointer;
		text-decoration: none;
	}
	.fix:hover {
		border-color: var(--accent);
	}
	.fix.link {
		color: var(--info);
	}
	.hint {
		flex: none;
		max-width: 45%;
		font-size: 12px;
		color: var(--faint);
		text-align: right;
	}
</style>
