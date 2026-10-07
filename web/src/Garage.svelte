<script lang="ts">
	import Terminal from './Terminal.svelte';
	import { isAlive, type Proc, type Project, type Stack } from './lib/api';
	import { ago, bytes } from './lib/time';

	export type ProcAction = 'stop' | 'restart' | 'remove' | 'reload' | 'hot-restart';

	type Props = {
		procs: Proc[];
		stacks: Stack[];
		projects: Project[];
		selectedId: string | null;
		now: number;
		configPath: string;
		onselect: (id: string) => void;
		onaction: (a: ProcAction, p: Proc) => void;
		onstack: (s: Stack) => void;
	};
	let { procs, stacks, projects, selectedId, now, configPath, onselect, onaction, onstack }: Props = $props();

	let terminal = $state<ReturnType<typeof Terminal>>();
	export function focusTerminal() {
		terminal?.focus();
	}

	const selected = $derived(procs.find((p) => p.id === selectedId) ?? null);
	const projectName = (rel?: string) => (rel ? (projects.find((p) => p.rel === rel)?.name ?? rel) : '');
	const stackProc = (s: Stack) => procs.find((p) => p.id === 'stack:' + s.name);

	function statusText(p: Proc) {
		switch (p.status) {
			case 'running':
				return `up ${ago(p.started_at, now)}`;
			case 'stopping':
				return 'stopping…';
			case 'orphan':
				return 'orphan · from an earlier loods';
			default:
				return `${p.stopped ? 'stopped' : p.exit_code === 0 ? 'exited' : p.exit_code < 0 ? 'killed' : `failed (${p.exit_code})`} · ${ago(p.ended_at, now)} ago`;
		}
	}
	const failed = (p: Proc) => p.status === 'exited' && !p.stopped && p.exit_code !== 0;
</script>

