<script lang="ts">
	import PlanEditor from './PlanEditor.svelte';
	import {
		fetchBranches,
		runState,
		type Branch,
		type BranchList,
		type ClaudeSummary,
		type GitHubInfo,
		type Plan,
		type PlanPatch,
		type Project,
		type Target
	} from './lib/api';
	import { fixLabel, hints, type Finding } from './lib/hygiene';
	import { ago, since } from './lib/time';

	type Props = {
		p: Project;
		plan: Plan | undefined;
		claude: ClaudeSummary | undefined;
		now: number;
		onclose: () => void;
		onact: (t: Target) => void;
		onsave: (patch: PlanPatch) => Promise<unknown>;
		gh: GitHubInfo | undefined;
		findings: Finding[];
		version: number; // bumped after git cleanups: refetch branches
		onfix: (f: Finding) => void;
		ondelete: (branches: Branch[], data: BranchList) => void;
		onundo: () => void;
		// Inline mode is the project page's Git tab: no drawer chrome, and no plan
		// editor, because the Plan tab already has one.
		inline?: boolean;
	};
	let { p, plan, claude, now, onclose, onact, onsave, gh, findings, version, onfix, ondelete, onundo, inline = false }: Props = $props();

	let editor = $state<ReturnType<typeof PlanEditor>>();
	export const focusNext = () => editor?.focusNext();
	export const focusNotes = () => editor?.focusNotes();

	const id = $derived(p.rel);
	const activity = $derived(p.last_activity);
	let data = $state<BranchList | null>(null);
	let error = $state('');

	// Refetch when another project is shown or this one changed on disk.
	$effect(() => {
		const current = id;
		void activity;
		void version;
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
	const deletable = (b: Branch) => !b.current && b.name !== defaultName;
	const mergedOnes = $derived(data?.branches.filter((b) => deletable(b) && b.merged) ?? []);
	const goneOnes = $derived(data?.branches.filter((b) => deletable(b) && !b.merged && b.upstream_gone) ?? []);
	const prFor = (branch: string) => gh?.prs.find((pr) => pr.branch === branch);
	const ciState = $derived(gh?.ci ? runState(gh.ci) : null);
</script>

<aside class:inline>
	{#if !inline}
		<header>
			<div>
				<div class="group">{p.group || 'projects'}</div>
				<h2>{p.name}</h2>
			</div>
			<button class="close" onclick={onclose} title="Close (esc)">✕</button>
		</header>
	{/if}

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
		{#if gh}
			<dt>github</dt>
			<dd>
				{#if gh.error}<span class="muted">{gh.error}</span>
				{:else}
					{#if gh.ci}
						<a class="ci {ciState}" href={gh.ci.url} target="_blank" rel="noreferrer">
							{ciState === 'pass' ? '✓' : ciState === 'fail' ? '✗' : '●'} {gh.ci.workflow}
						</a>
						<span class="muted">on {gh.ci.branch} · {since(gh.ci.created_at, now)} ·</span>
					{/if}
					<a href="https://github.com/{gh.repo}/pulls" target="_blank" rel="noreferrer">{gh.prs.length} open PR{gh.prs.length === 1 ? '' : 's'}</a>
					· <a href="https://github.com/{gh.repo}/issues" target="_blank" rel="noreferrer">{gh.issues} issue{gh.issues === 1 ? '' : 's'}</a>
				{/if}
			</dd>
		{/if}
	</dl>

	{#if !inline}
		<div class="buttons">
			<button onclick={() => onact('code')}><kbd>c</kbd> VS Code</button>
			<button onclick={() => onact('terminal')}><kbd>t</kbd> Terminal</button>
			<button onclick={() => onact('folder')}><kbd>o</kbd> Folder</button>
			{#if p.web_url}<button onclick={() => onact('github')}><kbd>g</kbd> Remote</button>{/if}
		</div>
	{/if}

	{#if findings.length}
		<ul class="findings">
			{#each findings as f (f.kind + f.text)}
				<li class={f.level}>
					<span class="fdot"></span>
					<span class="ftext">{f.text}</span>
					{#if f.fix}<button onclick={() => onfix(f)}>{fixLabel[f.fix]}</button>
					{:else if f.url}<a href={f.url} target="_blank" rel="noreferrer">open ↗</a>
					{:else if hints[f.kind]}<span class="fhint">{hints[f.kind]}</span>{/if}
				</li>
			{/each}
		</ul>
	{/if}

	{#if !inline}
		<PlanEditor bind:this={editor} rel={p.rel} {plan} {claude} {now} {onsave} />
	{/if}

	{#if gh?.prs.length}
		<h3>Pull requests <span class="muted">{gh.prs.length} open</span></h3>
		<ul class="prs">
			{#each gh.prs as pr (pr.number)}
				<li>
					<a href={pr.url} target="_blank" rel="noreferrer">
						<span class="num">#{pr.number}</span>
						<span class="ptitle">{pr.title}</span>
					</a>
					<span class="pmeta">
						{#if pr.draft}<span class="tag">draft</span>{/if}
						{#if pr.checks}<span class="tag {pr.checks === 'pass' ? 'ok' : pr.checks === 'fail' ? 'danger' : 'warn'}">checks {pr.checks}</span>{/if}
						{#if pr.review === 'APPROVED'}<span class="tag ok">approved</span>
						{:else if pr.review === 'CHANGES_REQUESTED'}<span class="tag danger">changes requested</span>{/if}
						<span class="mono">⎇ {pr.branch}</span>
					</span>
				</li>
			{/each}
		</ul>
	{/if}

	{#if p.kind === 'git'}
		<h3>
			Branches
			{#if data}<span class="muted">{data.branches.length} · compared to {data.default || '—'}</span>{/if}
		</h3>
		{#if data && (mergedOnes.length || goneOnes.length || data.undo)}
			<div class="cleanup">
				{#if mergedOnes.length}
					<button onclick={() => data && ondelete(mergedOnes, data)}>Delete {mergedOnes.length} merged</button>
				{/if}
				{#if goneOnes.length}
					<button onclick={() => data && ondelete(goneOnes, data)} title="Their remote branch was deleted, usually after a squash-merged PR"
						>Delete {goneOnes.length} with deleted remote</button
					>
				{/if}
				{#if data.undo}
					<button class="undo" onclick={onundo}>
						Undo cleanup ({data.undo.branches.length} branch{data.undo.branches.length === 1 ? '' : 'es'}, {since(data.undo.time, now)})
					</button>
				{/if}
			</div>
		{/if}
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
							{#if prFor(b.name)}
								{@const pr = prFor(b.name)}
								<a class="prtag" href={pr?.url} target="_blank" rel="noreferrer" title={pr?.title}>#{pr?.number}</a>
							{/if}
							<span class="when">{ago(b.last_commit, now)}</span>
							{#if data && deletable(b)}
								<button class="del" title="Delete branch (undoable)" onclick={() => data && ondelete([b], data)}>✕</button>
							{/if}
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
	aside.inline {
		position: static;
		width: auto;
		max-width: 760px;
		border-left: 0;
		box-shadow: none;
		background: none;
		padding: 0;
		height: 100%;
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
	.ci {
		text-decoration: none;
		font-weight: 600;
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
	.findings {
		list-style: none;
		margin: 16px 0 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.findings li {
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 12.5px;
		padding: 6px 10px;
		border-radius: 7px;
		background: var(--card);
	}
	.fdot {
		flex: none;
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--info);
	}
	.warn .fdot {
		background: var(--accent);
	}
	.danger .fdot {
		background: var(--danger);
	}
	.danger .ftext {
		color: var(--danger);
	}
	.ftext {
		flex: 1;
		min-width: 0;
		overflow-wrap: anywhere;
	}
	.findings button,
	.cleanup button {
		flex: none;
		font-size: 12px;
		padding: 2px 9px;
		border: 1px solid var(--line);
		border-radius: 6px;
		background: var(--panel);
		cursor: pointer;
	}
	.findings button:hover,
	.cleanup button:hover {
		border-color: var(--accent);
	}
	.fhint {
		flex: none;
		max-width: 50%;
		color: var(--faint);
		font-size: 11.5px;
		text-align: right;
	}
	.cleanup {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		margin-bottom: 8px;
	}
	.cleanup .undo {
		color: var(--info);
	}
	.prs {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.prs li {
		padding: 7px 10px;
		border-radius: 6px;
		background: var(--card);
	}
	.prs a {
		display: flex;
		gap: 6px;
		text-decoration: none;
		color: var(--text);
		font-size: 13px;
	}
	.prs a:hover .ptitle {
		color: var(--info);
	}
	.num {
		color: var(--faint);
		font-family: var(--mono);
		font-size: 12px;
	}
	.ptitle {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		min-width: 0;
	}
	.pmeta {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
		margin-top: 4px;
		font-size: 11.5px;
		color: var(--muted);
		align-items: center;
	}
	.prtag {
		flex: none;
		font-size: 11px;
		color: var(--info);
		text-decoration: none;
	}
	.del {
		flex: none;
		border: none;
		background: none;
		color: var(--faint);
		cursor: pointer;
		font-size: 11px;
		padding: 0 2px;
		opacity: 0;
	}
	.branches li:hover .del {
		opacity: 1;
	}
	.del:hover {
		color: var(--danger);
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
		flex: 1;
		min-width: 0;
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
