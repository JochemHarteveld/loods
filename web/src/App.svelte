<script lang="ts">
	import { onMount, tick } from 'svelte';
	import Card from './Card.svelte';
	import Detail from './Detail.svelte';
	import { openIn, rescan, subscribe, type Project, type Target } from './lib/api';
	import { ago } from './lib/time';

	type Group = { name: string; items: Project[]; latest: number };

	let projects = $state<Project[]>([]);
	let root = $state('');
	let scannedAt = $state('');
	let scanning = $state(false);
	let connected = $state(false);
	let loaded = $state(false);
	let now = $state(Date.now());
	let query = $state('');
	let sortBy = $state<'activity' | 'name'>('activity');
	let selectedId = $state<string | null>(null);
	let detailOpen = $state(false);
	let helpOpen = $state(false);
	let toast = $state<{ text: string; error: boolean } | null>(null);
	let filterEl = $state<HTMLInputElement>();
	let toastTimer: ReturnType<typeof setTimeout> | undefined;

	const time = (p: Project) => Date.parse(p.last_activity) || 0;

	const groups = $derived.by(() => {
		const q = query.trim().toLowerCase();
		const byName = new Map<string, Group>();
		for (const p of projects) {
			if (q && ![p.rel, p.branch ?? '', ...(p.stack ?? [])].join(' ').toLowerCase().includes(q)) continue;
			let g = byName.get(p.group);
			if (!g) byName.set(p.group, (g = { name: p.group, items: [], latest: 0 }));
			g.items.push(p);
			g.latest = Math.max(g.latest, time(p));
		}
		const list = [...byName.values()];
		if (sortBy === 'activity') {
			list.forEach((g) => g.items.sort((a, b) => time(b) - time(a)));
			list.sort((a, b) => b.latest - a.latest);
		} else {
			list.forEach((g) => g.items.sort((a, b) => a.name.localeCompare(b.name)));
			list.sort((a, b) => (a.name || '~').localeCompare(b.name || '~'));
		}
		return list;
	});
	const flat = $derived(groups.flatMap((g) => g.items));
	const selected = $derived(flat.find((p) => p.rel === selectedId) ?? null);
	const attention = $derived(projects.filter((p) => p.dirty_files || p.unpushed_commits || !p.has_remote).length);

	// Keep a valid selection when the filter or data changes.
	$effect(() => {
		if (!selected && flat.length) selectedId = flat[0].rel;
	});

	onMount(() => {
		const stop = subscribe(
			(s) => {
				projects = s.projects;
				root = s.root;
				scannedAt = s.scanned_at;
				scanning = s.scanning;
				loaded = true;
			},
			(up) => (connected = up)
		);
		const clock = setInterval(() => (now = Date.now()), 15_000);
		return () => {
			stop();
			clearInterval(clock);
		};
	});

	function flash(text: string, error = false) {
		toast = { text, error };
		clearTimeout(toastTimer);
		toastTimer = setTimeout(() => (toast = null), error ? 4000 : 1800);
	}

	const labels: Record<Target, string> = { code: 'VS Code', terminal: 'Terminal', folder: 'Folder', github: 'Remote' };
	async function act(p: Project | null, target: Target) {
		if (!p) return;
		try {
			await openIn(p.rel, target);
			flash(`${labels[target]} → ${p.name}`);
		} catch (e) {
			flash(`${labels[target]}: ${(e as Error).message}`, true);
		}
	}

	async function doRescan() {
		try {
			await rescan();
			flash('Rescanning…');
		} catch (e) {
			flash((e as Error).message, true);
		}
	}

	const cardEl = (id: string) => document.querySelector<HTMLElement>(`[data-id="${CSS.escape(id)}"]`);

	async function select(p: Project | undefined) {
		if (!p) return;
		selectedId = p.rel;
		await tick();
		cardEl(p.rel)?.scrollIntoView({ block: 'nearest' });
	}

	// Left/right walk the list; up/down jump to the nearest card on the next
	// visual row, so movement follows the grid even across group headers.
	function move(dir: 'left' | 'right' | 'up' | 'down') {
		const i = flat.findIndex((p) => p.rel === selectedId);
		if (i < 0) return select(flat[0]);
		if (dir === 'left') return select(flat[Math.max(0, i - 1)]);
		if (dir === 'right') return select(flat[Math.min(flat.length - 1, i + 1)]);
		const rects = flat.map((p) => cardEl(p.rel)?.getBoundingClientRect());
		const cur = rects[i];
		if (!cur) return;
		const cx = cur.left + cur.width / 2;
		const down = dir === 'down';
		let best = -1;
		let bestTop = down ? Infinity : -Infinity;
		let bestDx = Infinity;
		rects.forEach((r, j) => {
			if (!r || (down ? r.top <= cur.top + 4 : r.top >= cur.top - 4)) return;
			const dx = Math.abs(r.left + r.width / 2 - cx);
			const closerRow = down ? r.top < bestTop - 4 : r.top > bestTop + 4;
			if (closerRow || (Math.abs(r.top - bestTop) <= 4 && dx < bestDx)) {
				best = j;
				bestTop = r.top;
				bestDx = dx;
			}
		});
		if (best >= 0) select(flat[best]);
	}

	function focusFilter() {
		filterEl?.focus();
		filterEl?.select();
	}

	function onkeydown(e: KeyboardEvent) {
		if (e.ctrlKey && e.key === 'k') {
			e.preventDefault();
			return focusFilter();
		}
		if (e.ctrlKey || e.metaKey || e.altKey) return;
		if (e.target === filterEl) {
			if (e.key === 'Escape') {
				query = '';
				filterEl?.blur();
			} else if (e.key === 'Enter' || e.key === 'ArrowDown') {
				filterEl?.blur();
				select(flat[0]);
			} else return;
			e.preventDefault();
			return;
		}
		switch (e.key) {
			case 'ArrowLeft':
			case 'h':
				move('left');
				break;
			case 'ArrowRight':
			case 'l':
				move('right');
				break;
			case 'ArrowUp':
			case 'k':
				move('up');
				break;
			case 'ArrowDown':
			case 'j':
				move('down');
				break;
			case 'Enter':
			case ' ':
				detailOpen = !detailOpen;
				break;
			case 'Escape':
				if (helpOpen) helpOpen = false;
				else if (detailOpen) detailOpen = false;
				else query = '';
				break;
			case '/':
				focusFilter();
				break;
			case 'c':
				act(selected, 'code');
				break;
			case 't':
				act(selected, 'terminal');
				break;
			case 'o':
				act(selected, 'folder');
				break;
			case 'g':
				act(selected, 'github');
				break;
			case 'R':
				doRescan();
				break;
			case 's':
				sortBy = sortBy === 'activity' ? 'name' : 'activity';
				flash(`Sorted by ${sortBy}`);
				break;
			case '?':
				helpOpen = !helpOpen;
				break;
			default:
				return;
		}
		e.preventDefault();
	}

	const keys: [string, string][] = [
		['←↓↑→ / hjkl', 'move'],
		['enter', 'details & branches'],
		['c', 'open in VS Code'],
		['t', 'terminal in project'],
		['o', 'open folder'],
		['g', 'open remote (GitHub)'],
		['/  ctrl+k', 'filter'],
		['s', 'sort by activity / name'],
		['R', 'rescan now'],
		['esc', 'close / clear filter'],
		['?', 'this help']
	];
