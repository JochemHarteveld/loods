const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

export function ago(iso: string | undefined, now: number): string {
	const t = iso ? Date.parse(iso) : NaN;
	if (!t || t < 0) return '—';
	const d = Math.max(0, now - t);
	if (d < MIN) return 'now';
	if (d < HOUR) return `${Math.floor(d / MIN)}m`;
	if (d < DAY) return `${Math.floor(d / HOUR)}h`;
	if (d < 14 * DAY) return `${Math.floor(d / DAY)}d`;
	if (d < 365 * DAY) return `${Math.floor(d / (7 * DAY))}w`;
	return `${(d / (365 * DAY)).toFixed(1)}y`;
}

/** hot < 2 days, warm < 2 weeks, cold otherwise. */
export function freshness(iso: string | undefined, now: number): 'hot' | 'warm' | 'cold' {
	const t = iso ? Date.parse(iso) : NaN;
	if (!t) return 'cold';
	const d = now - t;
	return d < 2 * DAY ? 'hot' : d < 14 * DAY ? 'warm' : 'cold';
}

export function bytes(n: number | undefined): string {
	if (!n) return '—';
	if (n < 1 << 30) return `${Math.round(n / (1 << 20))} MB`;
	return `${(n / (1 << 30)).toFixed(1)} GB`;
}
