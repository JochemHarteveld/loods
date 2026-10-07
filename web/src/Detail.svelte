<script lang="ts">
	import PlanEditor from './PlanEditor.svelte';
	import { fetchBranches, type BranchList, type ClaudeSummary, type Plan, type PlanPatch, type Project, type Target } from './lib/api';
	import { ago } from './lib/time';

	type Props = {
		p: Project;
		plan: Plan | undefined;
		claude: ClaudeSummary | undefined;
		now: number;
		onclose: () => void;
		onact: (t: Target) => void;
		onsave: (patch: PlanPatch) => Promise<unknown>;
	};
	let { p, plan, claude, now, onclose, onact, onsave }: Props = $props();

	let editor = $state<ReturnType<typeof PlanEditor>>();
	export const focusNext = () => editor?.focusNext();
	export const focusTask = () => editor?.focusTask();
	export const focusNotes = () => editor?.focusNotes();

	const id = $derived(p.rel);
	const activity = $derived(p.last_activity);
	let data = $state<BranchList | null>(null);
	let error = $state('');

	// Refetch when another project is shown or this one changed on disk.
	$effect(() => {
		const current = id;
		void activity;
		if (p.kind !== 'git') {
			data = null;
			return;
		}
		let stale = false;
		error = '';
		fetchBranches(current)
			.then((d) => !stale && (data = d))
			.catch((e) => !stale && (error = e.message));
		return () => (stale = true);
	});

	const defaultName = $derived(data?.default.replace(/^origin\//, '') ?? '');
</script>

<aside>
	<header>
		<div>
			<div class="group">{p.group || 'projects'}</div>
			<h2>{p.name}</h2>
		</div>
		<button class="close" onclick={onclose} title="Close (esc)">✕</button>
	</header>

	<dl>
		<dt>path</dt>
		<dd class="mono">{p.path}</dd>
		{#if p.web_url}
			<dt>remote</dt>
			<dd><a href={p.web_url} onclick={(e) => (e.preventDefault(), onact('github'))}>{p.web_url.replace(/^https:\/\//, '')}</a></dd>
		{/if}
		<dt>activity</dt>
		<dd>
			{ago(p.last_activity, now) === 'now' ? 'just now' : `${ago(p.last_activity, now)} ago`}
			{#if p.last_commit}<span class="muted">· last commit {ago(p.last_commit, now)} ago</span>{/if}
		</dd>
		{#if p.risks?.length}
			<dt>attention</dt>
			<dd class="risks">{p.risks.join(' · ')}</dd>
		{/if}
	</dl>

	<div class="buttons">
		<button onclick={() => onact('code')}><kbd>c</kbd> VS Code</button>
		<button onclick={() => onact('terminal')}><kbd>t</kbd> Terminal</button>
		<button onclick={() => onact('folder')}><kbd>o</kbd> Folder</button>
		{#if p.web_url}<button onclick={() => onact('github')}><kbd>g</kbd> Remote</button>{/if}
	</div>

	<PlanEditor bind:this={editor} rel={p.rel} {plan} {claude} {now} {onsave} />

	{#if p.kind === 'git'}
		<h3>
			Branches
			{#if data}<span class="muted">{data.branches.length} · compared to {data.default || '—'}</span>{/if}
		</h3>
		{#if error}
			<p class="err">{error}</p>
		{:else if !data}
			<p class="muted">loading…</p>
		{:else}
			<ul class="branches">
				{#each data.branches as b (b.name)}
					<li class:current={b.current} class:merged={b.merged && b.name !== defaultName}>
						<div class="row1">
							<span class="bname">{b.current ? '● ' : ''}{b.name}</span>
							<span class="when">{ago(b.last_commit, now)}</span>
						</div>
						<div class="row2">
							{#if b.name === defaultName}<span class="tag">default</span>
							{:else if b.merged}<span class="tag ok">merged</span>
							{:else if data.default}<span class="tag" title="vs {data.default}">+{b.base_ahead} −{b.base_behind}</span>{/if}
							{#if b.upstream_gone}<span class="tag danger" title="remote branch deleted">upstream gone</span>
							{:else if !b.upstream}<span class="tag warn">local only</span>
							{:else if b.ahead || b.behind}<span class="tag info">↑{b.ahead ?? 0} ↓{b.behind ?? 0}</span>{/if}
							{#if b.worktree}<span class="tag info" title={b.worktree}>worktree</span>{/if}
							<span class="subject" title={b.subject}>{b.subject}</span>
						</div>
					</li>
				{/each}
			</ul>
		{/if}
	{/if}
</aside>

<style>
	aside {
		position: fixed;
		top: 0;
		right: 0;
		bottom: 0;
		width: min(480px, 100vw);
		background: var(--panel);
		border-left: 1px solid var(--line);
		box-shadow: -8px 0 24px rgb(0 0 0 / 0.12);
		padding: 18px 20px;
		overflow-y: auto;
		z-index: 10;
	}
	header {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
	}
	.group {
		font-size: 11px;
		text-transform: uppercase;
		letter-spacing: 0.08em;
		color: var(--muted);
	}
	h2 {
		margin: 2px 0 0;
		font-size: 20px;
	}
	.close {
		border: none;
		background: none;
		font-size: 16px;
		color: var(--muted);
		cursor: pointer;
	}
	dl {
		display: grid;
		grid-template-columns: 80px 1fr;
		gap: 6px 12px;
		margin: 16px 0;
		font-size: 13px;
	}
	dt {
		color: var(--muted);
	}
	dd {
		margin: 0;
		min-width: 0;
		overflow-wrap: anywhere;
	}
	.mono {
		font-family: var(--mono);
		font-size: 12px;
	}
	a {
		color: var(--info);
	}
	.risks {
		color: var(--accent);
	}
	.muted {
		color: var(--muted);
		font-weight: normal;
		font-size: 12px;
	}
	.err {
		color: var(--danger);
	}
	.buttons {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}
	.buttons button {
		display: flex;
		gap: 6px;
		align-items: center;
		padding: 5px 10px;
		border: 1px solid var(--line);
		border-radius: 6px;
		background: var(--card);
		cursor: pointer;
		font-size: 13px;
	}
	.buttons button:hover {
		border-color: var(--accent);
	}
	h3 {
		margin: 22px 0 8px;
		font-size: 14px;
		display: flex;
		gap: 8px;
		align-items: baseline;
	}
	.branches {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	.branches li {
		padding: 7px 10px;
		border-radius: 6px;
		background: var(--card);
		border: 1px solid transparent;
	}
	.branches li.current {
		border-color: var(--accent);
	}
	.branches li.merged {
		opacity: 0.6;
	}
	.row1 {
		display: flex;
		justify-content: space-between;
		gap: 8px;
		font: 12.5px var(--mono);
	}
	.bname {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.when {
		color: var(--muted);
		flex: none;
	}
	.row2 {
		display: flex;
		gap: 4px;
		align-items: center;
		margin-top: 4px;
		font-size: 11.5px;
		min-width: 0;
	}
	.tag {
		flex: none;
		padding: 0 6px;
		border-radius: 8px;
		background: var(--chip);
		color: var(--muted);
		font-family: var(--mono);
		font-size: 11px;
	}
	.tag.ok {
		color: var(--hot);
	}
	.tag.warn {
		background: var(--accent-soft);
		color: var(--accent);
	}
	.tag.danger {
		background: var(--danger-soft);
		color: var(--danger);
	}
	.tag.info {
		background: var(--info-soft);
		color: var(--info);
	}
	.subject {
		color: var(--muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		min-width: 0;
	}
</style>
