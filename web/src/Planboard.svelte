<script lang="ts">
	import { tick } from 'svelte';
	import { isAlive, type Claim, type Proc, type Task, type TaskState } from './lib/api';
	import { claimStale, TASK_STATES, taskStateClass, taskStateLabel } from './lib/plan';
	import { ago } from './lib/time';

	type Props = {
		tasks: Task[];
		selected: number; // index into tasks, -1 for none
		next: string;
		claims: Record<number, Claim>; // by task number: who is working on it
		agents: Record<number, Proc>; // by task number: the agent terminal loods started for it
		now: number;
		onselect: (i: number) => void;
		onmove: (i: number, state: TaskState) => void;
		onadd: (text: string, state: TaskState) => void;
		onedit: (i: number, text: string) => void;
		ondelete: (i: number) => void;
		onrelease: (i: number) => void;
		onassign: (i: number) => void; // hand the task to a Claude agent
		onterminal: (i: number) => void; // show the terminal of the agent on it
	};
	let { tasks, selected, next, claims, agents, now, onselect, onmove, onadd, onedit, ondelete, onrelease, onassign, onterminal }: Props = $props();

	const claimOf = (t: Task) => (t.id ? claims[t.id] : undefined);
	const agentOf = (t: Task) => (t.id ? agents[t.id] : undefined);

	type Cell = { task: Task; i: number };
	const columns = $derived(
		TASK_STATES.map((state) => ({
			state,
			cells: tasks.map((task, i) => ({ task, i })).filter((c) => (c.task.state ?? '') === state)
		}))
	);

	const hints: Record<string, string> = {
		'': 'Everything still to pick up. Press a to add one.',
		doing: 'What you are building right now.',
		done: 'Finished. Drag back if it reopens.'
	};

	let dragging = $state<number | null>(null);
	let over = $state<TaskState | null>(null);
	let editing = $state<number | null>(null);
	let draft = $state('');
	let adding = $state<TaskState | null>(null);
	let newText = $state('');
	let addEl = $state<HTMLInputElement>();
	let editEl = $state<HTMLInputElement>();

	/** a on the planboard: open the add field of a column. */
	export async function focusAdd(state: TaskState = '') {
		adding = state;
		newText = '';
		await tick();
		addEl?.focus();
	}

	/** enter on a card: edit its text in place. */
	export async function startEdit(i: number) {
		if (!tasks[i]) return;
		editing = i;
		draft = tasks[i].text;
		await tick();
		editEl?.focus();
		editEl?.select();
	}

	function commitEdit() {
		const i = editing;
		editing = null;
		if (i === null) return;
		const text = draft.trim();
		if (text && text !== tasks[i]?.text) onedit(i, text);
	}

	function commitAdd(keepOpen: boolean) {
		const text = newText.trim();
		const state = adding ?? '';
		newText = '';
		if (!keepOpen) adding = null;
		if (text) onadd(text, state);
	}

	function cardKey(e: KeyboardEvent, c: Cell) {
		if (e.key !== 'Enter' && e.key !== ' ') return;
		e.preventDefault();
		onselect(c.i);
		if (e.key === 'Enter') startEdit(c.i);
	}

	// Clicking a card opens its menu: what you can do with this task, with
	// handing it to an agent first. Keys do the same things, so the menu is the
	// discoverable half, not the only way.
	let menu = $state<{ i: number; x: number; y: number } | null>(null);
	const MENU_H = 250;

	function openMenu(e: MouseEvent, c: Cell) {
		onselect(c.i);
		if (menu?.i === c.i) return (menu = null); // clicking the same card again closes it
		const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
		menu = {
			i: c.i,
			x: Math.min(r.left, window.innerWidth - 230),
			y: r.bottom + MENU_H > window.innerHeight ? Math.max(8, r.top - MENU_H) : r.bottom + 4
		};
	}

	/** esc on the planboard closes the menu before it leaves the page. */
	export function closeMenu() {
		if (!menu) return false;
		menu = null;
		return true;
	}

	function pick(fn: (i: number) => void) {
		const i = menu?.i;
		menu = null;
		if (i !== undefined) fn(i);
	}
