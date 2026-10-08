<script lang="ts">
	import { onMount, tick } from 'svelte';
	import AgentDock from './AgentDock.svelte';
	import Agents from './Agents.svelte';
	import Card from './Card.svelte';
	import Detail from './Detail.svelte';
	import { type ProcAction } from './Garage.svelte';
	import ProjectPage, { TABS, type Tab } from './ProjectPage.svelte';
	import Hygiene, { type Row } from './Hygiene.svelte';
	import Graveyard from './Graveyard.svelte';
	import {
		bury,
		fetchBranches,
		fetchGraveyard,
		gitOp,
		unbury,
		isAgent,
		isAlive,
		runOver,
		type AgentRun,
		openIn,
		procAction,
		releaseClaim,
		rescan,
		startAgent,
		startCommand,
		startProject,
		cancelRun,
		startStack,
		subscribe,
		updatePlan,
		type Branch,
		type BranchList,
		type Claim,
		type ClaudeSummary,
		type GitHubInfo,
		type GraveyardData,
		type OpResult,
		type Plan,
		type PlanPatch,
		type Proc,
		type Project,
		type Stack,
		type Status,
		type Target,
		type Task,
		type TaskState
	} from './lib/api';
	import { claimStale, COLUMNS, moveTask, nextPriority, prioRank, statusLabel, TASK_STATES } from './lib/plan';
	import { findings, type Finding } from './lib/hygiene';
	import { ago, bytes } from './lib/time';

	type Group = { name: string; items: Project[]; latest: number };
	// Top-level views. Everything about one project — its planboard, its garage
	// and its git state — lives on that project's own page.
	type View = 'board' | 'hygiene' | 'graveyard' | 'agents' | 'project';
	const VIEWS: View[] = ['board', 'hygiene', 'graveyard', 'agents'];
	type Route = { view: View; rel?: string; tab?: Tab };

	// Routes: '', '#hygiene', '#graveyard', '#p/<rel>' and '#p/<rel>!<tab>'.
	function parseHash(hash: string): Route {
		const s = hash.replace(/^#/, '');
		if (s.startsWith('p/')) {
			const [rel, tab] = s.slice(2).split('!');
			return { view: 'project', rel: decodeURIComponent(rel), tab: (TABS as string[]).includes(tab) ? (tab as Tab) : 'plan' };
		}
		return { view: (VIEWS as string[]).includes(s) ? (s as View) : 'board' };
	}

	function hashOf(r: Route): string {
		if (r.view === 'project' && r.rel) return `#p/${encodeURIComponent(r.rel)}${r.tab && r.tab !== 'plan' ? '!' + r.tab : ''}`;
		return r.view === 'board' ? '' : '#' + r.view;
	}
	type ConfirmLine = { text: string; sub?: string; danger?: boolean };
	type Confirm = {
		title: string;
		lines: ConfirmLine[];
		note?: string;
		label: string;
		danger?: boolean;
		run: () => Promise<OpResult[] | void>;
	};

	let projects = $state<Project[]>([]);
	let stacks = $state<Stack[]>([]);
	let procs = $state<Proc[]>([]);
	let plans = $state<Record<string, Plan>>({});
	let claude = $state<Record<string, ClaudeSummary>>({});
	let claims = $state<Record<string, Claim>>({});
	let runs = $state<Record<string, AgentRun>>({});
	let github = $state<Record<string, GitHubInfo>>({});
	let githubStatus = $state('');
	let hyIndex = $state(0);
	let grave = $state<GraveyardData | null>(null);
	let graveLoading = $state(false);
	let graveSort = $state<'age' | 'size' | 'name'>('age');
	let graveDupes = $state(false);
	let graveSel = $state<string | null>(null);
	let marked = $state(new Set<string>());
	let confirm = $state<Confirm | null>(null);
	let confirmBusy = $state(false);
	let gitVersion = $state(0);
	let plansPath = $state('');
	let plansError = $state('');
	let mem = $state({ total: 0, available: 0 });
	let root = $state('');
	let configPath = $state('');
	let configError = $state('');
	let scannedAt = $state('');
	let scanning = $state(false);
	let connected = $state(false);
	let loaded = $state(false);
	let now = $state(Date.now());
	const start = parseHash(location.hash);
	let view = $state<View>(start.view);
	let projectRel = $state<string | null>(start.rel ?? null);
	let projectTab = $state<Tab>(start.tab ?? 'plan');
	let taskSel = $state(0);
	let query = $state('');
	let sortBy = $state<'activity' | 'priority' | 'name'>('activity');
	let selectedId = $state<string | null>(null);
	let selectedProcId = $state<string | null>(null);
	// The agent dock: terminals of the Claude sessions loods started, at the
	// bottom of the window whatever view you are in, because an agent keeps
	// working while you walk the board.
	let agentId = $state<string | null>(null);
	let dockOpen = $state(false);
	let dockHeight = $state(300);
	// The row picked in the Agents view, as "<run>:<job>" ("<run>:" is its
	// orchestrator), so the selection survives a snapshot.
	let selectedJob = $state<string | null>(null);
	// The + dialog: a name, what to build, and optionally a group folder.
	let newProj = $state<{ name: string; goal: string; group: string } | null>(null);
	let newBusy = $state(false);
	let detailOpen = $state(false);
	let helpOpen = $state(false);
	let picker = $state<{ project: Project; index: number } | null>(null);
	let statusPicker = $state<Project | null>(null);
	let toast = $state<{ text: string; error: boolean } | null>(null);
	let filterEl = $state<HTMLInputElement>();
	let page = $state<ReturnType<typeof ProjectPage>>();
	let toastTimer: ReturnType<typeof setTimeout> | undefined;

	const time = (p: Project) => Date.parse(p.last_activity) || 0;

	const filtered = $derived.by(() => {
		const q = query.trim().toLowerCase();
		if (!q) return projects;
		return projects.filter((p) => {
			const plan = plans[p.rel];
			return [p.rel, p.branch ?? '', ...(p.stack ?? []), plan?.status ?? 'inbox', plan?.next ?? ''].join(' ').toLowerCase().includes(q);
		});
	});
	const byPriority = (a: Project, b: Project) => prioRank(plans[a.rel]) - prioRank(plans[b.rel]) || time(b) - time(a);

	const groups = $derived.by(() => {
		const byName = new Map<string, Group>();
		for (const p of filtered) {
			let g = byName.get(p.group);
			if (!g) byName.set(p.group, (g = { name: p.group, items: [], latest: 0 }));
			g.items.push(p);
			g.latest = Math.max(g.latest, time(p));
		}
		const list = [...byName.values()];
		if (sortBy === 'activity') {
			list.forEach((g) => g.items.sort((a, b) => time(b) - time(a)));
			list.sort((a, b) => b.latest - a.latest);
		} else if (sortBy === 'priority') {
			list.forEach((g) => g.items.sort(byPriority));
			const best = (g: Group) => prioRank(plans[g.items[0]?.rel]);
			list.sort((a, b) => best(a) - best(b) || b.latest - a.latest);
		} else {
			list.forEach((g) => g.items.sort((a, b) => a.name.localeCompare(b.name)));
			list.sort((a, b) => (a.name || '~').localeCompare(b.name || '~'));
		}
		return list;
	});
	const flat = $derived(groups.flatMap((g) => g.items));
	const selected = $derived(filtered.find((p) => p.rel === selectedId) ?? null);
	// The project page works on the project itself, filter or no filter.
	const project = $derived(projects.find((p) => p.rel === projectRel) ?? null);
	const findingsOf = (p: Project) => findings(p, github[p.rel]);
	const levelRank = { danger: 0, warn: 1, info: 2 };
	// Hygiene rows: projects with the worst findings first.
	const hygieneRows = $derived.by(() => {
		const per = filtered.map((p) => ({ p, fs: findingsOf(p) })).filter((x) => x.fs.length);
		per.sort((a, b) => levelRank[a.fs[0].level] - levelRank[b.fs[0].level] || a.p.rel.localeCompare(b.p.rel));
		return per.flatMap(({ p, fs }) => fs.map((f): Row => ({ p, f })));
	});
	const attention = $derived(projects.filter((p) => findingsOf(p).some((f) => f.level !== 'info')).length);
	const graveItems = $derived.by(() => {
		const q = query.trim().toLowerCase();
		let list = (grave?.items ?? []).filter((it) => (!q || it.rel.toLowerCase().includes(q)) && (!graveDupes || it.possible_duplicates?.length));
		const age = (it: Project) => Date.parse(it.last_activity) || 0;
		if (graveSort === 'age') list = list.sort((a, b) => age(a) - age(b));
		else if (graveSort === 'size') list = list.sort((a, b) => (b.size_bytes ?? 0) - (a.size_bytes ?? 0));
		else list = list.sort((a, b) => a.rel.localeCompare(b.rel));
		return list;
	});
	// Visual order of the current view, for keeping a valid selection.
	const order = $derived(view === 'hygiene' ? hygieneRows.map((r) => r.p) : flat);
	const live = $derived(procs.filter(isAlive));
	const liveRSS = $derived(live.reduce((sum, p) => sum + (p.rss_bytes ?? 0), 0));
	const memUsed = $derived(mem.total ? 1 - mem.available / mem.total : 0);
	const procsOf = (rel: string) => live.filter((p) => p.project === rel && !isAgent(p));
	// Agents are processes too, but they belong in the dock, not in a garage.
	const agentProcs = $derived(procs.filter(isAgent));
	// Runs, newest first: the Agents view walks them in this order.
	const runList = $derived(
		Object.values(runs).sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at) || a.id.localeCompare(b.id))
	);
	const liveRuns = $derived(runList.filter((r) => !runOver(r)));
	/** Live sessions per project, for the card badge: agents plus a run's jobs. */
	const sessionsPer = $derived.by(() => {
		const per: Record<string, number> = {};
		for (const p of agentProcs) if (isAlive(p) && p.project) per[p.project] = (per[p.project] ?? 0) + 1;
		return per;
	});
	const selectedAgent = $derived(agentProcs.find((p) => p.id === agentId) ?? agentProcs[agentProcs.length - 1] ?? null);
	// space opens the details drawer for a quick peek without leaving the list.
	const drawerView = $derived(view === 'board' || view === 'hygiene');

	// The project page's garage shows this project's processes plus the stacks of
	// its group, which start several projects of that group at once.
	const groupStacks = $derived(project ? stacks.filter((s) => s.name === project.group) : []);
	const projectProcs = $derived.by(() => {
		if (!project) return [];
		const stackIds = new Set(groupStacks.map((s) => 'stack:' + s.name));
		return procs.filter((pr) => (pr.project === project.rel && !isAgent(pr)) || stackIds.has(pr.id));
	});
	// Garage keys act on a process of the open project, never on someone else's.
	const selectedProc = $derived(projectProcs.find((p) => p.id === selectedProcId) ?? null);

	const tasks = $derived(project ? (plans[project.rel]?.tasks ?? []) : []);
	// Claims of the open project, by task number, for the planboard.
	const projectClaims = $derived.by(() => {
		const out: Record<number, Claim> = {};
		if (!project) return out;
		for (const c of Object.values(claims)) if (c.project === project.rel) out[c.task] = c;
		return out;
	});
	// Agent terminals of the open project, by task number, for the planboard.
	const projectAgents = $derived.by(() => {
		const out: Record<number, Proc> = {};
		if (!project) return out;
		for (const pr of agentProcs) if (pr.project === project.rel && pr.task) out[pr.task] = pr;
		return out;
	});
	// How many tasks an agent is on, per project, for the board cards.
	const workingPer = $derived.by(() => {
		const out: Record<string, number> = {};
		for (const c of Object.values(claims)) if (!claimStale(c, now)) out[c.project] = (out[c.project] ?? 0) + 1;
		return out;
	});
	const working = $derived(Object.values(workingPer).reduce((n, v) => n + v, 0));
	// Planboard columns, as cells that remember each task's index in the plan.
	const taskCols = $derived(TASK_STATES.map((st) => tasks.map((t, i) => ({ t, i })).filter((c) => (c.t.state ?? '') === st)));

	// The view (and which project page) lives in the URL hash, so a reload or a
	// bookmark comes back to the same place.
	$effect(() => {
		const h = hashOf({ view, rel: projectRel ?? undefined, tab: projectTab });
		history.replaceState(null, '', h || location.pathname);
	});

	// A project that disappears (renamed, archived) sends you back to the board.
	$effect(() => {
		if (view === 'project' && loaded && !project) {
			view = 'board';
			projectRel = null;
		}
	});

	// Keep the planboard selection on a task that exists.
	$effect(() => {
		if (taskSel >= tasks.length) taskSel = Math.max(0, tasks.length - 1);
	});

	// The project garage selects one of this project's own processes.
	$effect(() => {
		if (view === 'project' && projectProcs.length && !projectProcs.some((pr) => pr.id === selectedProcId)) {
			selectedProcId = projectProcs[0].id;
		}
	});

	// Keep the dock on an agent that still exists, and close it when the last one
	// is removed, so an empty dock never sits there taking up room.
	$effect(() => {
		if (agentProcs.length && !agentProcs.some((p) => p.id === agentId)) agentId = agentProcs[agentProcs.length - 1].id;
		if (!agentProcs.length && dockOpen) dockOpen = false;
	});

	// Keep valid selections when filter or data change.
	$effect(() => {
		if (!selected && order.length) selectedId = order[0].rel;
	});
	$effect(() => {
		if (hyIndex >= hygieneRows.length) hyIndex = Math.max(0, hygieneRows.length - 1);
	});
	// The drawer in the Hygiene view shows the project of the selected row.
	$effect(() => {
		if (view === 'hygiene' && hygieneRows[hyIndex]) selectedId = hygieneRows[hyIndex].p.rel;
	});
	$effect(() => {
		if (view === 'graveyard' && !grave && !graveLoading) loadGrave(false);
	});
	$effect(() => {
		if (graveItems.length && !graveItems.some((it) => it.rel === graveSel)) graveSel = graveItems[0].rel;
	});

	onMount(() => {
		const stop = subscribe(
			(s) => {
				projects = s.projects;
				stacks = s.stacks;
				plans = s.plans ?? {};
				claude = s.claude ?? {};
				claims = s.claims ?? {};
				runs = s.runs ?? {};
				github = s.github ?? {};
				githubStatus = s.github_status ?? '';
				plansPath = s.plans_path;
				plansError = s.plans_error ?? '';
				root = s.root;
				configPath = s.config_path;
				configError = s.config_error ?? '';
				scannedAt = s.scanned_at;
				scanning = s.scanning;
				loaded = true;
			},
			(e) => {
				procs = e.procs;
				mem = { total: e.mem_total, available: e.mem_available };
			},
			(up) => (connected = up)
		);
		const clock = setInterval(() => (now = Date.now()), 15_000);
		const onhash = () => {
			const r = parseHash(location.hash);
			view = r.view;
			projectRel = r.rel ?? null;
			projectTab = r.tab ?? 'plan';
		};
		addEventListener('hashchange', onhash);
		return () => {
			stop();
			clearInterval(clock);
			removeEventListener('hashchange', onhash);
		};
	});

	function flash(text: string, error = false) {
		toast = { text, error };
		clearTimeout(toastTimer);
		toastTimer = setTimeout(() => (toast = null), error ? 5000 : 1800);
	}

	async function attempt(fn: () => Promise<unknown>, ok?: string) {
		try {
			await fn();
			if (ok) flash(ok);
		} catch (e) {
			flash((e as Error).message, true);
		}
	}

	// --- confirmations, hygiene fixes, graveyard ---

	function summarize(results: OpResult[] | void, verb: string) {
		if (!results) return flash(verb);
		const ok = results.filter((r) => r.ok);
		const bad = results.filter((r) => !r.ok);
		if (!bad.length) return flash(`${verb}: ${ok.length}`);
		flash(`${verb}: ${ok.length}. Not done: ${bad.map((r) => `${r.name} (${r.error})`).join('; ')}`, true);
	}

	async function runConfirm() {
		if (!confirm || confirmBusy) return;
		const c = confirm;
		confirmBusy = true;
		try {
			summarize(await c.run(), c.label);
			confirm = null;
		} catch (e) {
			flash((e as Error).message, true);
		} finally {
			confirmBusy = false;
		}
	}

	function deleteBranches(p: Project, branches: Branch[], data: BranchList) {
		if (!branches.length) return flash('No branches to delete');
		const prs = github[p.rel]?.prs ?? [];
		confirm = {
			title: `Delete ${branches.length} branch${branches.length === 1 ? '' : 'es'} in ${p.name}`,
			lines: branches.map((b) => {
				const pr = prs.find((x) => x.branch === b.name);
				const sub = [
					b.merged ? `merged into ${data.default}` : `${b.base_ahead} commit${b.base_ahead === 1 ? '' : 's'} not in ${data.default}`,
					b.upstream_gone ? 'remote branch deleted' : !b.upstream ? 'never pushed' : '',
					b.worktree ? `its worktree goes too (if clean): ${b.worktree}` : '',
					pr ? `open PR #${pr.number} (stays on GitHub)` : ''
				];
				return { text: b.name, sub: sub.filter(Boolean).join(' · '), danger: !b.merged };
			}),
			note: 'Undoable: loods logs the commit of every branch it deletes. Worktrees with uncommitted changes are kept.',
			label: 'Deleted',
			danger: branches.some((b) => !b.merged),
			run: async () => {
				const r = await gitOp('delete-branches', p.rel, branches.map((b) => b.name));
				gitVersion++;
				return r;
			}
		};
	}

	async function undoGit(p: Project) {
		try {
			summarize(await gitOp('undo', p.rel), 'Restored');
			gitVersion++;
		} catch (e) {
			flash((e as Error).message, true);
		}
	}

	async function fixFinding(p: Project, f: Finding) {
		switch (f.fix) {
			case 'cleanup-branches': {
				const data = await fetchBranches(p.rel).catch((e) => (flash(e.message, true), null));
				if (!data) return;
				const def = data.default.replace(/^origin\//, '');
				const ok = (b: Branch) => !b.current && b.name !== def;
				const list = data.branches.filter((b) => ok(b) && (f.kind === 'gone-branches' ? b.upstream_gone && !b.merged : b.merged));
				return deleteBranches(p, list, data);
			}
			case 'prune-worktrees':
				confirm = {
					title: `Prune worktrees in ${p.name}`,
					lines: [{ text: f.text }],
					note: 'Runs git worktree prune: it only forgets worktrees whose folder no longer exists.',
					label: 'Pruned',
					run: async () => {
						const r = await gitOp('prune-worktrees', p.rel);
						gitVersion++;
						return r;
					}
				};
				return;
			case 'ignore-env':
				confirm = {
					title: `Ignore secret files in ${p.name}`,
					lines: [{ text: f.text }],
					note: 'Appends their exact paths to .gitignore. Commit that change yourself.',
					label: 'Ignored',
					run: () => gitOp('ignore-env', p.rel)
				};
				return;
		}
		if (f.url) window.open(f.url, '_blank', 'noopener');
	}

	async function loadGrave(fresh: boolean) {
		graveLoading = true;
		try {
			grave = await fetchGraveyard(fresh);
		} catch (e) {
			flash((e as Error).message, true);
		} finally {
			graveLoading = false;
		}
	}

	function toggleMark(rel: string) {
		const next = new Set(marked);
		if (!next.delete(rel)) next.add(rel);
		marked = next;
	}

	function buryMarked(action: 'archive' | 'trash') {
		const rels = marked.size ? [...marked] : graveSel ? [graveSel] : [];
		const items = rels.map((r) => grave?.items.find((it) => it.rel === r)).filter((it): it is Project => !!it);
		if (!items.length) return;
		const trash = action === 'trash';
		confirm = {
			title: `${trash ? 'Move to the trash' : 'Archive'}: ${items.length} item${items.length === 1 ? '' : 's'}`,
			lines: items.map((it) => ({ text: it.rel, sub: [bytes(it.size_bytes), ...(it.risks ?? [])].join(' · '), danger: !!it.risks?.some((r) => r !== 'not in git') })),
			note: trash
				? 'Uses the system trash: restore from your file manager. loods cannot undo this.'
				: `Moves them to ${grave?.archive}/<date>/… (same disk, nothing copied). u undoes the last archive.`,
			label: trash ? 'Trashed' : 'Archived',
			danger: trash,
			run: async () => {
				const r = await bury(action, items.map((it) => it.rel));
				marked = new Set();
				await loadGrave(false);
				return r;
			}
		};
	}

	function undoBury() {
		const batch = grave?.batches.find((b) => b.action === 'archive' && !b.restored);
		if (!batch) return flash('Nothing to undo (trashed items: restore them from the trash)');
		confirm = {
			title: `Undo the archive of ${ago(batch.time, now) === 'now' ? 'just now' : ago(batch.time, now) + ' ago'}: ${batch.items.length} item${batch.items.length === 1 ? '' : 's'}`,
			lines: batch.items.map((path) => ({ text: path })),
			note: 'Moves them back to where they were. Anything that exists there again is skipped.',
			label: 'Restored',
			run: async () => {
				const r = await unbury();
				await loadGrave(false);
				return r;
			}
		};
	}

	const labels: Record<Target, string> = { code: 'VS Code', terminal: 'Terminal', folder: 'Folder', github: 'Remote' };
	function act(p: Project | null, target: Target | 'run') {
		if (!p) return;
		if (target === 'run') return run(p);
		attempt(() => openIn(p.rel, target), `${labels[target]} → ${p.name}`);
	}

	// r on the board: start the only command, or ask which one.
	function run(p: Project) {
		const cmds = p.commands ?? [];
		if (!cmds.length) {
			flash(`${p.name}: no commands found. Add one in ${configPath}`, true);
			return;
		}
		if (cmds.length === 1) return launch(p, cmds[0].name);
		picker = { project: p, index: Math.max(0, cmds.findIndex((c) => c.name === p.default_command)) };
	}

	async function launch(p: Project, command: string) {
		picker = null;
		const id = `${p.rel}#${command}`;
		if (live.some((x) => x.id === id)) {
			showProc(id);
			return;
		}
		await attempt(() => startCommand(p.rel, command), `▶ ${p.name} · ${command}`);
		selectedProcId = id;
	}

	// Open a project's own page. Tabs: planboard, garage, git.
	function openProject(rel: string, tab: Tab = 'plan') {
		projectRel = rel;
		projectTab = tab;
		taskSel = 0;
		view = 'project';
		detailOpen = false;
	}

	// Show a process in the garage of the project it belongs to. A stack has no
	// project of its own, so its group's first project hosts it.
	function showProc(id: string) {
		selectedProcId = id;
		const pr = procs.find((x) => x.id === id);
		const group = pr?.id.startsWith('stack:') ? pr.id.slice(6) : '';
		const rel = pr?.project ?? projects.find((x) => x.group === group)?.rel;
		if (rel) openProject(rel, 'garage');
		else flash('That process belongs to no project on the board', true);
	}

	function stopProject(p: Project | null) {
		const running = p ? procsOf(p.rel) : [];
		if (!running.length) return flash('Nothing running for this project');
		for (const r of running) attempt(() => procAction('stop', r.id));
		flash(`Stopping ${running.map((r) => r.name).join(', ')}`);
	}

	function openURL(p: Proc | undefined | null) {
		const u = p?.urls?.[0];
		if (u) window.open(u, '_blank', 'noopener');
		else flash('No URL seen in the output yet', true);
	}

	function onProcAction(a: ProcAction, p: Proc) {
		switch (a) {
			case 'reload':
				return attempt(() => procAction('input', p.id, 'r'), 'Hot reload');
			case 'hot-restart':
				return attempt(() => procAction('input', p.id, 'R'), 'Hot restart');
			case 'restart':
				return attempt(() => procAction('restart', p.id), isAlive(p) ? 'Restarting…' : 'Starting…');
			case 'stop':
				return attempt(() => procAction('stop', p.id), p.status === 'stopping' ? 'Killing' : 'Stopping…');
			case 'remove':
				return attempt(() => procAction('remove', p.id));
		}
	}

	// --- agents ---

	let dock = $state<ReturnType<typeof AgentDock>>();

	/** Hand a task to a Claude agent and bring up its terminal in the dock. */
	async function assignAgent(p: Project | null, t: Task | undefined) {
		if (!p || !t) return;
		if (!t.id) return flash('This task has no number yet; it gets one on the next save', true);
		const id = `${p.rel}#agent:${t.id}`;
		const running = agentProcs.find((pr) => pr.id === id && isAlive(pr));
		if (running) {
			showAgent(id);
			return flash(`An agent is already on ${p.name}#${t.id}`);
		}
		try {
			await startAgent(p.rel, t.id);
			showAgent(id);
			flash(`✻ agent on ${p.name}#${t.id}`);
		} catch (e) {
			flash((e as Error).message, true);
		}
	}

	function showAgent(id: string) {
		agentId = id;
		dockOpen = true;
	}

	/** Type into the agent terminal, like i does in a garage. */
	async function focusDock() {
		if (!agentProcs.length) return flash('No agent terminals yet: press A on a task to hand it to one');
		dockOpen = true;
		await tick();
		dock?.focus();
	}

	function toggleDock() {
		if (!agentProcs.length) return flash('No agent terminals yet: press A on a task to hand it to one');
		dockOpen = !dockOpen;
	}

	/** The dock's "task" button: open the planboard of the task this agent is on. */
	function gotoAgentTask(pr: Proc) {
		if (pr.project && pr.task) gotoTask(pr.project, pr.task);
	}

	/** Open the planboard of a task, from the dock or the Agents view. */
	function gotoTask(rel: string, task: number) {
		openProject(rel, 'plan');
		const i = (plans[rel]?.tasks ?? []).findIndex((t) => t.id === task);
		if (i >= 0) taskSel = i;
	}

	/** + : a new project an orchestrator plans and builds. */
	function openNewProject() {
		newProj = { name: '', goal: '', group: '' };
	}

	async function createProject() {
		if (!newProj || newBusy) return;
		const { name, goal, group } = newProj;
		if (!name.trim() || !goal.trim()) return flash('A new project needs a name and what to build', true);
		newBusy = true;
		try {
			const run = await startProject(name.trim(), goal.trim(), group.trim() || undefined);
			newProj = null;
			selectedJob = `${run.id}:`;
			view = 'agents';
			if (run.orchestrator) showAgent(run.orchestrator);
			flash(`✻ ${run.project}: an orchestrator is planning the work`);
		} catch (e) {
			flash((e as Error).message, true);
		} finally {
			newBusy = false;
		}
	}

	// Cancelling asks first: it stops every session the run started, and an
	// agent halfway through a commit is worth a confirmation.
	function askCancelRun(r: AgentRun) {
		const busy = (r.jobs ?? []).filter((j) => j.status === 'running');
		confirm = {
			title: `Cancel run ${r.id} of ${r.project}`,
			lines: [
				{ text: r.goal, sub: `${(r.jobs ?? []).length} jobs, ${busy.length} working` },
				...busy.map((j) => ({ text: j.role || j.id, sub: 'its session is stopped', danger: true }))
			],
			note: 'The jobs that already finished keep what they did. Nothing is handed out after this.',
			label: 'cancelled',
			run: async () => {
				await cancelRun(r.id);
				return [{ name: r.id, ok: true, note: 'cancelled' }];
			}
		};
	}

	async function onStack(s: Stack) {
		await attempt(() => startStack(s.name), `▶ stack ${s.name}`);
		if (s.run) selectedProcId = 'stack:' + s.name;
	}

	async function savePlan(p: Project, patch: PlanPatch, ok?: string) {
		try {
			plans = { ...plans, [p.rel]: await updatePlan(p.rel, patch) };
			if (ok) flash(ok);
		} catch (e) {
			flash((e as Error).message, true);
		}
	}

	function setStatus(p: Project | null, status: Status | '') {
		statusPicker = null;
		if (!p || (plans[p.rel]?.status ?? '') === status) return;
		savePlan(p, { status }, `${p.name} → ${statusLabel(status)}`);
		select(p);
	}

	function cyclePriority(p: Project | null) {
		if (!p) return;
		const n = nextPriority(plans[p.rel]?.priority);
		savePlan(p, { priority: n }, `${p.name}: ${n ? 'P' + n : 'no priority'}`);
	}

	// n / a / N: go to the project's planboard with that field ready to type in.
	async function editPlan(field: 'next' | 'task' | 'notes', p: Project | null = selected) {
		if (!p) return;
		if (view !== 'project' || projectRel !== p.rel) openProject(p.rel, 'plan');
		else projectTab = 'plan';
		await tick();
		if (field === 'next') page?.focusNext();
		else if (field === 'task') page?.addTask(selectedTaskState());
		else page?.focusNotes();
	}

	/** Column the planboard selection sits in, so a adds a task right there. */
	function selectedTaskState(): TaskState {
		return tasks[taskSel]?.state ?? '';
	}

	function saveTasks(p: Project, next: Task[], note?: string) {
		savePlan(p, note ? { tasks: next, note } : { tasks: next });
	}

	/** H / L on the planboard: move the selected task one column. */
	function shiftTask(step: number) {
		if (!project) return;
		const t = tasks[taskSel];
		if (!t) return;
		const i = TASK_STATES.indexOf(t.state ?? '');
		const to = TASK_STATES[i + step];
		if (to === undefined) return;
		const next = moveTask(tasks, taskSel, to);
		taskSel = next.length - 1; // moveTask puts it at the end of its column
		saveTasks(project, next);
	}

	/** space on the planboard: done ⇄ to do, the one move worth a single key. */
	function toggleTask() {
		if (!project || !tasks[taskSel]) return;
		const next = moveTask(tasks, taskSel, tasks[taskSel].state === 'done' ? '' : 'done');
		taskSel = next.length - 1;
		saveTasks(project, next);
	}

	// del on the planboard. A keystroke away from losing text, so it asks first;
	// the ✕ on a card is a deliberate click and deletes straight away.
	function deleteTask() {
		const p = project;
		const t = p && tasks[taskSel];
		if (!p || !t) return;
		const i = taskSel;
		confirm = {
			title: `Delete this task in ${p.name}`,
			lines: [{ text: t.text }],
			label: 'Deleted',
			run: async () => {
				taskSel = Math.max(0, i - 1);
				saveTasks(p, tasks.filter((_, j) => j !== i));
			}
		};
	}

	// X on the planboard: take a task back from an agent that is not coming back.
	// It asks first, because a live agent will keep working and reclaim it.
	function releaseTask() {
		const p = project;
		const t = p && tasks[taskSel];
		const c = t?.id ? projectClaims[t.id] : undefined;
		if (!p || !t || !c) return flash('No agent on this task');
		const stale = claimStale(c, now);
		confirm = {
			title: `Free ${p.name}#${t.id} from ${c.agent}`,
			lines: [{ text: t.text, sub: `${stale ? 'no heartbeat for' : 'working, last heartbeat'} ${ago(c.last_heartbeat, now)}`, danger: !stale }],
			note: stale
				? 'The agent stopped reporting in. Freeing it only removes the claim; the task stays in progress.'
				: 'This agent still reports in. If it is really running, it will carry on and claim the task again.',
			label: 'Freed',
			danger: !stale,
			run: () => attempt(() => releaseClaim(p.rel, t.id!))
		};
	}

	// Planboard movement: h/l across columns keeping the row, j/k within one.
	function moveTaskSel(dir: 'left' | 'right' | 'up' | 'down') {
		const ci = taskCols.findIndex((col) => col.some((c) => c.i === taskSel));
		if (ci < 0) {
			const first = taskCols.flat()[0];
			if (first) taskSel = first.i;
			return;
		}
		const col = taskCols[ci];
		const ri = col.findIndex((c) => c.i === taskSel);
		if (dir === 'up') taskSel = col[Math.max(0, ri - 1)].i;
		else if (dir === 'down') taskSel = col[Math.min(col.length - 1, ri + 1)].i;
		else {
			const step = dir === 'left' ? -1 : 1;
			for (let j = ci + step; j >= 0 && j < taskCols.length; j += step) {
				const other = taskCols[j];
				if (other.length) {
					taskSel = other[Math.min(ri, other.length - 1)].i;
					break;
				}
			}
		}
		tick().then(() => document.querySelector(`[data-task="${taskSel}"]`)?.scrollIntoView({ block: 'nearest' }));
	}

	const cardEl = (id: string) => document.querySelector<HTMLElement>(`[data-id="${CSS.escape(id)}"]`);

	async function select(p: Project | undefined) {
		if (!p) return;
		selectedId = p.rel;
		await tick();
		cardEl(p.rel)?.scrollIntoView({ block: 'nearest' });
	}

	// Left/right walk the list; up/down jump to the nearest card on the next
	// visual row, so movement follows the grid even across group headers.
	function move(dir: 'left' | 'right' | 'up' | 'down') {
		const i = flat.findIndex((p) => p.rel === selectedId);
		if (i < 0) return select(flat[0]);
		if (dir === 'left') return select(flat[Math.max(0, i - 1)]);
		if (dir === 'right') return select(flat[Math.min(flat.length - 1, i + 1)]);
		const rects = flat.map((p) => cardEl(p.rel)?.getBoundingClientRect());
		const cur = rects[i];
		if (!cur) return;
		const cx = cur.left + cur.width / 2;
		const down = dir === 'down';
		let best = -1;
		let bestTop = down ? Infinity : -Infinity;
		let bestDx = Infinity;
		rects.forEach((r, j) => {
			if (!r || (down ? r.top <= cur.top + 4 : r.top >= cur.top - 4)) return;
			const dx = Math.abs(r.left + r.width / 2 - cx);
			const closerRow = down ? r.top < bestTop - 4 : r.top > bestTop + 4;
			if (closerRow || (Math.abs(r.top - bestTop) <= 4 && dx < bestDx)) {
				best = j;
				bestTop = r.top;
				bestDx = dx;
			}
		});
		if (best >= 0) select(flat[best]);
	}

	function moveHygiene(delta: number) {
		if (!hygieneRows.length) return;
		hyIndex = Math.min(hygieneRows.length - 1, Math.max(0, hyIndex + delta));
		tick().then(() => document.querySelector(`[data-row="${hyIndex}"]`)?.scrollIntoView({ block: 'nearest' }));
	}

	function moveGrave(delta: number) {
		const i = graveItems.findIndex((it) => it.rel === graveSel);
		const next = graveItems[Math.min(graveItems.length - 1, Math.max(0, i + delta))];
		if (!next) return;
		graveSel = next.rel;
		tick().then(() => cardEl(next.rel)?.scrollIntoView({ block: 'nearest' }));
	}

	// j / k in a project's garage walk that project's own processes.
	function moveProc(delta: number) {
		const list = projectProcs;
		if (!list.length) return;
		const i = list.findIndex((p) => p.id === selectedProcId);
		selectedProcId = list[Math.min(list.length - 1, Math.max(0, i + delta))].id;
	}

	function focusFilter() {
		if (view === 'project') view = 'board';
		tick().then(() => {
			filterEl?.focus();
			filterEl?.select();
		});
	}

	function confirmKey(e: KeyboardEvent) {
		if (e.key === 'Escape' || e.key === 'n') confirm = null;
		else if (e.key === 'Enter' || e.key === 'y') runConfirm();
		else return;
		e.preventDefault();
	}

	function hygieneKey(e: KeyboardEvent) {
		const row = hygieneRows[hyIndex];
		switch (e.key) {
			case 'ArrowDown':
			case 'j':
				return moveHygiene(1);
			case 'ArrowUp':
			case 'k':
				return moveHygiene(-1);
			case 'Enter':
				if (!row) return;
				if (row.f.fix || row.f.url) return fixFinding(row.p, row.f);
				detailOpen = !detailOpen;
				return;
		}
		return projectKey(e);
	}

	function graveKey(e: KeyboardEvent) {
		switch (e.key) {
			case 'ArrowDown':
			case 'j':
				return moveGrave(1);
			case 'ArrowUp':
			case 'k':
				return moveGrave(-1);
			case ' ':
				if (graveSel) toggleMark(graveSel);
				return moveGrave(1);
			case 'a':
				return buryMarked('archive');
			case 'x':
				return buryMarked('trash');
			case 'u':
				return undoBury();
			case 'd':
				graveDupes = !graveDupes;
				return;
			case 's':
				graveSort = graveSort === 'age' ? 'size' : graveSort === 'size' ? 'name' : 'age';
				return;
			case 'Escape':
				marked = new Set();
				return;
		}
		return false;
	}

	function statusKey(e: KeyboardEvent) {
		if (e.key === 'Escape' || e.key === 'm') statusPicker = null;
		else if (/^[0-5]$/.test(e.key)) setStatus(statusPicker, COLUMNS[+e.key]);
		else return;
		e.preventDefault();
	}

	function pickerKey(e: KeyboardEvent) {
		if (!picker) return;
		const cmds = picker.project.commands ?? [];
		if (e.key === 'Escape') picker = null;
		else if (e.key === 'ArrowDown' || e.key === 'j') picker.index = Math.min(cmds.length - 1, picker.index + 1);
		else if (e.key === 'ArrowUp' || e.key === 'k') picker.index = Math.max(0, picker.index - 1);
		else if (e.key === 'Enter' || e.key === 'r') launch(picker.project, cmds[picker.index].name);
		else if (/^[1-9]$/.test(e.key) && cmds[+e.key - 1]) launch(picker.project, cmds[+e.key - 1].name);
		else return;
		e.preventDefault();
	}

	function boardKey(e: KeyboardEvent) {
		switch (e.key) {
			case 'ArrowLeft':
			case 'h':
				return move('left');
			case 'ArrowRight':
			case 'l':
				return move('right');
			case 'ArrowUp':
			case 'k':
				return move('up');
			case 'ArrowDown':
			case 'j':
				return move('down');
			case 'L':
				if (!selected) return;
				return openProject(selected.rel, 'garage');
			case 's':
				sortBy = sortBy === 'activity' ? 'priority' : sortBy === 'priority' ? 'name' : 'activity';
				flash(`Sorted by ${sortBy}`);
				return;
		}
		return projectKey(e);
	}

	// Keys that act on the project under the cursor, on the Board and in Hygiene.
	function projectKey(e: KeyboardEvent) {
		switch (e.key) {
			case 'Enter':
				if (selected) return openProject(selected.rel);
				return;
			case ' ':
				detailOpen = !detailOpen;
				return;
			case 'Escape':
				if (detailOpen) detailOpen = false;
				else query = '';
				return;
			case 'c':
				return act(selected, 'code');
			case 't':
				return act(selected, 'terminal');
			case 'o':
				return act(selected, 'folder');
			case 'g':
				return act(selected, 'github');
			case 'r':
				return selected && run(selected);
			case 'x':
				return stopProject(selected);
			case 'w':
				return openURL(selected && procsOf(selected.rel).find((p) => p.urls?.length));
			case 'n':
				return editPlan('next');
			case 'a':
				return editPlan('task');
			case 'N':
				return editPlan('notes');
			case 'p':
				return cyclePriority(selected);
			case 'm':
				statusPicker = selected;
				return;
		}
		return false;
	}

	// On a project page: esc leaves, 1–3 pick the tab, the rest is the tab's own.
	function projectPageKey(e: KeyboardEvent) {
		if (e.key === 'Escape' && projectTab === 'plan' && page?.closeTaskMenu()) return;
		if (e.key === 'Escape') {
			view = 'board';
			if (projectRel) selectedId = projectRel;
			return;
		}
		if (/^[1-3]$/.test(e.key)) {
			projectTab = TABS[+e.key - 1];
			return;
		}
		if (projectTab === 'garage') return garageKey(e);
		if (projectTab === 'plan' && planTabKey(e) !== false) return;
		return pageProjectKey(e);
	}

	function planTabKey(e: KeyboardEvent) {
		switch (e.key) {
			case 'ArrowLeft':
			case 'h':
				return moveTaskSel('left');
			case 'ArrowRight':
			case 'l':
				return moveTaskSel('right');
			case 'ArrowUp':
			case 'k':
				return moveTaskSel('up');
			case 'ArrowDown':
			case 'j':
				return moveTaskSel('down');
			case 'H':
				return shiftTask(-1);
			case 'L':
				return shiftTask(1);
			case ' ':
				return toggleTask();
			case 'Enter':
				return page?.editTask(taskSel);
			case 'Delete':
			case 'Backspace':
				return deleteTask();
			case 'X':
				return releaseTask();
			case 'A':
				return assignAgent(project, tasks[taskSel]);
			case 'i':
				return focusDock();
		}
		return false;
	}

	// Keys that act on the project whose page is open.
	function pageProjectKey(e: KeyboardEvent) {
		switch (e.key) {
			case 'c':
				return act(project, 'code');
			case 't':
				return act(project, 'terminal');
			case 'o':
				return act(project, 'folder');
			case 'g':
				return act(project, 'github');
			case 'r':
				return project && run(project);
			case 'x':
				return stopProject(project);
			case 'w':
				return openURL(project && procsOf(project.rel).find((pr) => pr.urls?.length));
			case 'a':
				return editPlan('task', project);
			case 'n':
				return editPlan('next', project);
			case 'N':
				return editPlan('notes', project);
			case 'p':
				return cyclePriority(project);
			case 'm':
				statusPicker = project;
				return;
		}
		return false;
	}

	function garageKey(e: KeyboardEvent) {
		const p = selectedProc;
		switch (e.key) {
			case 'ArrowDown':
			case 'j':
				return moveProc(1);
			case 'ArrowUp':
			case 'k':
				return moveProc(-1);
			case 'i':
			case 'Enter':
				return page?.focusTerminal();
			case 'x':
				return p && isAlive(p) && onProcAction('stop', p);
			case 'r':
				return p && onProcAction('restart', p);
			case 'u':
				return p?.keys && onProcAction('reload', p);
			case 'U':
				return p?.keys && onProcAction('hot-restart', p);
			case 'w':
				return openURL(p);
			case 'Delete':
			case 'Backspace':
				return p && !isAlive(p) && onProcAction('remove', p);
		}
		return pageProjectKey(e);
	}

	function onkeydown(e: KeyboardEvent) {
		const target = e.target as HTMLElement | null;
		// Typing into a process terminal: everything goes to the process, except esc.
		if (target?.closest('.xterm')) {
			if (e.key === 'Escape') target.blur();
			return;
		}
		// Typing in the plan editor: fields handle their own keys.
		if (target !== filterEl && target?.matches('input, textarea, select')) return;
		if (e.ctrlKey && e.key === 'k') {
			e.preventDefault();
			return focusFilter();
		}
		if (e.ctrlKey || e.metaKey || e.altKey) return;
		if (confirm) return confirmKey(e);
		if (picker) return pickerKey(e);
		if (statusPicker) return statusKey(e);
		if (newProj) {
			if (e.key === 'Escape') newProj = null;
			else if (e.key === 'Enter') createProject();
			else return; // typing in the fields
			e.preventDefault();
			return;
		}
		if (e.target === filterEl) {
			if (e.key === 'Escape') {
				query = '';
				filterEl?.blur();
			} else if (e.key === 'Enter' || e.key === 'ArrowDown') {
				filterEl?.blur();
				select(flat[0]);
			} else return;
			e.preventDefault();
			return;
		}
		if (helpOpen && (e.key === 'Escape' || e.key === '?')) {
			helpOpen = false;
			e.preventDefault();
			return;
		}
		// 1–4 switch top-level views, except on a project page, where they pick the
		// project's own tab.
		if (view !== 'project' && /^[1-4]$/.test(e.key)) {
			view = VIEWS[+e.key - 1];
			e.preventDefault();
			return;
		}
		switch (e.key) {
			case '+':
				openNewProject();
				break;
			case '`':
				toggleDock();
				break;
			case '/':
				focusFilter();
				break;
			case 'R':
				attempt(rescan, 'Rescanning…');
				if (view === 'graveyard') loadGrave(true);
				break;
			case '?':
				helpOpen = !helpOpen;
				break;
			default:
				{
					const handle = { board: boardKey, hygiene: hygieneKey, graveyard: graveKey, agents: () => true, project: projectPageKey }[
						view
					];
					if (handle(e) === false) return;
				}
		}
		e.preventDefault();
	}

	const keys: [string, string, string][] = [
		['1 – 4', 'Board · Hygiene · Graveyard · Agents', 'board, hygiene, graveyard, agents'],
		['+', 'new project: an orchestrator plans it and agents build it', 'any'],
		['/  ctrl+k', 'filter projects (also matches status and next step)', 'any'],
		['R', 'rescan now', 'any'],
		['←↓↑→ / hjkl', 'move', 'board, planboard'],
		['enter', 'open the project page', 'board'],
		['space', 'details drawer: branches, PRs, findings', 'board'],
		['L', 'open its garage', 'board'],
		['s', 'sort by activity / priority / name', 'board'],
		['esc', 'back to the board', 'project page'],
		['1 – 3', 'Planboard · Garage · Git', 'project page'],
		['c · t · o · g', 'VS Code · terminal · folder · remote', 'board, project page'],
		['r', 'run (asks when there are several commands)', 'board, project page'],
		['x', 'stop everything of this project', 'board, project page'],
		['w', 'open its web URL', 'board, project page'],
		['m', 'set status (0 inbox, 1–5)', 'board, project page'],
		['p', 'cycle priority P1 → P2 → P3 → none', 'board, project page'],
		['n · N', 'edit next step · notes', 'board, project page'],
		['a', 'add a task in the selected column', 'board, planboard'],
		['H / L', 'move a task one column left / right', 'planboard'],
		['space', 'task done ⇄ to do', 'planboard'],
		['enter', 'rename the selected task', 'planboard'],
		['del', 'delete the selected task', 'planboard'],
		['click a task', 'its menu: assign to an agent, move, rename, delete', 'planboard'],
		['A', 'hand the selected task to a Claude agent (terminal in the dock)', 'planboard'],
		['X', 'take a task back from an agent that stopped reporting in', 'planboard'],
		['`', 'show / hide the agent dock', 'any'],
		['i', 'type into the agent terminal (esc leaves)', 'planboard'],
		['j / k', 'select process', 'garage'],
		['i / enter', 'type into the terminal (esc leaves)', 'garage'],
		['r · x', 'restart · stop (twice: kill)', 'garage'],
		['u · U', 'flutter hot reload · hot restart', 'garage'],
		['del', 'remove an exited process', 'garage'],
		['j / k · enter', 'select · fix (asks first) or details', 'hygiene'],
		['space', 'mark', 'graveyard'],
		['a · x', 'archive · trash the marked (or selected) items', 'graveyard'],
		['u', 'undo the last archive', 'graveyard'],
		['d · s', 'duplicates only · sort by age / size / name', 'graveyard'],
		['y / n', 'confirm / cancel a question', 'any']
	];
</script>

<svelte:window {onkeydown} />

<div class="app" class:with-detail={drawerView && detailOpen && selected}>
	<header class="top">
		<div class="brand">
			<img src="/icon.svg" alt="" width="22" height="22" />
			<span>loods</span>
		</div>
		<nav>
			<button class="tab" class:active={view === 'board'} onclick={() => (view = 'board')}>
				Board
				{#if view !== 'project'}<kbd>1</kbd>{/if}
			</button>
			<button class="tab" class:active={view === 'hygiene'} onclick={() => (view = 'hygiene')}>
				Hygiene
				{#if attention}<span class="count warn">{attention}</span>{/if}
				{#if view !== 'project'}<kbd>2</kbd>{/if}
			</button>
			<button class="tab" class:active={view === 'graveyard'} onclick={() => (view = 'graveyard')}>
				Graveyard
				{#if view !== 'project'}<kbd>3</kbd>{/if}
			</button>
			<button class="tab" class:active={view === 'agents'} onclick={() => (view = 'agents')}>
				Agents
				{#if liveRuns.length}<span class="count run">{liveRuns.length}</span>{/if}
				{#if view !== 'project'}<kbd>4</kbd>{/if}
			</button>
			{#if view === 'project' && project}
				<span class="crumb">/ {project.name}</span>
			{/if}
		</nav>
		{#if view !== 'project'}
			<button class="newproj" onclick={openNewProject} title="New project, planned and built by agents (+)">
				+ new project
			</button>
			<input
				bind:this={filterEl}
				bind:value={query}
				class="filter"
				placeholder="Filter projects, branches, stack…   /"
				spellcheck="false"
			/>
		{/if}
		<div class="status">
			{#if mem.total}
				<span class="mem" title="system memory in use · {bytes(liveRSS)} by loods processes">
					<span class="bar"><span style:width="{Math.round(memUsed * 100)}%" class:high={memUsed > 0.85}></span></span>
					RAM {Math.round(memUsed * 100)}%
					{#if liveRSS}<span class="muted">· {bytes(liveRSS)} here</span>{/if}
				</span>
			{/if}
			{#if live.length}<span class="running" title="processes running in project garages">{live.length} running</span>{/if}
			{#if working}<span class="working" title="tasks an agent is working on">✻ {working} with claude</span>{/if}
			{#if attention}<button class="attn" title="open Hygiene (2)" onclick={() => (view = 'hygiene')}>{attention} need attention</button>{/if}
			<span class="scan" class:busy={scanning} title={root}>
				<span class="conn" class:up={connected}></span>
				{scanning ? 'scanning…' : scannedAt ? (ago(scannedAt, now) === 'now' ? 'scanned just now' : `scanned ${ago(scannedAt, now)} ago`) : 'connecting…'}
			</span>
		</div>
	</header>

	{#if configError}
		<div class="banner">Config error in <code>{configPath}</code>: {configError}</div>
	{/if}
	{#if plansError}
		<div class="banner">Can't read <code>{plansPath}</code>: {plansError}. Showing the last good version; fix the file to edit plans again.</div>
	{/if}

	{#if view === 'board'}
		<main>
			<div role="listbox" aria-label="Projects">
				{#if !loaded}
					<p class="empty">Connecting to loods…</p>
				{:else if !flat.length}
					<p class="empty">{query ? `Nothing matches “${query}”.` : `No projects found under ${root}.`}</p>
				{/if}
				{#each groups as g (g.name)}
					<section>
						<h2>
							{g.name || 'projects'} <span>{g.items.length}</span>
							{#each stacks.filter((s) => s.name === g.name) as s (s.name)}
								{@const sp = procs.find((p) => p.id === 'stack:' + s.name && isAlive(p))}
								<button class="stackbtn" class:on={sp} onclick={() => (sp ? showProc(sp.id) : onStack(s))} title={s.run}>
									{sp ? '● stack running' : '▶ start stack'}
								</button>
							{/each}
						</h2>
						<div class="grid">
							{#each g.items as p (p.rel)}
								<Card
									{p}
									plan={plans[p.rel]}
									claude={claude[p.rel]}
									gh={github[p.rel]}
									{now}
									procs={procsOf(p.rel)}
									working={workingPer[p.rel] ?? 0}
									sessions={sessionsPer[p.rel] ?? 0}
									selected={p.rel === selectedId}
									onselect={() => (selectedId = p.rel)}
									ondetail={() => openProject(p.rel)}
									onact={(t) => act(p, t)}
								/>
							{/each}
						</div>
					</section>
				{/each}
			</div>
		</main>
	{:else if view === 'hygiene'}
		<main>
			<Hygiene
				rows={hygieneRows}
				selected={hyIndex}
				{githubStatus}
				onselect={(i) => (hyIndex = i)}
				onfix={(r) => fixFinding(r.p, r.f)}
				ondetail={(r) => ((hyIndex = hygieneRows.indexOf(r)), (detailOpen = true))}
			/>
		</main>
	{:else if view === 'graveyard'}
		<main>
			<Graveyard
				items={graveItems}
				{plans}
				batches={grave?.batches ?? []}
				{marked}
				selectedId={graveSel}
				loading={graveLoading}
				scannedAt={grave?.scanned_at ?? ''}
				archive={grave?.archive ?? ''}
				sortBy={graveSort}
				dupesOnly={graveDupes}
				{now}
				onselect={(rel) => (graveSel = rel)}
				ontoggle={toggleMark}
				onundo={undoBury}
			/>
		</main>
	{:else if view === 'agents'}
		<main>
			<Agents
				runs={runList}
				{projects}
				{procs}
				{selectedJob}
				{now}
				onselect={(key) => (selectedJob = key)}
				onopen={showAgent}
				onstop={(pr) => onProcAction('stop', pr)}
				oncancel={askCancelRun}
				onproject={(rel) => openProject(rel, 'plan')}
				ontask={gotoTask}
			/>
		</main>
	{:else if view === 'project' && project}
		<ProjectPage
			bind:this={page}
			p={project}
			plan={plans[project.rel]}
			claude={claude[project.rel]}
			gh={github[project.rel]}
			findings={findingsOf(project)}
			version={gitVersion}
			tab={projectTab}
			procs={projectProcs}
			stacks={groupStacks}
			{projects}
			selectedProcId={selectedProcId}
			{taskSel}
			claims={projectClaims}
			agents={projectAgents}
			{now}
			{configPath}
			ontab={(t) => (projectTab = t)}
			onback={() => ((view = 'board'), (selectedId = project.rel))}
			onact={(t) => act(project, t)}
			onrun={() => run(project)}
			onstop={() => stopProject(project)}
			oncommand={(name) => launch(project, name)}
			onsave={(patch) => savePlan(project, patch)}
			ontasks={(next, note) => saveTasks(project, next, note)}
			ontaskselect={(i) => (taskSel = i)}
			onrelease={(i) => ((taskSel = i), releaseTask())}
			onassign={(i) => ((taskSel = i), assignAgent(project, tasks[i]))}
			onterminal={(i) => {
				const t = tasks[i];
				if (t?.id) showAgent(`${project.rel}#agent:${t.id}`);
			}}
			onprocselect={(id) => (selectedProcId = id)}
			onprocaction={onProcAction}
			onstack={onStack}
			onfix={(f) => fixFinding(project, f)}
			ondelete={(branches, data) => deleteBranches(project, branches, data)}
			onundo={() => undoGit(project)}
		/>
	{:else}
		<main><p class="empty">Loading project…</p></main>
	{/if}

	{#if agentProcs.length}
		<AgentDock
			bind:this={dock}
			agents={agentProcs}
			runs={runList}
			{projects}
			selectedId={selectedAgent?.id ?? null}
			open={dockOpen}
			height={dockHeight}
			{now}
			onselect={(id) => (agentId === id && dockOpen ? (dockOpen = false) : showAgent(id))}
			onaction={onProcAction}
			ontoggle={toggleDock}
			onheight={(h) => (dockHeight = h)}
			ongoto={gotoAgentTask}
			onrun={(pr) => ((selectedJob = `${pr.run_id}:${pr.job ?? ''}`), (view = 'agents'))}
		/>
	{/if}

	<footer class="keys">
		{#if view === 'board'}
			<span><kbd>hjkl</kbd> move</span>
			<span><kbd>enter</kbd> open project</span>
			<span><kbd>space</kbd> details</span>
			<span><kbd>r</kbd> run</span>
			<span><kbd>L</kbd> garage</span>
			<span><kbd>c</kbd> code</span>
			<span><kbd>t</kbd> terminal</span>
			<span><kbd>/</kbd> filter</span>
			<span><kbd>s</kbd> sort: {sortBy}</span>
		{:else if view === 'project' && projectTab === 'plan'}
			<span><kbd>esc</kbd> board</span>
			<span><kbd>1</kbd><kbd>2</kbd><kbd>3</kbd> plan · garage · git</span>
			<span><kbd>hjkl</kbd> move</span>
			<span><kbd>H</kbd><kbd>L</kbd> move task</span>
			<span><kbd>a</kbd> add task</span>
			<span><kbd>space</kbd> done</span>
			<span><kbd>enter</kbd> rename</span>
			<span><kbd>A</kbd> assign to agent</span>
			<span><kbd>X</kbd> free from agent</span>
			<span><kbd>n</kbd> next step</span>
			<span><kbd>m</kbd> status</span>
			<span><kbd>r</kbd> run</span>
		{:else if view === 'project' && projectTab === 'garage'}
			<span><kbd>esc</kbd> board</span>
			<span><kbd>1</kbd><kbd>2</kbd><kbd>3</kbd> plan · garage · git</span>
			<span><kbd>j</kbd><kbd>k</kbd> select</span>
			<span><kbd>i</kbd> type in terminal</span>
			<span><kbd>r</kbd> restart</span>
			<span><kbd>x</kbd> stop</span>
			<span><kbd>u</kbd> reload</span>
			<span><kbd>w</kbd> open URL</span>
		{:else if view === 'project'}
			<span><kbd>esc</kbd> board</span>
			<span><kbd>1</kbd><kbd>2</kbd><kbd>3</kbd> plan · garage · git</span>
			<span><kbd>c</kbd> code</span>
			<span><kbd>t</kbd> terminal</span>
			<span><kbd>g</kbd> remote</span>
			<span><kbd>r</kbd> run</span>
		{:else if view === 'agents'}
			<span><kbd>click</kbd> a session shows its terminal in the dock</span>
			<span><kbd>+</kbd> new project</span>
			<span><kbd>`</kbd> dock</span>
		{:else if view === 'hygiene'}
			<span><kbd>j</kbd><kbd>k</kbd> select</span>
			<span><kbd>enter</kbd> fix / details</span>
			<span><kbd>space</kbd> details</span>
			<span><kbd>c</kbd> code</span>
			<span><kbd>t</kbd> terminal</span>
		{:else if view === 'graveyard'}
			<span><kbd>j</kbd><kbd>k</kbd> select</span>
			<span><kbd>space</kbd> mark</span>
			<span><kbd>a</kbd> archive</span>
			<span><kbd>x</kbd> trash</span>
			<span><kbd>u</kbd> undo</span>
			<span><kbd>d</kbd> dupes{graveDupes ? ' ✓' : ''}</span>
			<span><kbd>s</kbd> sort: {graveSort}</span>
			<span><kbd>R</kbd> re-measure</span>
		{/if}
		<span><kbd>?</kbd> help</span>
	</footer>
</div>

{#if drawerView && detailOpen && selected}
	<Detail
		p={selected}
		plan={plans[selected.rel]}
		claude={claude[selected.rel]}
		gh={github[selected.rel]}
		findings={findingsOf(selected)}
		version={gitVersion}
		onfix={(f) => fixFinding(selected, f)}
		ondelete={(branches, data) => deleteBranches(selected, branches, data)}
		onundo={() => undoGit(selected)}
		{now}
		onclose={() => (detailOpen = false)}
		onact={(t) => act(selected, t)}
		onsave={(patch) => savePlan(selected, patch)}
	/>
{/if}

{#if statusPicker}
	{@const current = plans[statusPicker.rel]?.status ?? ''}
	<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
	<div class="overlay" onclick={() => (statusPicker = null)}>
		<div class="modal picker" onclick={(e) => e.stopPropagation()}>
			<h2>Status of {statusPicker.name}</h2>
			<ul>
				{#each COLUMNS as st, i (st)}
					<li>
						<button class:active={st === current} onclick={() => setStatus(statusPicker, st)}>
							<kbd>{i}</kbd>
							<span class="cname">{statusLabel(st)}</span>
							<code></code>
							{#if st === current}<span class="def">current</span>{/if}
						</button>
					</li>
				{/each}
			</ul>
			<p class="foot">Plans are stored in <code>{plansPath}</code></p>
		</div>
	</div>
{/if}

{#if newProj}
	<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
	<div class="overlay" onclick={() => (newProj = null)}>
		<div class="modal newp" onclick={(e) => e.stopPropagation()}>
			<h2>New project</h2>
			<p class="intro">
				loods makes the folder, a git repository and a plan, then hands your goal to an orchestrator: it splits
				the work into jobs and loods runs them as agents, several at a time. Watch them in Agents (<kbd>4</kbd>).
			</p>
			<label>
				<span>name</span>
				<!-- svelte-ignore a11y_autofocus -->
				<input bind:value={newProj.name} placeholder="huizenzoeker" spellcheck="false" autofocus />
			</label>
			<label>
				<span>group</span>
				<input bind:value={newProj.group} placeholder="(none)" spellcheck="false" />
			</label>
			<label class="wide">
				<span>what to build</span>
				<textarea bind:value={newProj.goal} rows="3" placeholder="a house-hunting site with svelte and express"
				></textarea>
			</label>
			<div class="acts">
				<p class="foot">{root}/{newProj.group ? newProj.group + '/' : ''}{newProj.name || '…'}</p>
				<button onclick={() => (newProj = null)}>cancel</button>
				<button class="go" onclick={createProject} disabled={newBusy}>
					<kbd>enter</kbd> {newBusy ? 'creating…' : 'create and plan it'}
				</button>
			</div>
		</div>
	</div>
{/if}

{#if picker}
	{@const cmds = picker.project.commands ?? []}
	<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
	<div class="overlay" onclick={() => (picker = null)}>
		<div class="modal picker" onclick={(e) => e.stopPropagation()}>
			<h2>Run in {picker.project.name}</h2>
			<ul>
				{#each cmds as c, i (c.name)}
					{@const running = live.some((p) => p.id === `${picker?.project.rel}#${c.name}`)}
					<li>
						<button class:active={i === picker.index} onclick={() => picker && launch(picker.project, c.name)}>
							<kbd>{i + 1}</kbd>
							<span class="cname">{c.name}</span>
							<code>{c.run}</code>
							{#if running}<span class="on">running</span>{:else if c.name === picker.project.default_command}<span class="def">default</span>{/if}
						</button>
					</li>
				{/each}
			</ul>
			<p class="foot">Set the default or add commands in <code>{configPath}</code></p>
		</div>
	</div>
{/if}

{#if confirm}
	<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
	<div class="overlay" onclick={() => !confirmBusy && (confirm = null)}>
		<div class="modal confirm" class:danger={confirm.danger} onclick={(e) => e.stopPropagation()}>
			<h2>{confirm.title}</h2>
			<ul>
				{#each confirm.lines as l (l.text)}
					<li class:danger={l.danger}>
						<span class="ltext">{l.text}</span>
						{#if l.sub}<span class="lsub">{l.sub}</span>{/if}
					</li>
				{/each}
			</ul>
			{#if confirm.note}<p class="foot">{confirm.note}</p>{/if}
			<div class="actions">
				<button onclick={() => (confirm = null)} disabled={confirmBusy}><kbd>n</kbd> cancel</button>
				<button class="go" onclick={runConfirm} disabled={confirmBusy}><kbd>y</kbd> {confirmBusy ? 'working…' : 'yes, do it'}</button>
			</div>
		</div>
	</div>
{/if}

{#if helpOpen}
	<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
	<div class="overlay" onclick={() => (helpOpen = false)}>
		<div class="modal help">
			<h2>Keys</h2>
			<dl>
				{#each keys as [k, v, where] (k + where)}
					<dt><kbd>{k}</kbd></dt>
					<dd>{v} <span class="where">{where}</span></dd>
				{/each}
			</dl>
		</div>
	</div>
{/if}

{#if toast}
	<div class="toast" class:error={toast.error}>{toast.text}</div>
{/if}

<style>
	.app {
		height: 100vh;
		display: flex;
		flex-direction: column;
		transition: padding-right 0.15s;
	}
	.app.with-detail {
		padding-right: min(480px, 100vw);
	}

	.top {
		position: sticky;
		top: 0;
		z-index: 5;
		display: flex;
		align-items: center;
		gap: 20px;
		padding: 10px 24px;
		background: color-mix(in srgb, var(--bg) 88%, transparent);
		backdrop-filter: blur(8px);
		border-bottom: 1px solid var(--line);
	}
	.brand {
		display: flex;
		align-items: center;
		gap: 8px;
		font-weight: 700;
		font-size: 16px;
		letter-spacing: -0.01em;
	}
	nav {
		display: flex;
		gap: 2px;
	}
	.tab {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 4px 10px;
		border: none;
		border-radius: 6px;
		background: none;
		font-size: 13px;
		color: var(--muted);
		cursor: pointer;
	}
	.tab kbd {
		font-size: 10px;
		padding: 0 4px;
		opacity: 0.6;
	}
	.tab.active {
		background: var(--card);
		color: var(--text);
		box-shadow: var(--shadow);
	}
	.crumb {
		align-self: center;
		font-size: 13px;
		color: var(--muted);
	}
	.count.warn {
		background: var(--accent);
	}
	.count {
		font-size: 11px;
		min-width: 17px;
		padding: 0 5px;
		border-radius: 9px;
		background: var(--running);
		color: var(--bg);
		font-weight: 600;
		text-align: center;
	}
	.filter {
		flex: 1;
		max-width: 420px;
		padding: 6px 10px;
		border: 1px solid var(--line);
		border-radius: 7px;
		background: var(--card);
		color: var(--text);
		font: inherit;
		outline: none;
	}
	.filter:focus {
		border-color: var(--accent);
	}
	.status {
		margin-left: auto;
		display: flex;
		gap: 14px;
		align-items: center;
		font-size: 12.5px;
		color: var(--muted);
		white-space: nowrap;
	}
	.mem {
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.bar {
		width: 46px;
		height: 6px;
		border-radius: 3px;
		background: var(--chip);
		overflow: hidden;
	}
	.bar span {
		display: block;
		height: 100%;
		background: var(--muted);
	}
	.bar span.high {
		background: var(--danger);
	}
	.muted {
		color: var(--faint);
	}
	.running {
		font: 11.5px var(--mono);
		color: var(--running);
	}
	.newproj {
		flex: none;
		border: 1px solid var(--line);
		border-radius: 7px;
		background: var(--card);
		color: var(--text);
		font: 12px var(--sans);
		padding: 4px 9px;
		cursor: pointer;
	}
	.newproj:hover {
		border-color: var(--accent);
		color: var(--accent);
	}
	.count.run {
		background: var(--accent-soft);
		color: var(--accent);
	}
	.modal.newp {
		display: grid;
		grid-template-columns: auto 1fr;
		gap: 8px 10px;
		align-items: center;
		min-width: min(560px, 92vw);
	}
	.modal.newp h2,
	.modal.newp .intro,
	.modal.newp .acts,
	.modal.newp label.wide {
		grid-column: 1 / -1;
	}
	.modal.newp .intro {
		margin: 0;
		font-size: 12px;
		color: var(--muted);
	}
	.modal.newp label {
		display: contents;
	}
	.modal.newp label span {
		font: 11.5px var(--mono);
		color: var(--faint);
	}
	.modal.newp label.wide {
		display: grid;
		grid-template-columns: auto 1fr;
		gap: 10px;
		align-items: start;
	}
	.modal.newp input,
	.modal.newp textarea {
		border: 1px solid var(--line);
		border-radius: 7px;
		background: var(--panel);
		color: var(--text);
		font: 13px var(--sans);
		padding: 5px 8px;
		width: 100%;
		resize: vertical;
	}
	.modal.newp input:focus,
	.modal.newp textarea:focus {
		outline: 1px solid var(--accent);
	}
	.modal.newp .acts {
		display: flex;
		align-items: center;
		gap: 8px;
		margin-top: 4px;
	}
	.modal.newp .acts .foot {
		margin: 0 auto 0 0;
		font: 11.5px var(--mono);
		color: var(--faint);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.working {
		font: 11.5px var(--mono);
		color: var(--accent);
	}
	.attn {
		border: none;
		background: none;
		padding: 0;
		font: inherit;
		color: var(--accent);
		cursor: pointer;
	}
	.attn:hover {
		text-decoration: underline;
	}
	.confirm {
		width: min(560px, 100%);
	}
	.confirm.danger h2 {
		color: var(--danger);
	}
	.confirm ul {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 4px;
		max-height: 50vh;
		overflow-y: auto;
	}
	.confirm li {
		display: flex;
		flex-direction: column;
		padding: 6px 10px;
		border-radius: 6px;
		background: var(--card);
	}
	.ltext {
		font: 13px var(--mono);
		overflow-wrap: anywhere;
	}
	.confirm li.danger .ltext {
		color: var(--danger);
	}
	.lsub {
		font-size: 12px;
		color: var(--muted);
	}
	.actions {
		display: flex;
		justify-content: flex-end;
		gap: 8px;
		margin-top: 16px;
	}
	.actions button {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 6px 14px;
		border: 1px solid var(--line);
		border-radius: 7px;
		background: var(--card);
		cursor: pointer;
		font-size: 13px;
	}
	.actions .go {
		border-color: var(--accent);
		color: var(--accent);
		font-weight: 600;
	}
	.confirm.danger .go {
		border-color: var(--danger);
		color: var(--danger);
	}
	.scan {
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.scan.busy {
		color: var(--accent);
	}
	.conn {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--danger);
	}
	.conn.up {
		background: var(--hot);
	}
	.banner {
		padding: 8px 24px;
		background: var(--danger-soft);
		color: var(--danger);
		font-size: 13px;
	}

	main {
		flex: 1;
		overflow-y: auto;
		padding: 8px 24px 24px;
	}
	section {
		margin-top: 18px;
	}
	h2 {
		display: flex;
		align-items: center;
		gap: 6px;
		margin: 0 0 10px;
		font-size: 12px;
		text-transform: uppercase;
		letter-spacing: 0.08em;
		color: var(--muted);
		font-weight: 600;
	}
	h2 span {
		color: var(--faint);
	}
	.stackbtn {
		margin-left: 8px;
		font-size: 11px;
		text-transform: none;
		letter-spacing: 0;
		padding: 1px 8px;
		border: 1px dashed var(--line);
		border-radius: 10px;
		background: none;
		color: var(--muted);
		cursor: pointer;
	}
	.stackbtn:hover {
		border-color: var(--running);
		color: var(--running);
	}
	.stackbtn.on {
		border-style: solid;
		color: var(--running);
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(270px, 1fr));
		gap: 12px;
	}
	.empty {
		color: var(--muted);
		text-align: center;
		margin-top: 80px;
	}

	.keys {
		display: flex;
		flex-wrap: wrap;
		gap: 4px 18px;
		padding: 8px 24px;
		font-size: 12px;
		color: var(--muted);
		background: color-mix(in srgb, var(--bg) 92%, transparent);
		border-top: 1px solid var(--line);
	}
	.keys span {
		display: flex;
		gap: 4px;
		align-items: center;
	}

	.overlay {
		position: fixed;
		inset: 0;
		background: rgb(0 0 0 / 0.35);
		display: grid;
		place-items: center;
		z-index: 20;
		padding: 16px;
	}
	.modal {
		background: var(--panel);
		border: 1px solid var(--line);
		border-radius: 12px;
		padding: 18px 24px;
		min-width: min(320px, 100%);
		max-width: 640px;
		max-height: 90vh;
		overflow-y: auto;
	}
	.modal h2 {
		margin-bottom: 14px;
	}
	.help dl {
		display: grid;
		grid-template-columns: auto 1fr;
		gap: 8px 16px;
		margin: 0;
	}
	.help dd {
		margin: 0;
	}
	.where {
		font-size: 11px;
		color: var(--faint);
		margin-left: 6px;
	}
	.picker ul {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.picker li button {
		width: 100%;
		display: grid;
		grid-template-columns: auto auto 1fr auto;
		gap: 10px;
		align-items: center;
		padding: 8px 10px;
		border: 1px solid transparent;
		border-radius: 7px;
		background: var(--card);
		text-align: left;
		cursor: pointer;
	}
	.picker li button.active {
		border-color: var(--accent);
	}
	.cname {
		font-weight: 600;
		font-size: 13.5px;
	}
	.picker li code {
		font: 12px var(--mono);
		color: var(--muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.on {
		font-size: 11px;
		color: var(--running);
	}
	.def {
		font-size: 11px;
		color: var(--faint);
	}
	.foot {
		margin: 14px 0 0;
		font-size: 12px;
		color: var(--muted);
	}
	.foot code {
		font-size: 11.5px;
		overflow-wrap: anywhere;
	}

	.toast {
		position: fixed;
		bottom: 52px;
		left: 50%;
		transform: translateX(-50%);
		padding: 8px 14px;
		border-radius: 8px;
		background: var(--text);
		color: var(--bg);
		font-size: 13px;
		z-index: 30;
		box-shadow: 0 4px 16px rgb(0 0 0 / 0.2);
		max-width: min(640px, calc(100vw - 32px));
	}
	.toast.error {
		background: var(--danger);
		color: #fff;
	}

	@media (max-width: 760px) {
		.top {
			flex-wrap: wrap;
			padding: 10px 16px;
		}
		.status {
			display: none;
		}
		main,
		.keys {
			padding-left: 16px;
			padding-right: 16px;
		}
	}
</style>
