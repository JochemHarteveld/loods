<script lang="ts">
	import { fetchSessions, type ClaudeSession, type ClaudeSummary, type Plan, type PlanPatch, type Status } from './lib/api';
	import { STATUSES, statusClass, statusLabel, taskProgress } from './lib/plan';
	import { ago } from './lib/time';

	type Props = {
		rel: string;
		plan: Plan | undefined;
		claude: ClaudeSummary | undefined;
		now: number;
		onsave: (patch: PlanPatch) => Promise<unknown>;
	};
	let { rel, plan, claude, now, onsave }: Props = $props();

	let nextEl = $state<HTMLInputElement>();
	let notesEl = $state<HTMLTextAreaElement>();
	export const focusNext = () => (nextEl?.focus(), nextEl?.select());
	export const focusNotes = () => notesEl?.focus();

	// Drafts follow the saved plan unless the field is being edited.
	let next = $state('');
	let notes = $state('');
	$effect(() => {
		const v = plan?.next ?? '';
		if (document.activeElement !== nextEl) next = v;
	});
	$effect(() => {
		const v = plan?.notes ?? '';
		if (document.activeElement !== notesEl) notes = v;
	});
	const progress = $derived(taskProgress(plan));
	const log = $derived([...(plan?.log ?? [])].reverse().slice(0, 8));

	let sessions = $state<ClaudeSession[]>([]);
	$effect(() => {
		const id = rel;
		void claude?.last_at;
		let stale = false;
		fetchSessions(id)
			.then((s) => !stale && (sessions = s))
			.catch(() => !stale && (sessions = []));
		return () => (stale = true);
	});

	function saveNext() {
		if (next.trim() !== (plan?.next ?? '')) onsave({ next: next.trim() });
	}
	function saveNotes() {
		if (notes !== (plan?.notes ?? '')) onsave({ notes });
	}
	function onFieldKey(e: KeyboardEvent, revert: () => void, save: () => void) {
		if (e.key === 'Escape') {
			revert();
			(e.target as HTMLElement).blur();
		} else if (e.key === 'Enter' && (e.target instanceof HTMLInputElement || e.ctrlKey)) {
			save();
			(e.target as HTMLElement).blur();
		}
	}

	const mins = (m: number) => (m < 60 ? `${m}m` : `${Math.floor(m / 60)}h${m % 60 ? ` ${m % 60}m` : ''}`);
	const when = (iso: string) => (ago(iso, now) === 'now' ? 'just now' : `${ago(iso, now)} ago`);
</script>

