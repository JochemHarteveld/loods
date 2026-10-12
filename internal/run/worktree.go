package run

import (
	"path/filepath"
	"strings"
)

// Where an isolated job works. Both the branch and the folder are derived from
// the run and the job rather than recorded, so every part of loods — the
// scheduler that creates the worktree, the prompt that tells the agent where it
// is, `loods run status`, the board — names the same thing without a field that
// could go stale or disagree with what is on disk.

// WorktreeBranch is the branch an isolated job commits on. The run id is in it
// so two runs on one project cannot collide, and the job id is in it so the
// merge job can be told which branches to land without being handed a list.
func WorktreeBranch(runID, jobID string) string {
	return "loods/" + runID + "/" + jobID
}

// WorktreePath is the folder an isolated job works in, under the loods state
// directory. It deliberately lives outside the project: a worktree inside the
// project folder would be picked up as a project of its own by the scanner, and
// would turn up in the repo as an untracked folder nobody asked for.
func WorktreePath(worktreesDir, projectRel, runID, jobID string) string {
	return filepath.Join(worktreesDir, pathSlug(projectRel), runID+"-"+jobID)
}

// pathSlug flattens a project rel like "group/project" into one folder name.
func pathSlug(rel string) string {
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return "project"
	}
	return strings.ReplaceAll(rel, "/", "-")
}

// Isolated reports whether this job works in a worktree of its own.
func (j Job) Isolated() bool { return j.Isolation == IsolationWorktree }
