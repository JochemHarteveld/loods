export type Project = {
	path: string;
	rel: string; // stable id
	name: string;
	group: string;
	kind: 'git' | 'proj';
	stack?: string[];
	last_activity: string;
	last_file_change: string;
	last_commit?: string;
	branch?: string;
	dirty_files?: number;
	unpushed_commits?: number;
	has_remote?: boolean;
	stashes?: number;
	upstream?: string;
	ahead?: number;
	behind?: number;
	branches?: number;
	worktrees?: number;
	web_url?: string;
	risks?: string[];
};

export type Snapshot = {
	root: string;
	scanned_at: string;
	scanning: boolean;
	projects: Project[];
};

export type Branch = {
	name: string;
	current?: boolean;
	last_commit: string;
	subject: string;
	upstream?: string;
	ahead?: number;
	behind?: number;
	upstream_gone?: boolean;
	base_ahead: number;
	base_behind: number;
	merged?: boolean;
	worktree?: string;
};

export type BranchList = { default: string; branches: Branch[] };

export type Target = 'code' | 'terminal' | 'folder' | 'github';

async function post(url: string, body: unknown) {
	const r = await fetch(url, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json', 'X-Loods': '1' },
		body: JSON.stringify(body)
	});
	if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
}

export const openIn = (id: string, target: Target) => post('/api/open', { id, target });
export const rescan = () => post('/api/rescan', {});

export async function fetchBranches(id: string): Promise<BranchList> {
	const r = await fetch('/api/branches?id=' + encodeURIComponent(id));
	if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
	return r.json();
}

/** Live snapshots over SSE. EventSource reconnects on its own after errors. */
export function subscribe(onSnapshot: (s: Snapshot) => void, onConnection: (up: boolean) => void) {
	const es = new EventSource('/api/events');
	es.addEventListener('snapshot', (e) => onSnapshot(JSON.parse((e as MessageEvent).data)));
	es.onopen = () => onConnection(true);
	es.onerror = () => onConnection(false);
	return () => es.close();
}
