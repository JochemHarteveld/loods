<script lang="ts">
	import type { ClaudeSummary, Plan, Project, Status } from './lib/api';
	import { statusClass, statusLabel, taskProgress } from './lib/plan';
	import { ago } from './lib/time';

	export type Column = { status: Status | ''; items: Project[] };

	type Props = {
		columns: Column[];
		plans: Record<string, Plan>;
		claude: Record<string, ClaudeSummary>;
		running: Set<string>; // project rels with a live process
		selectedId: string | null;
		now: number;
		onselect: (rel: string) => void;
		ondetail: (rel: string) => void;
		onmove: (rel: string, status: Status | '') => void;
	};
	let { columns, plans, claude, running, selectedId, now, onselect, ondetail, onmove }: Props = $props();

	let dragging = $state<string | null>(null);
	let over = $state<Status | '' | null>(null);

	const hints: Record<string, string> = {
		'': 'Projects without a status. Press m or drag them to a column.',
		idea: 'Not started, or just an idea.',
		active: 'Being worked on.',
		paused: 'On hold.',
		shipped: 'Live or finished; maintenance only.',
		dead: 'Not going anywhere. Archive candidates.'
	};
</script>

<div class="kanban">
	{#each columns as col (col.status)}
		<div
			class="col {statusClass(col.status)}"
			class:over={over === col.status && dragging}
			role="listbox"
			tabindex="-1"
			aria-label={statusLabel(col.status)}
			ondragover={(e) => {
				if (!dragging) return;
				e.preventDefault();
				over = col.status;
			}}
			ondragleave={(e) => {
				if (!(e.currentTarget as HTMLElement).contains(e.relatedTarget as Node)) over = null;
			}}
			ondrop={(e) => {
				e.preventDefault();
				if (dragging && (plans[dragging]?.status ?? '') !== col.status) onmove(dragging, col.status);
				dragging = over = null;
			}}
		>
			<h2><span class="dot"></span>{statusLabel(col.status)} <span class="n">{col.items.length}</span></h2>
			{#if !col.items.length}
				<p class="hint">{hints[col.status]}</p>
			{/if}
			{#each col.items as p (p.rel)}
				{@const plan = plans[p.rel]}
				{@const progress = taskProgress(plan)}
				{@const c = claude[p.rel]}
				<!-- svelte-ignore a11y_click_events_have_key_events (keyboard handled globally) -->
				<div
					class="item"
					class:selected={p.rel === selectedId}
					class:dragging={dragging === p.rel}
					data-id={p.rel}
					role="option"
					aria-selected={p.rel === selectedId}
					tabindex="-1"
					draggable="true"
					ondragstart={(e) => {
						dragging = p.rel;
						e.dataTransfer?.setData('text/plain', p.rel);
						onselect(p.rel);
					}}
					ondragend={() => (dragging = over = null)}
					onclick={() => onselect(p.rel)}
					ondblclick={() => ondetail(p.rel)}
				>
					<div class="top">
						{#if running.has(p.rel)}<span class="run" title="running in the Garage"></span>{/if}
						<span class="name">{p.name}</span>
						{#if plan?.priority}<span class="prio p{plan.priority}">P{plan.priority}</span>{/if}
					</div>
					{#if p.group}<div class="group">{p.group}</div>{/if}
					{#if plan?.next}
						<p class="next">{plan.next}</p>
					{:else if col.status === 'active'}
						<p class="next none">no next step: press n</p>
					{/if}
					<div class="meta">
						{#if progress[1]}<span class:ok={progress[0] === progress[1]}>☑ {progress[0]}/{progress[1]}</span>{/if}
						<span title="last activity">{ago(p.last_activity, now)}</span>
						{#if c?.live}<span class="claude live">✻ now</span>
						{:else if c}<span class="claude" title={c.last_title}>✻ {ago(c.last_at, now)}</span>{/if}
					</div>
				</div>
			{/each}
		</div>
	{/each}
</div>

<style>
	.kanban {
		display: grid;
		grid-template-columns: repeat(6, minmax(190px, 1fr));
		gap: 10px;
		height: 100%;
		min-height: 0;
		overflow-x: auto;
	}
	.col {
		display: flex;
		flex-direction: column;
		gap: 8px;
		min-height: 0;
		overflow-y: auto;
		padding: 10px;
		border-radius: 10px;
		background: color-mix(in srgb, var(--st) 5%, var(--panel));
		border: 1px solid var(--line);
		transition: border-color 0.1s;
	}
	.col.over {
		border-color: var(--st);
		box-shadow: 0 0 0 1px var(--st);
	}
	h2 {
		display: flex;
		align-items: center;
		gap: 7px;
		margin: 2px 2px 4px;
		font-size: 12px;
		text-transform: uppercase;
		letter-spacing: 0.08em;
		color: var(--muted);
		font-weight: 600;
	}
	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--st);
	}
	.n {
		color: var(--faint);
		font-weight: normal;
	}
	.hint {
		margin: 4px 2px;
		font-size: 12px;
		color: var(--faint);
	}
	.item {
		padding: 9px 11px;
		border-radius: 8px;
		background: var(--card);
		border: 1px solid var(--line);
		box-shadow: var(--shadow);
		cursor: grab;
		scroll-margin: 40px;
	}
	.item.selected {
		border-color: var(--accent);
		box-shadow:
			0 0 0 1px var(--accent),
			var(--shadow);
	}
	.item.dragging {
		opacity: 0.4;
	}
	.top {
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.name {
		flex: 1;
		min-width: 0;
		font-weight: 600;
		font-size: 13.5px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.run {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--running);
		flex: none;
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
	.group {
		font-size: 11px;
		color: var(--faint);
	}
	.next {
		margin: 6px 0 0;
		font-size: 12.5px;
		line-height: 1.35;
		display: -webkit-box;
		-webkit-line-clamp: 3;
		line-clamp: 3;
		-webkit-box-orient: vertical;
		overflow: hidden;
	}
	.next.none {
		color: var(--faint);
		font-style: italic;
	}
	.meta {
		display: flex;
		gap: 8px;
		margin-top: 6px;
		font: 11px var(--mono);
		color: var(--faint);
	}
	.meta .ok {
		color: var(--hot);
	}
	.claude.live {
		color: var(--accent);
	}
</style>
