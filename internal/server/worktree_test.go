package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"loods/internal/run"
	"loods/internal/xdg"
)

func gitInit(t *testing.T, dir string, commit bool) {
	t.Helper()
	args := [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
	}
	if commit {
		args = append(args, []string{"commit", "--allow-empty", "-m", "first"})
	}
	for _, a := range args {
		if out, err := exec.Command("git", append([]string{"-C", dir}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(a, " "), err, out)
		}
	}
}

func isolatedJob(id string) run.Job {
	return run.Job{
		ID: id, Role: id, Title: "do " + id, Prompt: "build " + id,
		Exclusive: "repo", Isolation: run.IsolationWorktree,
	}
}

// The whole feature, through the real scheduler: two jobs that both commit,
// both holding `exclusive: repo`, start at the same time because each gets a
// worktree of its own — and the session really runs in that worktree.
func TestServerStartsIsolatedJobsInTheirOwnWorktrees(t *testing.T) {
	s, it, runs := testServer(t, "sleep 5 #")
	gitInit(t, it.Path, true)
	r, err := runs.New(it.Rel, "a sailing game", 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Submit(r.ID, []run.Job{isolatedJob("docs"), isolatedJob("e2e")}); err != nil {
		t.Fatal(err)
	}

	s.tickRuns()
	got, _ := runs.Get(r.ID)
	for _, id := range []string{"docs", "e2e"} {
		i, _ := got.JobIndex(id)
		j := got.Jobs[i]
		if j.Status != run.JobRunning {
			t.Fatalf("job %s is %s (%s), want running alongside the other", id, j.Status, j.Note)
		}
		path := run.WorktreePath(xdg.WorktreesDir(), it.Rel, r.ID, id)
		out, err := exec.Command("git", "-C", path, "rev-parse", "--abbrev-ref", "HEAD").Output()
		if err != nil {
			t.Fatalf("job %s has no worktree at %s: %v", id, path, err)
		}
		if branch := strings.TrimSpace(string(out)); branch != run.WorktreeBranch(r.ID, id) {
			t.Errorf("job %s worktree is on %q, want %q", id, branch, run.WorktreeBranch(r.ID, id))
		}
		p, ok := s.procs.Find(j.ProcID)
		if !ok {
			t.Fatalf("job %s has no process", id)
		}
		if p.Dir != path {
			t.Errorf("job %s session runs in %q, want its worktree %q", id, p.Dir, path)
		}
	}
	// The project's own tree is where it was: nobody checked a branch out in it.
	out, _ := exec.Command("git", "-C", it.Path, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if branch := strings.TrimSpace(string(out)); branch != "main" {
		t.Errorf("the project tree moved to %q", branch)
	}
}

// A shared job and an isolated one both say `exclusive: repo`; only the shared
// one runs in the project folder, and it does not wait for the isolated one.
func TestServerSharedJobKeepsTheProjectFolder(t *testing.T) {
	s, it, runs := testServer(t, "sleep 5 #")
	gitInit(t, it.Path, true)
	r, _ := runs.New(it.Rel, "goal", 3)
	shared := run.Job{ID: "scaffold", Role: "scaffold", Title: "scaffold", Prompt: "scaffold it", Exclusive: "repo"}
	if _, err := runs.Submit(r.ID, []run.Job{shared, isolatedJob("docs")}); err != nil {
		t.Fatal(err)
	}

	s.tickRuns()
	got, _ := runs.Get(r.ID)
	i, _ := got.JobIndex("scaffold")
	k, _ := got.JobIndex("docs")
	if got.Jobs[i].Status != run.JobRunning || got.Jobs[k].Status != run.JobRunning {
		t.Fatalf("scaffold is %s and docs is %s; both should run", got.Jobs[i].Status, got.Jobs[k].Status)
	}
	p, _ := s.procs.Find(got.Jobs[i].ProcID)
	if p.Dir != it.Path {
		t.Errorf("the shared job runs in %q, want the project folder %q", p.Dir, it.Path)
	}
}

// An isolated job declared next to the job that creates the repository, instead
// of after it, cannot have a worktree. It has to fail with a note that says so,
// not start a session in a folder that is not there.
func TestServerFailsIsolatedJobWithNothingToBranchFrom(t *testing.T) {
	s, it, runs := testServer(t, "sleep 5 #")
	gitInit(t, it.Path, false) // a repo, but no commits
	r, _ := runs.New(it.Rel, "goal", 2)
	if _, err := runs.Submit(r.ID, []run.Job{isolatedJob("docs")}); err != nil {
		t.Fatal(err)
	}

	s.tickRuns()
	got, _ := runs.Get(r.ID)
	j := got.Jobs[0]
	if j.Status != run.JobFailed {
		t.Fatalf("job is %s, want failed", j.Status)
	}
	if !strings.Contains(j.Note, "no commits") {
		t.Errorf("note does not say why: %q", j.Note)
	}
}

// A job whose session died is started again over the worktree it left behind,
// with its commits still in it: that is why AddWorktree is safe to call twice.
func TestServerReusesAnExistingWorktree(t *testing.T) {
	s, it, runs := testServer(t, "sleep 5 #")
	gitInit(t, it.Path, true)
	r, _ := runs.New(it.Rel, "goal", 2)
	if _, err := runs.Submit(r.ID, []run.Job{isolatedJob("docs")}); err != nil {
		t.Fatal(err)
	}
	s.tickRuns()
	got := mustGet(t, runs, r.ID)
	path := run.WorktreePath(xdg.WorktreesDir(), it.Rel, r.ID, "docs")
	marker := filepath.Join(path, "kept.md")
	if err := os.WriteFile(marker, []byte("work done before the session died"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The session dies and loods reaps the job, exactly as it does when the
	// server is restarted under a running run.
	procID := got.Jobs[0].ProcID
	s.procs.Stop(procID)
	until(t, "the session to be gone", func() bool {
		p, ok := s.procs.Find(procID)
		return !ok || !p.Alive()
	})
	s.tickRuns()
	if j := mustGet(t, runs, r.ID).Jobs[0]; j.Status != run.JobFailed {
		t.Fatalf("job is %s, want failed after its session was stopped", j.Status)
	}

	// A repaired job with the same id starts again over the same worktree.
	if _, err := s.startJob(mustGet(t, runs, r.ID), isolatedJob("docs")); err != nil {
		t.Fatalf("starting again over the same worktree failed: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the work left in the worktree is gone: %v", err)
	}
}

func mustGet(t *testing.T, runs *run.Store, id string) run.Run {
	t.Helper()
	r, ok := runs.Get(id)
	if !ok {
		t.Fatalf("run %s is gone", id)
	}
	return r
}
