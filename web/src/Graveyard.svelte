<script lang="ts">
	import type { Batch, Plan, Project } from './lib/api';
	import { ago, bytes, since } from './lib/time';

	type Props = {
		items: Project[]; // filtered and sorted by the App
		plans: Record<string, Plan>;
		batches: Batch[];
		marked: Set<string>;
		selectedId: string | null;
		loading: boolean;
		scannedAt: string;
		archive: string;
		sortBy: string;
		dupesOnly: boolean;
		now: number;
		onselect: (rel: string) => void;
		ontoggle: (rel: string) => void;
		onundo: () => void;
	};
	let { items, plans, batches, marked, selectedId, loading, scannedAt, archive, sortBy, dupesOnly, now, onselect, ontoggle, onundo }: Props = $props();

	const total = $derived(items.reduce((n, it) => n + (it.size_bytes ?? 0), 0));
	const markedSize = $derived(items.filter((it) => marked.has(it.rel)).reduce((n, it) => n + (it.size_bytes ?? 0), 0));
	const undoable = $derived(batches.find((b) => b.action === 'archive' && !b.restored));
	const kindLabel: Record<string, string> = { git: 'git', proj: 'no git', dir: 'folder', zip: 'archive' };
</script>

<div class="grave">
	<header>
		<h2>Graveyard</h2>
		<span class="muted">
			{items.length} items · {bytes(total)}
			{#if marked.size}· <b>{marked.size} marked ({bytes(markedSize)})</b>{/if}
			· sorted by {sortBy}{dupesOnly ? ' · duplicates only' : ''}
			· {loading ? 'measuring sizes…' : `scanned ${since(scannedAt, now)}`}
		</span>
	</header>

	<table>
		<thead>
			<tr><th></th><th>name</th><th>kind</th><th class="r">age</th><th class="r">size</th><th>flags</th></tr>
		</thead>
		<tbody>
			{#each items as it (it.rel)}
				{@const plan = plans[it.rel]}
				<tr
					class:selected={it.rel === selectedId}
					class:marked={marked.has(it.rel)}
					class:dead={plan?.status === 'dead'}
					data-id={it.rel}
					onclick={() => onselect(it.rel)}
				>
					<td class="box">
						<input type="checkbox" checked={marked.has(it.rel)} onclick={(e) => (e.stopPropagation(), ontoggle(it.rel))} aria-label="mark {it.rel}" />
					</td>
					<td class="name" title={it.path}>
						{#if it.group}<span class="grp">{it.group}/</span>{/if}{it.name}
					</td>
					<td class="kind">{kindLabel[it.kind] ?? it.kind}</td>
					<td class="r mono">{ago(it.last_activity, now)}</td>
					<td class="r mono">{bytes(it.size_bytes)}</td>
					<td><div class="flags">
						{#if plan?.status === 'dead'}<span class="f dead">marked dead</span>{:else if plan?.status}<span class="f">{plan.status}</span>{/if}
						{#if it.possible_duplicates?.length}<span class="f info" title={it.possible_duplicates.join(', ')}>≈ {it.possible_duplicates.join(', ')}</span>{/if}
						{#each it.risks ?? [] as r (r)}<span class="f warn">{r}</span>{/each}
						{#if !it.size_bytes && it.kind !== 'zip'}<span class="f">empty</span>{/if}
					</div></td>
				</tr>
			{/each}
		</tbody>
	</table>

	<section class="log">
		<h3>
			Recent burials
			{#if undoable}<button onclick={onundo}><kbd>u</kbd> undo the last archive ({undoable.items.length})</button>{/if}
		</h3>
		{#if !batches.length}<p class="muted">Nothing buried yet. Archive moves into <code>{archive}</code>; trash goes to the system trash.</p>{/if}
		<ul>
			{#each batches as b (b.batch + b.action)}
				<li class:restored={b.restored}>
					<span class="when">{ago(b.time, now)}</span>
					<span class="act {b.action}">{b.restored ? 'restored' : b.action === 'archive' ? 'archived' : 'trashed'}</span>
					<span class="what" title={b.items.join('\n')}>
						{b.items.slice(0, 8).map((p) => p.split('/').slice(-1)[0]).join(', ')}{b.items.length > 8 ? ` +${b.items.length - 8} more` : ''}
					</span>
				</li>
			{/each}
		</ul>
	</section>
</div>

<style>
	.grave {
		max-width: 1180px;
		overflow-x: auto;
	}
	header {
		display: flex;
		align-items: baseline;
		gap: 14px;
		flex-wrap: wrap;
		margin: 6px 0 12px;
	}
	h2 {
		margin: 0;
		font-size: 12px;
		text-transform: uppercase;
		letter-spacing: 0.08em;
		color: var(--muted);
	}
	.muted {
		font-size: 12.5px;
		color: var(--muted);
	}
	table {
		width: 100%;
		border-collapse: collapse;
		font-size: 13px;
	}
	th {
		text-align: left;
		font-weight: 600;
		font-size: 11.5px;
		color: var(--faint);
		padding: 4px 8px;
		border-bottom: 1px solid var(--line);
	}
	td {
		padding: 5px 8px;
		border-bottom: 1px solid color-mix(in srgb, var(--line) 50%, transparent);
		vertical-align: top;
	}
	tr {
		scroll-margin: 60px;
	}
	tbody tr:hover {
		background: var(--card-hover);
	}
	tr.selected td {
		background: var(--accent-soft);
	}
	tr.marked .name {
		color: var(--danger);
		font-weight: 600;
	}
	tr.dead .name {
		text-decoration: line-through;
		text-decoration-color: var(--faint);
	}
	.r {
		text-align: right;
		white-space: nowrap;
	}
	.mono {
		font-family: var(--mono);
		font-size: 12px;
	}
	.box {
		width: 24px;
	}
	.box input {
		accent-color: var(--danger);
	}
	.name {
		white-space: nowrap;
	}
	.grp {
		color: var(--faint);
	}
	.kind {
		color: var(--muted);
		white-space: nowrap;
	}
	.flags {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
	}
	.f {
		font-size: 11.5px;
		padding: 0 7px;
		border-radius: 9px;
		background: var(--chip);
		color: var(--muted);
	}
	.f.warn {
		background: var(--accent-soft);
		color: var(--accent);
	}
	.f.info {
		background: var(--info-soft);
		color: var(--info);
	}
	.f.dead {
		background: var(--danger-soft);
		color: var(--danger);
	}
	.log {
		margin-top: 26px;
	}
	h3 {
		display: flex;
		align-items: center;
		gap: 12px;
		font-size: 13px;
		margin: 0 0 8px;
	}
	h3 button {
		font-size: 12px;
		padding: 3px 10px;
		border: 1px solid var(--line);
		border-radius: 6px;
		background: var(--card);
		cursor: pointer;
		font-weight: normal;
	}
	.log ul {
		list-style: none;
		margin: 0;
		padding: 0;
		font-size: 12.5px;
	}
	.log li {
		display: flex;
		gap: 10px;
		padding: 2px 0;
	}
	.log li.restored {
		opacity: 0.55;
	}
	.when {
		width: 34px;
		font-family: var(--mono);
		color: var(--faint);
	}
	.act {
		width: 64px;
	}
	.act.trash {
		color: var(--danger);
	}
	.what {
		color: var(--muted);
	}
	@media (max-width: 760px) {
		.kind,
		th:nth-child(3) {
			display: none;
		}
	}
</style>