<section class="plan">
	<div class="row">
		<div class="pills" role="group" aria-label="Status">
			{#each STATUSES as s, i (s)}
				<button
					class="pill {statusClass(s)}"
					class:on={plan?.status === s}
					title="{s} ({i + 1} after m)"
					onclick={() => onsave({ status: plan?.status === s ? '' : (s as Status) })}>{s}</button
				>
			{/each}
		</div>
		<div class="pills" role="group" aria-label="Priority">
			{#each [1, 2, 3] as n (n)}
				<button class="pill prio" class:on={plan?.priority === n} title="priority (p cycles)" onclick={() => onsave({ priority: plan?.priority === n ? 0 : n })}
					>P{n}</button
				>
			{/each}
		</div>
	</div>

	<label class="field">
		<span>Next step <kbd>n</kbd></span>
		<input
			bind:this={nextEl}
			bind:value={next}
			placeholder="What's the very next thing to do here?"
			maxlength="300"
			spellcheck="false"
			onblur={saveNext}
			onkeydown={(e) => onFieldKey(e, () => (next = plan?.next ?? ''), saveNext)}
		/>
	</label>

	{#if progress[1]}
		<p class="progress" class:ok={progress[0] === progress[1]}>☑ {progress[0]} of {progress[1]} tasks done — the planboard has them</p>
	{/if}

	<label class="field">
		<span>Notes <kbd>N</kbd></span>
		<textarea
			bind:this={notesEl}
			bind:value={notes}
			rows={Math.min(12, Math.max(3, notes.split('\n').length + 1))}
			placeholder="Decisions, links, open questions… (ctrl+enter saves)"
			onblur={saveNotes}
			onkeydown={(e) => onFieldKey(e, () => (notes = plan?.notes ?? ''), saveNotes)}
		></textarea>
	</label>

	{#if log.length}
		<div class="field">
			<span>Log</span>
			<ul class="log">
				{#each log as e (e.at + e.text)}
					<li>
						<time title={e.at}>{ago(e.at, now)}</time>
						{#if e.by === 'claude'}<span class="by">claude</span>{/if}
						<span class="text">{e.text}</span>
					</li>
				{/each}
			</ul>
		</div>
	{/if}

	{#if sessions.length}
		<div class="field">
			<span>
				Claude sessions
				{#if claude?.sessions_7d}<em>{claude.sessions_7d} this week · {mins(claude.mins_7d ?? 0)} active</em>{/if}
			</span>
			<ul class="sessions">
				{#each sessions.slice(0, 6) as s (s.id)}
					<li>
						<div class="stitle" title={s.title}>{s.title}</div>
						<div class="smeta">
							{when(s.end)} · {s.prompts} prompt{s.prompts === 1 ? '' : 's'} · {mins(s.active_mins)}
							{#if s.branch}· <span class="mono">⎇ {s.branch}</span>{/if}
						</div>
					</li>
				{/each}
			</ul>
		</div>
	{/if}

	{#if plan?.updated}<p class="foot">plan updated {when(plan.updated)} · status {statusLabel(plan.status)}</p>{/if}
</section>

<style>
	.plan {
		display: flex;
		flex-direction: column;
		gap: 14px;
		margin: 16px 0 6px;
		padding: 14px;
		border: 1px solid var(--line);
		border-radius: 10px;
		background: var(--card);
	}
	.row {
		display: flex;
		flex-wrap: wrap;
		gap: 8px 14px;
		justify-content: space-between;
	}
	.pills {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
	}
	.pill {
		font-size: 12px;
		padding: 2px 9px;
		border-radius: 10px;
		border: 1px solid var(--line);
		background: none;
		color: var(--muted);
		cursor: pointer;
	}
	.pill:hover {
		border-color: var(--st, var(--accent));
		color: var(--st, var(--accent));
	}
	.pill.on {
		background: color-mix(in srgb, var(--st, var(--accent)) 16%, transparent);
		border-color: var(--st, var(--accent));
		color: var(--st, var(--accent));
		font-weight: 600;
	}
	.field {
		display: flex;
		flex-direction: column;
		gap: 6px;
		min-width: 0;
	}
	.field > span {
		display: flex;
		align-items: center;
		gap: 6px;
		font-size: 11.5px;
		text-transform: uppercase;
		letter-spacing: 0.06em;
		color: var(--muted);
		font-weight: 600;
	}
	.field > span kbd {
		font-size: 10px;
		text-transform: none;
		opacity: 0.7;
	}
	.field > span em {
		font-style: normal;
		text-transform: none;
		letter-spacing: 0;
		font-weight: normal;
		color: var(--faint);
	}
	input:not([type='checkbox']),
	textarea {
		width: 100%;
		padding: 6px 9px;
		border: 1px solid var(--line);
		border-radius: 7px;
		background: var(--panel);
		color: var(--text);
		font: inherit;
		font-size: 13.5px;
		outline: none;
		resize: vertical;
	}
	input:focus,
	textarea:focus {
		border-color: var(--accent);
	}
	textarea {
		font-size: 13px;
		line-height: 1.45;
	}
	.link {
		margin-left: auto;
		border: none;
		background: none;
		color: var(--info);
		font-size: 11.5px;
		text-transform: none;
		letter-spacing: 0;
		cursor: pointer;
		padding: 0;
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	.progress {
		margin: 0;
		font: 11.5px var(--mono);
		color: var(--muted);
	}
	.progress.ok {
		color: var(--hot);
	}
	.log li {
		display: flex;
		gap: 8px;
		font-size: 12.5px;
		align-items: baseline;
	}
	.log time {
		flex: none;
		width: 30px;
		font: 11.5px var(--mono);
		color: var(--faint);
	}
	.by {
		flex: none;
		font-size: 10.5px;
		padding: 0 5px;
		border-radius: 4px;
		background: var(--accent-soft);
		color: var(--accent);
	}
	.text {
		color: var(--muted);
		overflow-wrap: anywhere;
	}
	.sessions {
		gap: 6px;
	}
	.stitle {
		font-size: 13px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.smeta {
		font-size: 11.5px;
		color: var(--faint);
	}
	.mono {
		font-family: var(--mono);
	}
	.foot {
		margin: 0;
		font-size: 11.5px;
		color: var(--faint);
	}
</style>