</script>

<svelte:window {onkeydown} />

<div class="app" class:with-detail={detailOpen && selected}>
	<header class="top">
		<div class="brand">
			<img src="/icon.svg" alt="" width="22" height="22" />
			<span>loods</span>
		</div>
		<nav>
			<span class="tab active">Board</span>
			<span class="tab soon" title="phase 2">Garage</span>
			<span class="tab soon" title="phase 3">Plans</span>
			<span class="tab soon" title="phase 4">Graveyard</span>
		</nav>
		<input
			bind:this={filterEl}
			bind:value={query}
			class="filter"
			placeholder="Filter projects, branches, stack…   /"
			spellcheck="false"
		/>
		<div class="status">
			<span>{projects.length} projects</span>
			{#if attention}<span class="attn" title="uncommitted, unpushed or no remote">{attention} need attention</span>{/if}
			<span class="scan" class:busy={scanning} title={root}>
				<span class="conn" class:up={connected}></span>
				{scanning ? 'scanning…' : scannedAt ? (ago(scannedAt, now) === 'now' ? 'scanned just now' : `scanned ${ago(scannedAt, now)} ago`) : 'connecting…'}
			</span>
		</div>
	</header>

	<main>
		<div role="listbox" aria-label="Projects">
			{#if !loaded}
				<p class="empty">Connecting to loods…</p>
			{:else if !flat.length}
				<p class="empty">{query ? `Nothing matches “${query}”.` : `No projects found under ${root}.`}</p>
			{/if}
			{#each groups as g (g.name)}
				<section>
					<h2>{g.name || 'projects'} <span>{g.items.length}</span></h2>
					<div class="grid">
						{#each g.items as p (p.rel)}
							<Card
								{p}
								{now}
								selected={p.rel === selectedId}
								onselect={() => (selectedId = p.rel)}
								ondetail={() => ((selectedId = p.rel), (detailOpen = true))}
								onact={(t) => act(p, t)}
							/>
						{/each}
					</div>
				</section>
			{/each}
		</div>
	</main>

	<footer class="keys">
		<span><kbd>hjkl</kbd> move</span>
		<span><kbd>enter</kbd> details</span>
		<span><kbd>c</kbd> code</span>
		<span><kbd>t</kbd> terminal</span>
		<span><kbd>g</kbd> remote</span>
		<span><kbd>/</kbd> filter</span>
		<span><kbd>s</kbd> sort: {sortBy}</span>
		<span><kbd>?</kbd> help</span>
	</footer>
</div>

{#if detailOpen && selected}
	<Detail p={selected} {now} onclose={() => (detailOpen = false)} onact={(t) => act(selected, t)} />
{/if}

{#if helpOpen}
	<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
	<div class="overlay" onclick={() => (helpOpen = false)}>
		<div class="help">
			<h2>Keys</h2>
			<dl>
				{#each keys as [k, v] (k)}
					<dt><kbd>{k}</kbd></dt>
					<dd>{v}</dd>
				{/each}
			</dl>
		</div>
	</div>
{/if}

{#if toast}
	<div class="toast" class:error={toast.error}>{toast.text}</div>
{/if}

<style>
	.app {
		min-height: 100vh;
		display: flex;
		flex-direction: column;
		transition: padding-right 0.15s;
	}
	.app.with-detail {
		padding-right: min(480px, 100vw);
	}

	.top {
		position: sticky;
		top: 0;
		z-index: 5;
		display: flex;
		align-items: center;
		gap: 20px;
		padding: 10px 24px;
		background: color-mix(in srgb, var(--bg) 88%, transparent);
		backdrop-filter: blur(8px);
		border-bottom: 1px solid var(--line);
	}
	.brand {
		display: flex;
		align-items: center;
		gap: 8px;
		font-weight: 700;
		font-size: 16px;
		letter-spacing: -0.01em;
	}
	nav {
		display: flex;
		gap: 2px;
	}
	.tab {
		padding: 4px 10px;
		border-radius: 6px;
		font-size: 13px;
		color: var(--muted);
	}
	.tab.active {
		background: var(--card);
		color: var(--text);
		box-shadow: var(--shadow);
	}
	.tab.soon {
		opacity: 0.45;
	}
	.filter {
		flex: 1;
		max-width: 420px;
		padding: 6px 10px;
		border: 1px solid var(--line);
		border-radius: 7px;
		background: var(--card);
		color: var(--text);
		font: inherit;
		outline: none;
	}
	.filter:focus {
		border-color: var(--accent);
	}
	.status {
		margin-left: auto;
		display: flex;
		gap: 14px;
		align-items: center;
		font-size: 12.5px;
		color: var(--muted);
		white-space: nowrap;
	}
	.attn {
		color: var(--accent);
	}
	.scan {
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.scan.busy {
		color: var(--accent);
	}
	.conn {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--danger);
	}
	.conn.up {
		background: var(--hot);
	}

	main {
		flex: 1;
		padding: 8px 24px 24px;
	}
	section {
		margin-top: 18px;
	}
	h2 {
		margin: 0 0 10px;
		font-size: 12px;
		text-transform: uppercase;
		letter-spacing: 0.08em;
		color: var(--muted);
		font-weight: 600;
	}
	h2 span {
		color: var(--faint);
		margin-left: 4px;
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(270px, 1fr));
		gap: 12px;
	}
	.empty {
		color: var(--muted);
		text-align: center;
		margin-top: 80px;
	}

	.keys {
		position: sticky;
		bottom: 0;
		display: flex;
		flex-wrap: wrap;
		gap: 4px 18px;
		padding: 8px 24px;
		font-size: 12px;
		color: var(--muted);
		background: color-mix(in srgb, var(--bg) 92%, transparent);
		backdrop-filter: blur(8px);
		border-top: 1px solid var(--line);
	}
	.keys span {
		display: flex;
		gap: 6px;
		align-items: center;
	}

	.overlay {
		position: fixed;
		inset: 0;
		background: rgb(0 0 0 / 0.35);
		display: grid;
		place-items: center;
		z-index: 20;
	}
	.help {
		background: var(--panel);
		border: 1px solid var(--line);
		border-radius: 12px;
		padding: 18px 24px;
		min-width: 320px;
	}
	.help h2 {
		margin-bottom: 14px;
	}
	.help dl {
		display: grid;
		grid-template-columns: auto 1fr;
		gap: 8px 16px;
		margin: 0;
	}
	.help dd {
		margin: 0;
	}

	.toast {
		position: fixed;
		bottom: 52px;
		left: 50%;
		transform: translateX(-50%);
		padding: 8px 14px;
		border-radius: 8px;
		background: var(--text);
		color: var(--bg);
		font-size: 13px;
		z-index: 30;
		box-shadow: 0 4px 16px rgb(0 0 0 / 0.2);
	}
	.toast.error {
		background: var(--danger);
		color: #fff;
	}

	@media (max-width: 760px) {
		.top {
			flex-wrap: wrap;
			padding: 10px 16px;
		}
		nav,
		.status {
			display: none;
		}
		main,
		.keys {
			padding-left: 16px;
			padding-right: 16px;
		}
	}
</style>
