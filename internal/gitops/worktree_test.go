package gitops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoWithCommit is a real repository with one commit: worktrees are git's
// behaviour, not ours, so these tests use git rather than a fake.
func repoWithCommit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"commit", "--allow-empty", "-m", "first"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	return dir
}

func headOf(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse in %s: %v", dir, err)
	}
	return strings.TrimSpace(string(out))
}

func TestAddWorktreeCreatesABranchOfItsOwn(t *testing.T) {
	repo := repoWithCommit(t)
	path := filepath.Join(t.TempDir(), "trees", "74ce78-docs")

	created, err := AddWorktree(repo, path, "loods/74ce78/docs")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("reported that it created nothing")
	}
	if got := headOf(t, path); got != "loods/74ce78/docs" {
		t.Errorf("worktree is on %q", got)
	}
	// The project's own tree is untouched: that is the whole promise.
	if got := headOf(t, repo); got != "main" {
		t.Errorf("the project tree moved to %q", got)
	}
}

// A job that is restarted has to find its work, not lose it: calling again is
// a no-op, and a commit made in the worktree survives.
func TestAddWorktreeIsSafeToCallAgain(t *testing.T) {
	repo := repoWithCommit(t)
	path := filepath.Join(t.TempDir(), "docs")
	if _, err := AddWorktree(repo, path, "loods/r1/docs"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "physics.md"), []byte("lift and drag"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.email=t@e.com", "-c", "user.name=t", "commit", "-m", "docs"}} {
		if out, err := exec.Command("git", append([]string{"-C", path}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}

	created, err := AddWorktree(repo, path, "loods/r1/docs")
	if err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	if created {
		t.Error("created the worktree a second time")
	}
	if _, err := os.Stat(filepath.Join(path, "physics.md")); err != nil {
		t.Errorf("the work in the worktree is gone: %v", err)
	}
}

// An empty repository cannot be branched from, and the message has to say what
// to do about it: it lands in the job's note as the reason it failed.
func TestAddWorktreeRefusesARepoWithNoCommits(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s", out)
	}
	_, err := AddWorktree(dir, filepath.Join(t.TempDir(), "wt"), "loods/r1/docs")
	if err == nil {
		t.Fatal("a repo with no commits was accepted")
	}
	if !strings.Contains(err.Error(), "no commits") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestAddWorktreeRefusesSomethingThatIsNotARepo(t *testing.T) {
	if _, err := AddWorktree(t.TempDir(), filepath.Join(t.TempDir(), "wt"), "loods/r1/docs"); err == nil {
		t.Fatal("a plain folder was accepted as a repository")
	}
}

// A folder already holding something else must not be swallowed.
func TestAddWorktreeRefusesAnOccupiedPath(t *testing.T) {
	repo := repoWithCommit(t)
	path := filepath.Join(t.TempDir(), "taken")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := AddWorktree(repo, path, "loods/r1/docs"); err == nil {
		t.Fatal("an occupied folder was used as a worktree")
	}
}

// Picking up a branch an earlier attempt already pushed, rather than failing
// because it exists.
func TestAddWorktreeChecksOutAnExistingBranch(t *testing.T) {
	repo := repoWithCommit(t)
	if out, err := exec.Command("git", "-C", repo, "branch", "loods/r1/docs").CombinedOutput(); err != nil {
		t.Fatalf("git branch: %s", out)
	}
	path := filepath.Join(t.TempDir(), "docs")
	if _, err := AddWorktree(repo, path, "loods/r1/docs"); err != nil {
		t.Fatal(err)
	}
	if got := headOf(t, path); got != "loods/r1/docs" {
		t.Errorf("worktree is on %q", got)
	}
}

func TestRemoveWorktree(t *testing.T) {
	repo := repoWithCommit(t)
	path := filepath.Join(t.TempDir(), "docs")
	if _, err := AddWorktree(repo, path, "loods/r1/docs"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveWorktree(repo, path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("worktree folder is still there: %v", err)
	}
}