</script>

<svelte:window
	onpointerdown={(e) => {
		const t = e.target as HTMLElement;
		if (menu && !t.closest('.taskmenu') && !t.closest('.task')) menu = null;
	}}
/>

<div class="planboard">
	{#each columns as col (col.state)}
		<div
			class="col {taskStateClass(col.state)}"
			class:over={over === col.state && dragging !== null}
			role="listbox"
			tabindex="-1"
			aria-label={taskStateLabel(col.state)}
			ondragover={(e) => {
				if (dragging === null) return;
				e.preventDefault();
				over = col.state;
			}}
			ondragleave={(e) => {
				if (!(e.currentTarget as HTMLElement).contains(e.relatedTarget as Node)) over = null;
			}}
			ondrop={(e) => {
				e.preventDefault();
				if (dragging !== null) onmove(dragging, col.state);
				dragging = over = null;
			}}
		>
			<h2>
				<span class="dot"></span>{taskStateLabel(col.state)} <span class="n">{col.cells.length}</span>
				<button class="add" title="Add a task here (a)" onclick={() => focusAdd(col.state)}>+</button>
			</h2>

			{#if col.state === '' && next}
				<p class="nextstep" title="the plan's next step (n)">▸ {next}</p>
			{/if}

			{#each col.cells as c (c.task.id ?? c.task.text + c.i)}
				{@const claim = claimOf(c.task)}
				{@const agent = agentOf(c.task)}
				{@const stalled = claim ? claimStale(claim, now) : false}
				<!-- svelte-ignore a11y_click_events_have_key_events (handled by onkeydown) -->
				<div
					class="task"
					class:claimed={claim && !stalled}
					class:selected={c.i === selected}
					class:dragging={dragging === c.i}
					class:done={col.state === 'done'}
					data-task={c.i}
					role="option"
					aria-selected={c.i === selected}
					tabindex="-1"
					draggable={editing !== c.i}
					ondragstart={(e) => {
						dragging = c.i;
						e.dataTransfer?.setData('text/plain', c.task.text);
						onselect(c.i);
					}}
					ondragend={() => (dragging = over = null)}
					onclick={(e) => openMenu(e, c)}
					ondblclick={() => ((menu = null), startEdit(c.i))}
					onkeydown={(e) => cardKey(e, c)}
				>
					{#if editing === c.i}
						<input
							bind:this={editEl}
							bind:value={draft}
							class="edit"
							spellcheck="false"
							onblur={commitEdit}
							onkeydown={(e) => {
								if (e.key === 'Enter') commitEdit();
								else if (e.key === 'Escape') ((editing = null), (e.target as HTMLElement).blur());
							}}
						/>
					{:else}
						<span class="text">
							{#if c.task.id}<span class="num" title="say “do todo #{c.task.id} of this project”">#{c.task.id}</span>{/if}
							{c.task.text}
						</span>
						{#if claim}
							<div class="agent" class:stalled title={claim.branch ? `on ${claim.branch}, since ${ago(claim.claimed_at, now)} ago` : claim.agent}>
								<span class="spark">✻</span>
								{claim.agent}
								{stalled ? 'stalled' : 'working'}
								· {ago(claim.last_heartbeat, now)}
								{#if agent}
									<button class="term" title="Show its terminal in the agent dock" onclick={(e) => (e.stopPropagation(), onterminal(c.i))}>terminal</button>
								{/if}
								<button class="drop" title="This agent is gone: free the task (X)" onclick={(e) => (e.stopPropagation(), onrelease(c.i))}>free</button>
							</div>
						{:else if agent}
							<!-- An agent of ours is up but has not claimed the task yet (or stopped without finishing). -->
							<div class="agent" class:stalled={!isAlive(agent)} title={agent.run}>
								<span class="spark">✻</span>
								{isAlive(agent) ? 'agent starting' : agent.stopped ? 'agent stopped' : 'agent gone'}
								<button class="term" title="Show its terminal in the agent dock" onclick={(e) => (e.stopPropagation(), onterminal(c.i))}>terminal</button>
							</div>
						{/if}
						<div class="row">
							<button
								class="step"
								title="Move left (H)"
								disabled={col.state === ''}
								onclick={(e) => (e.stopPropagation(), (menu = null), onmove(c.i, TASK_STATES[TASK_STATES.indexOf(col.state) - 1]))}>←</button
							>
							<button
								class="step"
								title="Move right (L)"
								disabled={col.state === 'done'}
								onclick={(e) => (e.stopPropagation(), (menu = null), onmove(c.i, TASK_STATES[TASK_STATES.indexOf(col.state) + 1]))}>→</button
							>
							<button class="x" title="Delete task (del)" onclick={(e) => (e.stopPropagation(), (menu = null), ondelete(c.i))}>✕</button>
						</div>
					{/if}
				</div>
			{/each}

			{#if adding === col.state}
				<input
					bind:this={addEl}
					bind:value={newText}
					class="new"
					placeholder="Task, enter to save"
					spellcheck="false"
					onblur={() => commitAdd(false)}
					onkeydown={(e) => {
						if (e.key === 'Enter') commitAdd(true);
						else if (e.key === 'Escape') ((newText = ''), (adding = null), (e.target as HTMLElement).blur());
					}}
				/>
			{:else if !col.cells.length}
				<p class="hint">{hints[col.state]}</p>
			{/if}
		</div>
	{/each}
</div>

{#if menu && tasks[menu.i]}
	{@const t = tasks[menu.i]}
	{@const claim = claimOf(t)}
	{@const agent = agentOf(t)}
	<div class="taskmenu" style:left="{menu.x}px" style:top="{menu.y}px">
		<p class="head">{#if t.id}<span class="num">#{t.id}</span>{/if}{t.text}</p>
		{#if agent && isAlive(agent)}
			<button onclick={() => pick(onterminal)}><span class="ico">✻</span> Show agent terminal</button>
		{:else if t.id}
			<button class="assign" onclick={() => pick(onassign)}>
				<span class="ico">✻</span> Assign to agent <kbd>A</kbd>
			</button>
		{/if}
		{#each TASK_STATES.filter((st) => st !== (t.state ?? '')) as st (st)}
			<button onclick={() => pick((i) => onmove(i, st))}><span class="ico">→</span> Move to {taskStateLabel(st).toLowerCase()}</button>
		{/each}
		<button onclick={() => pick(startEdit)}><span class="ico">✎</span> Rename <kbd>enter</kbd></button>
		{#if claim}
			<button onclick={() => pick(onrelease)}><span class="ico">⦸</span> Free from {claim.agent} <kbd>X</kbd></button>
		{/if}
		<button class="danger" onclick={() => pick(ondelete)}><span class="ico">✕</span> Delete</button>
	</div>
{/if}

<style>
	.planboard {
		display: grid;
		grid-template-columns: repeat(3, minmax(220px, 1fr));
		gap: 10px;
		height: 100%;
		min-height: 0;
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
	.add {
		margin-left: auto;
		border: 0;
		background: none;
		color: var(--faint);
		font-size: 15px;
		line-height: 1;
		cursor: pointer;
		padding: 0 3px;
	}
	.add:hover {
		color: var(--accent);
	}
	.hint {
		margin: 4px 2px;
		font-size: 12px;
		color: var(--faint);
	}
	.nextstep {
		margin: 0 0 2px;
		padding: 7px 9px;
		border-radius: 8px;
		border: 1px dashed var(--line);
		background: var(--accent-soft);
		color: var(--text);
		font-size: 12.5px;
		line-height: 1.35;
	}
	.task {
		padding: 8px 10px;
		border-radius: 8px;
		background: var(--card);
		border: 1px solid var(--line);
		box-shadow: var(--shadow);
		cursor: grab;
		scroll-margin: 40px;
	}
	.task.claimed {
		border-color: color-mix(in srgb, var(--accent) 55%, var(--line));
	}
	.task.selected {
		border-color: var(--accent);
		box-shadow:
			0 0 0 1px var(--accent),
			var(--shadow);
	}
	.task.dragging {
		opacity: 0.4;
	}
	.task.done .text {
		color: var(--muted);
		text-decoration: line-through;
	}
	.num {
		font: 600 11px var(--mono);
		color: var(--faint);
		margin-right: 3px;
	}
	.agent {
		display: flex;
		align-items: center;
		gap: 5px;
		margin-top: 5px;
		font: 11px var(--mono);
		color: var(--accent);
	}
	.agent .spark {
		animation: beat 2s ease-in-out infinite;
	}
	.agent.stalled {
		color: var(--faint);
	}
	.agent.stalled .spark {
		animation: none;
	}
	@keyframes beat {
		50% {
			opacity: 0.35;
		}
	}
	.taskmenu {
		position: fixed;
		z-index: 20;
		width: 230px;
		padding: 5px;
		border-radius: 9px;
		border: 1px solid var(--line);
		background: var(--panel);
		box-shadow: 0 10px 28px rgb(0 0 0 / 0.3);
	}
	.taskmenu .head {
		margin: 2px 6px 5px;
		font-size: 11.5px;
		line-height: 1.35;
		color: var(--muted);
		display: -webkit-box;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		-webkit-box-orient: vertical;
		overflow: hidden;
	}
	.taskmenu button {
		display: flex;
		align-items: center;
		gap: 8px;
		width: 100%;
		border: 0;
		border-radius: 6px;
		background: none;
		color: var(--text);
		font: 12.5px var(--sans);
		text-align: left;
		padding: 6px 7px;
		cursor: pointer;
	}
	.taskmenu button:hover {
		background: var(--chip);
	}
	.taskmenu button.assign:hover {
		background: var(--accent-soft);
	}
	.taskmenu button.danger:hover {
		color: var(--danger);
	}
	.taskmenu .ico {
		width: 13px;
		color: var(--faint);
		font-size: 12px;
		text-align: center;
	}
	.taskmenu button.assign .ico {
		color: var(--accent);
	}
	.taskmenu kbd {
		margin-left: auto;
		font: 10.5px var(--mono);
		color: var(--faint);
		border: 1px solid var(--line);
		border-radius: 4px;
		padding: 0 3px;
	}
	.term {
		border: 0;
		background: none;
		color: inherit;
		font: inherit;
		text-decoration: underline;
		cursor: pointer;
		padding: 0;
		opacity: 0.75;
	}
	.drop {
		margin-left: auto;
		border: 0;
		background: none;
		color: inherit;
		font: inherit;
		text-decoration: underline;
		cursor: pointer;
		opacity: 0;
		padding: 0;
	}
	.task:hover .drop,
	.task.selected .drop {
		opacity: 0.75;
	}
	.text {
		display: block;
		font-size: 13px;
		line-height: 1.4;
		overflow-wrap: anywhere;
	}
	.row {
		display: flex;
		gap: 2px;
		margin-top: 5px;
		opacity: 0;
		transition: opacity 0.1s;
	}
	.task:hover .row,
	.task.selected .row {
		opacity: 1;
	}
	.row button {
		border: 0;
		background: none;
		color: var(--faint);
		font: 11px var(--mono);
		cursor: pointer;
		padding: 1px 4px;
		border-radius: 4px;
	}
	.row button:hover:not(:disabled) {
		background: var(--chip);
		color: var(--text);
	}
	.row button:disabled {
		opacity: 0.25;
		cursor: default;
	}
	.row .x {
		margin-left: auto;
	}
	.row .x:hover {
		color: var(--danger);
	}
	input {
		width: 100%;
		box-sizing: border-box;
		font: 13px var(--sans);
		color: var(--text);
		background: var(--card);
		border: 1px solid var(--accent);
		border-radius: 8px;
		padding: 8px 9px;
	}
	input:focus {
		outline: none;
		box-shadow: 0 0 0 1px var(--accent);
	}
	.edit {
		border-radius: 6px;
		padding: 4px 6px;
	}
</style>