<div class="garage">
	<aside class="list">
		<h2>Processes <span>{procs.filter(isAlive).length} running</span></h2>
		{#if !procs.length}
			<p class="empty">Nothing running. Pick a project on the Board and press <kbd>r</kbd>.</p>
		{/if}
		<ul>
			{#each procs as p (p.id)}
				<li>
					<button class="proc" class:selected={p.id === selectedId} onclick={() => onselect(p.id)}>
						<span class="dot {p.status}" class:failed={failed(p)}></span>
						<span class="body">
							<span class="title">
								{#if p.project}<b>{projectName(p.project)}</b> · {p.name}{:else}<b>{p.name}</b> <em>stack</em>{/if}
							</span>
							<span class="sub">{statusText(p)}</span>
							{#if isAlive(p) && (p.rss_bytes || p.ports?.length || p.warning)}
								<span class="meta">
									{#if p.rss_bytes}<span>{bytes(p.rss_bytes)}</span>{/if}
									{#each p.ports ?? [] as port (port)}<span class="port">:{port}</span>{/each}
									{#if p.warning}<span class="warn">{p.warning}</span>{/if}
								</span>
							{/if}
						</span>
					</button>
				</li>
			{/each}
		</ul>

		<h2>Stacks</h2>
		{#if !stacks.length}
			<p class="empty">No stacks. Add one under <code>stacks:</code> in<br /><code>{configPath}</code></p>
		{/if}
		<ul>
			{#each stacks as s (s.name)}
				{@const sp = stackProc(s)}
				<li class="stack">
					<div class="body">
						<span class="title"><b>{s.name}</b></span>
						<span class="sub mono">{s.run ?? s.commands?.join(' + ')}</span>
					</div>
					{#if sp && isAlive(sp)}
						<button class="small" onclick={() => onselect(sp.id)}>view</button>
					{:else}
						<button class="small go" onclick={() => onstack(s)}>▶ start</button>
					{/if}
				</li>
			{/each}
		</ul>
	</aside>

	<section class="pane">
		{#if selected}
			<header>
				<div class="info">
					<div class="title">
						<span class="dot {selected.status}" class:failed={failed(selected)}></span>
						{#if selected.project}{projectName(selected.project)} · {selected.name}{:else}{selected.name}{/if}
						<span class="status">{statusText(selected)}</span>
					</div>
					<code class="run" title={selected.dir}>$ {selected.run}</code>
				</div>
				<div class="buttons">
					{#each selected.urls ?? [] as u, i (u)}
						<a class="btn link" href={u} target="_blank" rel="noreferrer" title={u}>
							{#if i === 0}<kbd>w</kbd>{/if}
							{u.replace(/^https?:\/\//, '').replace(/\/$/, '')} ↗
						</a>
					{/each}
					{#if selected.keys && selected.status === 'running'}
						<button class="btn" onclick={() => onaction('reload', selected)}><kbd>u</kbd> reload</button>
						<button class="btn" onclick={() => onaction('hot-restart', selected)}><kbd>U</kbd> hot restart</button>
					{/if}
					{#if isAlive(selected)}
						<button class="btn" onclick={() => onaction('restart', selected)}><kbd>r</kbd> restart</button>
						<button class="btn danger" onclick={() => onaction('stop', selected)}>
							<kbd>x</kbd>
							{selected.status === 'stopping' ? 'kill now' : 'stop'}
						</button>
					{:else}
						<button class="btn go" onclick={() => onaction('restart', selected)}><kbd>r</kbd> run again</button>
						<button class="btn" onclick={() => onaction('remove', selected)}><kbd>del</kbd> remove</button>
					{/if}
				</div>
			</header>
			<div class="termwrap">
				{#key selected.id}
					<Terminal bind:this={terminal} id={selected.id} />
				{/key}
			</div>
		{:else}
			<div class="placeholder">
				<p>Select a process to see its terminal.</p>
				<p class="hint"><kbd>j</kbd>/<kbd>k</kbd> select · <kbd>i</kbd> type into terminal · <kbd>esc</kbd> leave it</p>
			</div>
		{/if}
	</section>
</div>

<style>
	.garage {
		display: grid;
		grid-template-columns: 320px 1fr;
		gap: 16px;
		height: 100%;
		min-height: 0;
	}
	.list {
		overflow-y: auto;
		min-height: 0;
	}
	h2 {
		margin: 6px 0 8px;
		font-size: 12px;
		text-transform: uppercase;
		letter-spacing: 0.08em;
		color: var(--muted);
		font-weight: 600;
	}
	h2 span {
		color: var(--faint);
		text-transform: none;
		letter-spacing: 0;
		font-weight: normal;
		margin-left: 6px;
	}
	ul {
		list-style: none;
		margin: 0 0 20px;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.empty {
		color: var(--muted);
		font-size: 13px;
		margin: 0 0 20px;
	}
	.empty code {
		font-size: 11.5px;
		overflow-wrap: anywhere;
	}
	.proc {
		width: 100%;
		display: flex;
		gap: 10px;
		align-items: flex-start;
		text-align: left;
		padding: 9px 12px;
		border: 1px solid var(--line);
		border-radius: 8px;
		background: var(--card);
		cursor: pointer;
	}
	.proc.selected {
		border-color: var(--accent);
		box-shadow: 0 0 0 1px var(--accent);
	}
	.body {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
		flex: 1;
	}
	.title {
		font-size: 13.5px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.title em {
		font-style: normal;
		font-size: 11px;
		color: var(--muted);
		border: 1px solid var(--line);
		border-radius: 4px;
		padding: 0 4px;
	}
	.sub {
		font-size: 12px;
		color: var(--muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.mono {
		font-family: var(--mono);
		font-size: 11.5px;
	}
	.meta {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
		margin-top: 3px;
		font: 11px var(--mono);
		color: var(--muted);
	}
	.meta span {
		background: var(--chip);
		padding: 0 5px;
		border-radius: 4px;
	}
	.meta .port {
		background: var(--info-soft);
		color: var(--info);
	}
	.meta .warn {
		background: var(--danger-soft);
		color: var(--danger);
	}
	.dot {
		width: 9px;
		height: 9px;
		border-radius: 50%;
		margin-top: 5px;
		flex: none;
		background: var(--cold);
	}
	.dot.running {
		background: var(--running);
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--running) 22%, transparent);
		animation: pulse 2s ease-in-out infinite;
	}
	.dot.stopping {
		background: var(--warm);
	}
	.dot.orphan {
		background: var(--orphan);
	}
	.dot.failed {
		background: var(--danger);
	}
	@keyframes pulse {
		50% {
			box-shadow: 0 0 0 5px color-mix(in srgb, var(--running) 8%, transparent);
		}
	}
	.stack {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 9px 12px;
		border: 1px dashed var(--line);
		border-radius: 8px;
	}
	.small {
		flex: none;
		font-size: 12px;
		padding: 3px 9px;
		border: 1px solid var(--line);
		border-radius: 6px;
		background: var(--card);
		cursor: pointer;
	}
	.go {
		color: var(--running);
		border-color: color-mix(in srgb, var(--running) 45%, var(--line));
	}

	.pane {
		display: flex;
		flex-direction: column;
		min-height: 0;
		min-width: 0;
		border: 1px solid var(--line);
		border-radius: 10px;
		overflow: hidden;
		background: var(--term-bg);
	}
	.pane header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 12px;
		padding: 10px 14px;
		background: var(--panel);
		border-bottom: 1px solid var(--line);
		flex-wrap: wrap;
	}
	.info {
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 3px;
	}
	.info .title {
		display: flex;
		align-items: center;
		gap: 8px;
		font-weight: 600;
		font-size: 14px;
	}
	.info .title .dot {
		margin-top: 0;
	}
	.status {
		font-weight: normal;
		font-size: 12px;
		color: var(--muted);
	}
	.run {
		font: 12px var(--mono);
		color: var(--muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.buttons {
		display: flex;
		gap: 6px;
		flex-wrap: wrap;
	}
	.btn {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-size: 12.5px;
		padding: 4px 10px;
		border: 1px solid var(--line);
		border-radius: 6px;
		background: var(--card);
		cursor: pointer;
		text-decoration: none;
		color: var(--text);
	}
	.btn:hover {
		border-color: var(--accent);
	}
	.btn.link {
		color: var(--info);
		font-family: var(--mono);
		font-size: 12px;
	}
	.btn.danger {
		color: var(--danger);
	}
	.termwrap {
		flex: 1;
		min-height: 0;
	}
	.placeholder {
		margin: auto;
		text-align: center;
		color: var(--muted);
	}
	.hint {
		font-size: 12.5px;
	}

	@media (max-width: 760px) {
		.garage {
			grid-template-columns: 1fr;
			grid-template-rows: auto 1fr;
		}
		.list {
			max-height: 40vh;
		}
	}
</style>
