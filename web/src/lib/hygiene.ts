import { runState, type GitHubInfo, type Project, type Warning } from './api';

export type Finding = Warning & { url?: string };

const rank = { danger: 0, warn: 1, info: 2 };
export const byLevel = (a: Warning, b: Warning) => rank[a.level] - rank[b.level];

/** Server warnings plus what GitHub says: failing CI and PR checks. */
export function findings(p: Project, gh: GitHubInfo | undefined): Finding[] {
	const out: Finding[] = [...(p.warnings ?? [])];
	if (gh?.ci && runState(gh.ci) === 'fail') {
		out.push({ level: 'warn', kind: 'ci-failing', text: `CI failing on ${gh.ci.branch}: ${gh.ci.workflow}`, url: gh.ci.url });
	}
	for (const pr of gh?.prs ?? []) {
		if (pr.checks === 'fail') out.push({ level: 'warn', kind: 'pr-failing', text: `PR #${pr.number} checks failing: ${pr.title}`, url: pr.url });
	}
	return out.sort(byLevel);
}

export const fixLabel: Record<string, string> = {
	'cleanup-branches': 'clean up',
	'prune-worktrees': 'prune',
	'ignore-env': 'add to .gitignore'
};

/** What to do about findings loods cannot fix itself. */
export const hints: Record<string, string> = {
	'env-tracked': 'git rm --cached it, add it to .gitignore, and rotate the secrets in it',
	'stale-dirty': 'commit, stash or throw away',
	'no-remote': 'push it to GitHub, or archive it',
	'stale-unpushed': 'push',
	behind: 'pull',
	'ci-failing': 'open the run',
	'pr-failing': 'open the PR'
};
