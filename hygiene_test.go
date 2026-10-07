package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// newRepo makes a repo with a main branch and one commit.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	os.MkdirAll(dir, 0o755)
	run(t, dir, "git", "init", "-q", "-b", "main")
	run(t, dir, "git", "config", "user.email", "t@example.com")
	run(t, dir, "git", "config", "user.name", "t")
	commit(t, dir, "README", "hi")
	return dir
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, file, body string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(filepath.Join(dir, file)), 0o755)
	os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644)
	run(t, dir, "git", "add", "-A")
	run(t, dir, "git", "commit", "-q", "-m", file)
}

func TestHygieneEnvAndBranches(t *testing.T) {
	dir := newRepo(t)
	commit(t, dir, "app/.env", "SECRET=1")                         // committed secret
	commit(t, dir, ".env.example", "SECRET=")                      // template: fine
	commit(t, dir, ".gitignore", "web/.env.local\n")
	os.WriteFile(filepath.Join(dir, ".env"), []byte("X=1"), 0o644) // on disk, not ignored
	os.MkdirAll(filepath.Join(dir, "web"), 0o755)
	os.WriteFile(filepath.Join(dir, "web", ".env.local"), []byte("X=1"), 0o644)
	os.MkdirAll(filepath.Join(dir, "node_modules", "x"), 0o755)
	os.WriteFile(filepath.Join(dir, "node_modules", "x", ".env"), []byte("X=1"), 0o644)

	run(t, dir, "git", "branch", "done") // merged (same commit as main)
	run(t, dir, "git", "switch", "-q", "-c", "wip")
	os.WriteFile(filepath.Join(dir, "wip.txt"), []byte("x"), 0o644)
	run(t, dir, "git", "add", "wip.txt")
	run(t, dir, "git", "commit", "-q", "-m", "wip")
	run(t, dir, "git", "switch", "-q", "main")

	g := readGit(dir)
	h := g.hy
	if !slices.Equal(h.envTracked, []string{"app/.env"}) {
		t.Errorf("tracked = %q", h.envTracked)
	}
	if !slices.Equal(h.envUnignored, []string{".env"}) {
		t.Errorf("unignored = %q", h.envUnignored)
	}
	if !slices.Equal(h.merged, []string{"done"}) {
		t.Errorf("merged = %q (wip is not merged, main is current)", h.merged)
	}

	it := &Item{Kind: KindGit, Dirty: 1, HasRemote: true}
	var kinds []string
	for _, w := range it.warnings(h, time.Now()) {
		kinds = append(kinds, w.Kind)
	}
	if !slices.Equal(kinds, []string{"env-tracked", "env-unignored", "merged-branches"}) {
		t.Errorf("warnings = %q", kinds)
	}

	results, err := ignoreEnv(dir)
	if err != nil || len(results) != 1 {
		t.Fatal(results, err)
	}
	if h := readGit(dir).hy; len(h.envUnignored) != 0 {
		t.Errorf("still unignored after ignoreEnv: %q", h.envUnignored)
	}
	gi, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.HasSuffix(string(gi), "web/.env.local\n\n# secrets (added by loods)\n/.env\n") {
		t.Errorf(".gitignore = %q", gi)
	}
}

func TestDeleteBranchesAndUndo(t *testing.T) {
	dir := newRepo(t)
	run(t, dir, "git", "branch", "old")
	run(t, dir, "git", "branch", "feature")
	wt := filepath.Join(filepath.Dir(dir), "wt-feature")
	run(t, dir, "git", "worktree", "add", "-q", wt, "feature")
	run(t, dir, "git", "branch", "busy")
	busyWT := filepath.Join(filepath.Dir(dir), "wt-busy")
	run(t, dir, "git", "worktree", "add", "-q", busyWT, "busy")
	os.WriteFile(filepath.Join(busyWT, "scratch.txt"), []byte("unsaved"), 0o644)
	featureSHA := run(t, dir, "git", "rev-parse", "feature")
	run(t, dir, "git", "branch", "lost")
	lostWT := filepath.Join(filepath.Dir(dir), "wt-lost")
	run(t, dir, "git", "worktree", "add", "-q", lostWT, "lost")
	os.RemoveAll(lostWT) // folder deleted by hand: worktree is prunable

	l := &gitLog{path: filepath.Join(t.TempDir(), "git.log")}
	res := l.deleteBranches(dir, []string{"old", "feature", "busy", "main", "nope", "lost"})
	ok := map[string]bool{}
	for _, r := range res {
		ok[r.Name] = r.OK
	}
	if !ok["old"] || !ok["feature"] || !ok["lost"] || ok["busy"] || ok["main"] || ok["nope"] {
		t.Fatalf("results = %+v", res)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Error("clean worktree of a deleted branch should be removed")
	}
	if _, err := os.Stat(filepath.Join(busyWT, "scratch.txt")); err != nil {
		t.Error("dirty worktree must be kept")
	}

	info, _ := l.lastBatch(dir)
	if info == nil || len(info.Branches) != 3 || info.Worktrees != 1 {
		t.Fatalf("undo info = %+v", info)
	}
	res, err := l.undo(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if !r.OK {
			t.Errorf("undo %s: %s", r.Name, r.Error)
		}
	}
	if got := run(t, dir, "git", "rev-parse", "feature"); got != featureSHA {
		t.Errorf("feature restored at %s, want %s", got, featureSHA)
	}
	if _, err := os.Stat(filepath.Join(wt, "README")); err != nil {
		t.Error("worktree not restored")
	}
	if info, _ := l.lastBatch(dir); info != nil {
		t.Errorf("batch still undoable after undo: %+v", info)
	}
}

func TestCheckStateAndRepo(t *testing.T) {
	for _, c := range []struct{ status, conclusion, state, want string }{
		{"COMPLETED", "SUCCESS", "", "pass"},
		{"COMPLETED", "FAILURE", "", "fail"},
		{"IN_PROGRESS", "", "", "pending"},
		{"", "", "PENDING", "pending"},
		{"", "", "SUCCESS", "pass"},
		{"", "", "ERROR", "fail"},
	} {
		if got := checkState(c.status, c.conclusion, c.state); got != c.want {
			t.Errorf("checkState(%q,%q,%q) = %s, want %s", c.status, c.conclusion, c.state, got, c.want)
		}
	}
	if r := githubRepo("https://github.com/TranspaClean/transpakit"); r != "TranspaClean/transpakit" {
		t.Error(r)
	}
	if r := githubRepo("https://gitlab.com/a/b"); r != "" {
		t.Error(r)
	}
}
