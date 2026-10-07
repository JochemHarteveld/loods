export type Command = {
	name: string;
	run: string;
	dir?: string;
	source: string;
	keys?: boolean; // single-key input: flutter r / R
	url?: string;
};

export type Warning = {
	level: 'danger' | 'warn' | 'info';
	kind: string;
	text: string;
	fix?: 'cleanup-branches' | 'prune-worktrees' | 'ignore-env';
};

export type Project = {
	path: string;
	rel: string; // stable id
	name: string;
	group: string;
	kind: 'git' | 'proj' | 'dir' | 'zip'; // dir and zip only in the Graveyard
	size_bytes?: number; // Graveyard only
	possible_duplicates?: string[];
	warnings?: Warning[];
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
	commands?: Command[];
	default_command?: string;
};

export type Stack = { name: string; dir: string; run?: string; commands?: string[]; source: string };

export type Status = 'idea' | 'active' | 'paused' | 'shipped' | 'dead';
export type Task = { text: string; done?: boolean };
export type PlanLog = { at: string; by?: string; text: string };
export type Plan = {
	status?: Status;
	priority?: number; // 1 high … 3 low
	next?: string;
	notes?: string;
	tasks?: Task[];
	log?: PlanLog[];
	updated?: string;
};
export type PlanPatch = {
	status?: Status | '';
	priority?: number;
	next?: string;
	notes?: string;
	tasks?: Task[];
	note?: string;
};

export type ClaudeSummary = { last_at: string; last_title: string; live?: boolean; sessions_7d?: number; mins_7d?: number };
export type ClaudeSession = {
	id: string;
	title: string;
	branch?: string;
	start: string;
	end: string;
	prompts: number;
	active_mins: number;
};

export type PR = {
	number: number;
	title: string;
	branch: string;
	draft?: boolean;
	url: string;
	review?: string;
	checks?: 'pass' | 'fail' | 'pending';
};
export type Run = { workflow: string; branch: string; status: string; conclusion: string; url: string; created_at: string };
export type GitHubInfo = { repo: string; branch: string; prs: PR[]; issues: number; ci?: Run; error?: string; fetched_at: string };

export type Snapshot = {
	root: string;
	scanned_at: string;
	scanning: boolean;
	config_path: string;
	config_error?: string;
	projects: Project[];
	stacks: Stack[];
	plans: Record<string, Plan>;
	plans_path: string;
	plans_error?: string;
	claude: Record<string, ClaudeSummary>;
	github: Record<string, GitHubInfo>;
	github_status?: string;
};

export type Proc = {
	id: string;
	project?: string;
	name: string;
	run: string;
	dir: string;
	keys?: boolean;
	status: 'running' | 'stopping' | 'exited' | 'orphan';
	exit_code: number;
	started_at: string;
	ended_at?: string;
	pid?: number;
	rss_bytes?: number;
	ports?: number[];
	urls?: string[];
	warning?: string;
	stopped?: boolean;
};

export type ProcsEvent = { procs: Proc[]; mem_total: number; mem_available: number };

export const isAlive = (p: Proc) => p.status !== 'exited';

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

export type UndoInfo = { batch: string; time: string; branches: string[]; worktrees: number };
export type BranchList = { default: string; branches: Branch[]; undo?: UndoInfo };
export type OpResult = { name: string; ok: boolean; note?: string; error?: string };

export type Batch = { batch: string; time: string; action: 'archive' | 'trash'; items: string[]; restored?: boolean };
export type GraveyardData = { scanned_at: string; archive: string; items: Project[]; batches: Batch[] };
export type GitAction = 'delete-branches' | 'undo' | 'prune-worktrees' | 'ignore-env';

export type Target = 'code' | 'terminal' | 'folder' | 'github';

async function post(url: string, body: unknown) {
	const r = await fetch(url, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json', 'X-Loods': '1' },
		body: JSON.stringify(body)
	});
	if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
	return r;
}

async function get<T>(url: string): Promise<T> {
	const r = await fetch(url);
	if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
	return r.json();
}

export const openIn = (id: string, target: Target) => post('/api/open', { id, target });
export const rescan = () => post('/api/rescan', {});
export const startCommand = (project: string, command?: string) => post('/api/procs/start', { project, command });
export const startStack = (stack: string) => post('/api/procs/start', { stack });
export const procAction = (action: 'stop' | 'restart' | 'remove' | 'input', id: string, data?: string) =>
	post('/api/procs/' + action, { id, data });

export const fetchBranches = (id: string) => get<BranchList>('/api/branches?id=' + encodeURIComponent(id));
export const fetchSessions = (id: string) => get<ClaudeSession[]>('/api/claude?id=' + encodeURIComponent(id));
export const gitOp = async (action: GitAction, project: string, branches?: string[]): Promise<OpResult[]> =>
	(await post('/api/git/' + action, { project, branches })).json();
export const fetchGraveyard = (fresh = false) => get<GraveyardData>('/api/graveyard' + (fresh ? '?fresh=1' : ''));
export const bury = async (action: 'archive' | 'trash', rels: string[]): Promise<OpResult[]> =>
	(await post('/api/graveyard/' + action, { rels })).json();
export const unbury = async (): Promise<OpResult[]> => (await post('/api/graveyard/undo', {})).json();

/** CI state of a run: pass, fail or pending. */
export const runState = (r: Run) =>
	r.status !== 'completed' ? 'pending' : ['success', 'neutral', 'skipped'].includes(r.conclusion) ? 'pass' : 'fail';

export const updatePlan = async (project: string, patch: PlanPatch): Promise<Plan> =>
	(await post('/api/plan', { project, patch })).json();

/** Live snapshots over SSE. EventSource reconnects on its own after errors. */
export function subscribe(
	onSnapshot: (s: Snapshot) => void,
	onProcs: (p: ProcsEvent) => void,
	onConnection: (up: boolean) => void
) {
	const es = new EventSource('/api/events');
	es.addEventListener('snapshot', (e) => onSnapshot(JSON.parse((e as MessageEvent).data)));
	es.addEventListener('procs', (e) => onProcs(JSON.parse((e as MessageEvent).data)));
	es.onopen = () => onConnection(true);
	es.onerror = () => onConnection(false);
	return () => es.close();
}
