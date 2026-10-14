import { jobOver, runOver, type AgentRun, type Job } from './api';

/**
 * How far an agent is and how much longer it needs, from the checklist its
 * orchestrator handed it. The same arithmetic lives in internal/run/steps.go,
 * which is what `loods run status` prints: the board and the terminal have to
 * agree about a percentage, and neither side is a good place to put the other's
 * copy — a run in the state file carries ticks and timestamps, never a figure
 * that would be stale the second it was written.
 *
 * The forecast is naive on purpose: the steps a job has finished set the pace
 * for the ones it has left. That only holds for steps of roughly equal size,
 * which is what every prompt asking for steps asks for.
 */

const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

const at = (iso?: string) => (iso ? Date.parse(iso) : NaN);

export const stepsDone = (j: Job) => (j.steps ?? []).filter((s) => s.done).length;

/** How far along a job is, 0..1. Without a checklist: nothing until it is done. */
export function jobProgress(j: Job): number {
	const steps = j.steps ?? [];
	if (!steps.length) return j.status === 'done' ? 1 : 0;
	return stepsDone(j) / steps.length;
}

/** The average time one ticked step took, or null before the first tick. */
function pace(j: Job): number | null {
	const start = at(j.started_at);
	if (!start) return null;
	let last = start;
	let n = 0;
	for (const s of j.steps ?? []) {
		const done = at(s.done_at);
		if (!s.done || !done || done < last) continue;
		last = done;
		n++;
	}
	return n ? (last - start) / n : null;
}

/** When the job last said anything: its newest tick, or its start. */
function lastTick(j: Job): number {
	let t = at(j.started_at) || 0;
	for (const s of j.steps ?? []) {
		const done = at(s.done_at);
		if (s.done && done > t) t = done;
	}
	return t;
}

/**
 * How much longer a running job is expected to need, in ms, or null when there
 * is nothing to go on: no checklist, nothing ticked yet, or it is over.
 */
export function jobETA(j: Job, now: number): number | null {
	const steps = j.steps ?? [];
	if (j.status !== 'running' || !steps.length) return null;
	const left = steps.length - stepsDone(j);
	if (!left) return 0; // every step ticked; it is writing its report
	const per = pace(j);
	if (per === null) return null;
	const spent = Math.max(0, now - lastTick(j));
	return Math.max(0, per * left - spent);
}

/** The run as one fraction: every job weighs the same. */
export function runProgress(r: AgentRun): number {
	const jobs = r.jobs ?? [];
	if (!jobs.length) return 0;
	return jobs.reduce((sum, j) => sum + jobProgress(j), 0) / jobs.length;
}

/**
 * How much longer the whole run needs: the longest forecast among the jobs
 * running now, plus the jobs still waiting for a session in waves of
 * max_parallel, each wave as long as a job of this run has taken on average.
 */
export function runETA(r: AgentRun, now: number): number | null {
	if (runOver(r)) return null;
	const jobs = r.jobs ?? [];
	let longest: number | null = null;
	for (const j of jobs) {
		const d = jobETA(j, now);
		if (d !== null) longest = Math.max(longest ?? 0, d);
	}
	const pending = jobs.filter((j) => j.status === 'pending').length;
	if (!pending) return longest;
	const typical = typicalJob(r, now);
	if (typical === null) return longest;
	const waves = Math.ceil(pending / Math.max(1, r.max_parallel));
	return (longest ?? 0) + typical * waves;
}

/** How long one job of this run takes: the finished ones, or a running pace. */
function typicalJob(r: AgentRun, now: number): number | null {
	const spans = (r.jobs ?? [])
		.filter((j) => j.status === 'done' && at(j.started_at) && at(j.ended_at))
		.map((j) => at(j.ended_at) - at(j.started_at));
	if (spans.length) return spans.reduce((a, b) => a + b, 0) / spans.length;
	for (const j of (r.jobs ?? []).filter((j) => j.status === 'running')) {
		const per = pace(j);
		if (per !== null) return per * Math.max(1, (j.steps ?? []).length);
		const start = at(j.started_at);
		if (start && now > start) return now - start;
	}
	return null;
}

/** "<1m", "7m", "1h20m", "2d": a forecast, so never more precise than that. */
export function left(ms: number): string {
	if (ms < MIN) return '<1m';
	if (ms < HOUR) return `${Math.floor(ms / MIN)}m`;
	if (ms < DAY) {
		const h = Math.floor(ms / HOUR);
		const m = Math.floor((ms % HOUR) / MIN);
		return m ? `${h}h${String(m).padStart(2, '0')}m` : `${h}h`;
	}
	return `${Math.floor(ms / DAY)}d`;
}

export const pct = (fraction: number) => Math.round(fraction * 100);

/**
 * What a job's row says about its progress: the percentage, and the time left
 * while it is running. A job without a checklist says nothing — it never told
 * anybody more than its status.
 */
export function jobLine(j: Job, now: number): string {
	if (!(j.steps ?? []).length) return '';
	const d = jobETA(j, now);
	const bar = `${pct(jobProgress(j))}%`;
	if (d === null) return bar;
	if (jobOver(j)) return bar;
	return `${bar} · ~${left(d)} left`;
}
