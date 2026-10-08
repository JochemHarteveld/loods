<script module lang="ts">
	export type Tab = 'plan' | 'garage' | 'git';
	/** Sub-tabs of a project page, in the order the digit keys pick them. */
	export const TABS: Tab[] = ['plan', 'garage', 'git'];
</script>

<script lang="ts">
	import { tick } from 'svelte';
	import Detail from './Detail.svelte';
	import Garage, { type ProcAction } from './Garage.svelte';
	import Planboard from './Planboard.svelte';
	import PlanEditor from './PlanEditor.svelte';
	import {
		isAlive,
		type Branch,
		type BranchList,
		type Claim,
		type ClaudeSummary,
		type GitHubInfo,
		type Plan,
		type PlanPatch,
		type Proc,
		type Project,
		type Stack,
		type Target,
		type Task,
		type TaskState
	} from './lib/api';
	import { claimStale, moveTask, statusClass, statusLabel } from './lib/plan';
	import type { Finding } from './lib/hygiene';
	import { ago } from './lib/time';

	type Props = {
		p: Project;
		plan: Plan | undefined;
		claude: ClaudeSummary | undefined;
		gh: GitHubInfo | undefined;
		findings: Finding[];
		version: number; // bumped after git cleanups: refetch branches
		tab: Tab;
		procs: Proc[]; // this project's processes, plus its group's stacks
		stacks: Stack[]; // stacks of this project's group
		projects: Project[];
		selectedProcId: string | null;
		taskSel: number;
		claims: Record<number, Claim>; // by task number, this project only
		now: number;
		configPath: string;
		ontab: (t: Tab) => void;
		onback: () => void;
		onact: (t: Target) => void;
		onrun: () => void;
		onstop: () => void;
		oncommand: (name: string) => void;
		onsave: (patch: PlanPatch) => Promise<unknown>;
		ontasks: (tasks: Task[], note?: string) => void;
		ontaskselect: (i: number) => void;
		onrelease: (i: number) => void;
		onprocselect: (id: string) => void;
		onprocaction: (a: ProcAction, proc: Proc) => void;
		onstack: (s: Stack) => void;
		onfix: (f: Finding) => void;
		ondelete: (branches: Branch[], data: BranchList) => void;
		onundo: () => void;
	};
	let {
		p,
		plan,
		claude,
		gh,
		findings,
		version,
		tab,
		procs,
		stacks,
		projects,
		selectedProcId,
		taskSel,
		claims,
		now,
		configPath,
		ontab,
		onback,
		onact,
		onrun,
		onstop,
		oncommand,
		onsave,
		ontasks,
		ontaskselect,
		onrelease,
		onprocselect,
		onprocaction,
		onstack,
		onfix,
		ondelete,
		onundo
	}: Props = $props();

	let editor = $state<ReturnType<typeof PlanEditor>>();
	let board = $state<ReturnType<typeof Planboard>>();
	let garage = $state<ReturnType<typeof Garage>>();

	// The caller may have just switched tabs, so wait for the child to exist.
	export const focusNext = async () => (await tick(), editor?.focusNext());
	export const focusNotes = async () => (await tick(), editor?.focusNotes());
	export const addTask = async (state: TaskState = '') => (await tick(), board?.focusAdd(state));
	export const editTask = async (i: number) => (await tick(), board?.startEdit(i));
	export const focusTerminal = async () => (await tick(), garage?.focusTerminal());

	const tasks = $derived(plan?.tasks ?? []);
	const working = $derived(Object.values(claims).filter((c) => !claimStale(c, now)).length);
	const live = $derived(procs.filter(isAlive));
	const serious = $derived(findings.filter((f) => f.level !== 'info').length);
	const labels: Record<Tab, string> = { plan: 'Planboard', garage: 'Garage', git: 'Git' };

	const moveTaskTo = (i: number, state: TaskState) => {
		const moved = moveTask(tasks, i, state);
		if (moved !== tasks) ontasks(moved);
	};
	const addNew = (text: string, state: TaskState) => ontasks([...tasks, { text, state: state || undefined }], `task added: ${text}`);
	const editText = (i: number, text: string) => ontasks(tasks.map((t, j) => (j === i ? { ...t, text } : t)));
	const removeTask = (i: number) => ontasks(tasks.filter((_, j) => j !== i));
</script>

