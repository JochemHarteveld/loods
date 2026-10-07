import type { Plan, Status } from './api';

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
	return [t.filter((x) => x.done).length, t.length];
}
