import type { Claim, Plan, Status, Task, TaskState } from './api';

export const STATUSES: Status[] = ['idea', 'active', 'paused', 'shipped', 'dead'];

/** Kanban columns: projects without a status wait in the inbox. */
export const COLUMNS: (Status | '')[] = ['', ...STATUSES];

export const statusLabel = (s: Status | '' | undefined) => (s ? s : 'inbox');

/** CSS class that sets --st to the status colour (see app.css). */
export const statusClass = (s: Status | '' | undefined) => 'st-' + (s || 'none');

/** P1 first, unprioritised last. */
export const prioRank = (p: Plan | undefined) => p?.priority || 9;

export const nextPriority = (n: number | undefined) => ((n ?? 0) + 1) % 4;

export function taskProgress(p: Plan | undefined): [number, number] {
	const t = p?.tasks ?? [];
	return [t.filter(isDone).length, t.length];
}

/** Planboard columns of one project, left to right. */
export const TASK_STATES: TaskState[] = ['', 'doing', 'done'];

export const taskStateLabel = (s: TaskState) => (s === '' ? 'to do' : s === 'doing' ? 'in progress' : 'done');

export const taskStateClass = (s: TaskState) => 'ts-' + (s || 'todo');

export const isDone = (t: Task) => t.state === 'done';

/**
 * A claim with no heartbeat for this long is probably an abandoned session. The
 * server says so too, but working it out here means a claim goes stale on screen
 * without waiting for the next snapshot.
 */
export const CLAIM_STALE_MS = 10 * 60_000;

export const claimStale = (c: Claim, now: number) => now - Date.parse(c.last_heartbeat) > CLAIM_STALE_MS;

/** Move one task to another column: it goes to the end of that column. */
export function moveTask(tasks: Task[], index: number, state: TaskState): Task[] {
	const t = tasks[index];
	if (!t || (t.state ?? '') === state) return tasks;
	const rest = tasks.filter((_, i) => i !== index);
	return [...rest, { ...t, state: state || undefined }];
}