<div class="page">
	<header class="phead">
		<button class="back" onclick={onback} title="Back to the board (esc)">←</button>
		<div class="who">
			<div class="group">{p.group || 'projects'}</div>
			<h1>{p.name}</h1>
		</div>
		<span class="chip {statusClass(plan?.status)}" title="status (m)">
			<span class="dot"></span>{statusLabel(plan?.status)}
			{#if plan?.priority}<b>P{plan.priority}</b>{/if}
		</span>
		<span class="when" title={p.path}>
			{ago(p.last_activity, now) === 'now' ? 'active just now' : `${ago(p.last_activity, now)} ago`}
			{#if p.branch}<span class="mono">· ⎇ {p.branch}</span>{/if}
		</span>

		<nav class="tabs">
			{#each TABS as t, i (t)}
				<button class="tab" class:active={tab === t} onclick={() => ontab(t)}>
					{labels[t]}
					{#if t === 'plan' && working}<span class="count agent" title="{working} task{working === 1 ? '' : 's'} an agent is working on">✻{working}</span>{/if}
					{#if t === 'garage' && live.length}<span class="count">{live.length}</span>{/if}
					{#if t === 'git' && serious}<span class="count warn">{serious}</span>{/if}
					<kbd>{i + 1}</kbd>
				</button>
			{/each}
		</nav>

		<div class="acts">
			{#if live.length}
				<button class="btn danger" onclick={onstop}><kbd>x</kbd> stop</button>
			{:else}
				<button class="btn go" onclick={onrun}><kbd>r</kbd> run</button>
			{/if}
			<button class="btn" onclick={() => onact('code')}><kbd>c</kbd> code</button>
			<button class="btn" onclick={() => onact('terminal')}><kbd>t</kbd> term</button>
			<button class="btn" onclick={() => onact('folder')}><kbd>o</kbd> folder</button>
			{#if p.web_url}<button class="btn" onclick={() => onact('github')}><kbd>g</kbd> remote</button>{/if}
		</div>
	</header>

	{#if tab === 'plan'}
		<div class="split">
			<div class="boardwrap">
				<Planboard
					bind:this={board}
					{tasks}
					selected={taskSel}
					next={plan?.next ?? ''}
					{claims}
					{now}
					onselect={ontaskselect}
					{onrelease}
					onmove={moveTaskTo}
					onadd={addNew}
					onedit={editText}
					ondelete={removeTask}
				/>
			</div>
			<aside class="side">
				<PlanEditor bind:this={editor} rel={p.rel} {plan} {claude} {now} {onsave} />
			</aside>
		</div>
	{:else if tab === 'garage'}
		<div class="garagewrap">
			<Garage
				bind:this={garage}
				{procs}
				{stacks}
				{projects}
				project={p}
				{now}
				{configPath}
				selectedId={selectedProcId}
				onselect={onprocselect}
				onaction={onprocaction}
				{onstack}
				{oncommand}
			/>
		</div>
	{:else}
		<div class="gitwrap">
			<Detail
				inline
				{p}
				{plan}
				{claude}
				{gh}
				{findings}
				{version}
				{now}
				{onfix}
				{ondelete}
				{onundo}
				onclose={onback}
				{onact}
				{onsave}
			/>
		</div>
	{/if}
</div>

<style>
	.page {
		flex: 1;
		min-height: 0;
		display: flex;
		flex-direction: column;
	}
	.phead {
		display: flex;
		align-items: center;
		gap: 14px;
		padding: 10px 24px;
		border-bottom: 1px solid var(--line);
		background: var(--panel);
		flex-wrap: wrap;
	}
	.back {
		border: 1px solid var(--line);
		background: var(--card);
		color: var(--muted);
		border-radius: 8px;
		width: 30px;
		height: 30px;
		font-size: 15px;
		cursor: pointer;
	}
	.back:hover {
		color: var(--text);
		border-color: var(--accent);
	}
	.who {
		min-width: 0;
	}
	.group {
		font-size: 11px;
		text-transform: uppercase;
		letter-spacing: 0.08em;
		color: var(--muted);
	}
	h1 {
		margin: 1px 0 0;
		font-size: 19px;
		letter-spacing: -0.01em;
	}
	.chip {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-size: 12px;
		padding: 3px 9px;
		border-radius: 999px;
		background: color-mix(in srgb, var(--st) 12%, var(--card));
		border: 1px solid color-mix(in srgb, var(--st) 35%, var(--line));
		color: var(--text);
	}
	.chip .dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--st);
	}
	.chip b {
		font: 600 10.5px var(--mono);
		color: var(--muted);
	}
	.when {
		font-size: 12px;
		color: var(--faint);
	}
	.mono {
		font-family: var(--mono);
	}
	.tabs {
		display: flex;
		gap: 4px;
		margin-left: auto;
	}
	.tab {
		display: flex;
		align-items: center;
		gap: 6px;
		border: 1px solid transparent;
		background: none;
		color: var(--muted);
		font: 500 13px var(--sans);
		padding: 5px 11px;
		border-radius: 8px;
		cursor: pointer;
	}
	.tab:hover {
		background: var(--chip);
	}
	.tab.active {
		background: var(--card);
		border-color: var(--line);
		color: var(--text);
		box-shadow: var(--shadow);
	}
	.count {
		font: 600 10.5px var(--mono);
		padding: 0 5px;
		border-radius: 999px;
		background: var(--running);
		color: #fff;
	}
	.count.warn {
		background: var(--danger);
	}
	.count.agent {
		background: var(--accent);
	}
	kbd {
		font: 10.5px var(--mono);
		color: var(--faint);
		border: 1px solid var(--line);
		border-radius: 4px;
		padding: 0 3px;
	}
	.acts {
		display: flex;
		gap: 5px;
	}
	.btn {
		display: flex;
		align-items: center;
		gap: 5px;
		font-size: 12px;
		padding: 4px 9px;
		border: 1px solid var(--line);
		border-radius: 7px;
		background: var(--card);
		color: var(--text);
		cursor: pointer;
	}
	.btn:hover {
		border-color: var(--accent);
	}
	.btn.go {
		color: var(--running);
		border-color: color-mix(in srgb, var(--running) 45%, var(--line));
	}
	.btn.danger {
		color: var(--danger);
		border-color: color-mix(in srgb, var(--danger) 45%, var(--line));
	}
	.split {
		flex: 1;
		min-height: 0;
		display: grid;
		grid-template-columns: minmax(0, 1fr) 360px;
		gap: 14px;
		padding: 14px 24px;
	}
	.boardwrap {
		min-width: 0;
		min-height: 0;
	}
	.side {
		min-height: 0;
		overflow-y: auto;
		padding-left: 14px;
		border-left: 1px solid var(--line);
	}
	.garagewrap,
	.gitwrap {
		flex: 1;
		min-height: 0;
		padding: 14px 24px;
	}
	.gitwrap {
		overflow-y: auto;
	}
	@media (max-width: 1100px) {
		.split {
			grid-template-columns: minmax(0, 1fr);
			overflow-y: auto;
		}
		.side {
			border-left: 0;
			padding-left: 0;
		}
	}
</style>
