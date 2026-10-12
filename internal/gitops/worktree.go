package gitops

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// AddWorktree makes sure path is a git worktree of repo on branch, and reports
// whether it had to create it. It is written to be safe to call again: a job
// that is restarted finds the tree it left behind, with its commits and its
// node_modules, instead of failing or starting over.
//
// Three cases, in order:
//   - the folder is already a worktree on that branch: nothing to do;
//   - the branch exists (an earlier attempt pushed it): check it out;
//   - neither exists: branch off the repo's current HEAD.
func AddWorktree(repo, path, branch string) (created bool, err error) {
	if err := worktreeable(repo); err != nil {
		return false, err
	}
	if ok, err := isWorktreeOn(path, branch); err != nil {
		return false, err
	} else if ok {
		return false, nil
	}
	if _, err := os.Stat(path); err == nil {
		return false, fmt.Errorf("%s is in the way: it exists but is not a worktree on %s", path, branch)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	// A stale administrative entry for a folder someone deleted by hand would
	// make `worktree add` refuse the path; pruning first costs nothing.
	exec.Command("git", "-C", repo, "worktree", "prune").Run()

	args := []string{"-C", repo, "worktree", "add"}
	if branchExists(repo, branch) {
		args = append(args, path, branch)
	} else {
		args = append(args, "-b", branch, path)
	}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return false, fmt.Errorf("git worktree add %s: %s", branch, firstLine(out))
	}
	return true, nil
}

// RemoveWorktree takes a worktree away again. Work that was never merged is
// work that is lost, so this is for cleaning up after a merge, not for tidying
// up after a job: nothing in loods calls it on its own.
func RemoveWorktree(repo, path string) error {
	if out, err := exec.Command("git", "-C", repo, "worktree", "remove", "--force", path).CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree remove %s: %s", path, firstLine(out))
	}
	return nil
}

// worktreeable explains, in the terms the orchestrator reads in the job's note,
// why a repo cannot have a worktree yet. The unborn-HEAD case is the one that
// actually happens: a graph whose first job creates the repository, with an
// isolated job declared next to it instead of after it.
func worktreeable(repo string) error {
	if out, err := exec.Command("git", "-C", repo, "rev-parse", "--is-inside-work-tree").CombinedOutput(); err != nil {
		return fmt.Errorf("%s is not a git repository, so it cannot have a worktree: %s", repo, firstLine(out))
	}
	if err := exec.Command("git", "-C", repo, "rev-parse", "--verify", "HEAD").Run(); err != nil {
		return fmt.Errorf("%s has no commits yet, so there is nothing to branch a worktree from: "+
			"let this job need the one that makes the first commit", repo)
	}
	return nil
}

// isWorktreeOn reports whether path is already a worktree checked out on
// branch. It asks the folder itself rather than parsing the repo's worktree
// list, so a worktree someone moved or made by hand answers too.
func isWorktreeOn(path, branch string) (bool, error) {
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		return false, nil
	}
	out, err := exec.Command("git", "-C", path, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return false, nil
	}
	return strings.TrimSpace(string(out)) == branch, nil
}

func branchExists(repo, branch string) bool {
	return exec.Command("git", "-C", repo, "rev-parse", "--verify", "refs/heads/"+branch).Run() == nil
}

// firstLine keeps a git failure to the line that says what went wrong: these
// end up in a job note, which is one line on the board.
func firstLine(out []byte) string {
	s := strings.TrimSpace(string(out))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
