package main

import (
	"path/filepath"
	"testing"
	"time"
)

// A run end to end through the server, with a harmless command standing in for
// Claude: the scheduler starts what may run, the Manager really runs it, and
// the pass after a session ends turns it into a finished job.
func testServer(t *testing.T, agentCmd string) (*Server, *Item) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("LOODS_AGENT_COMMAND", agentCmd)
	s := newServer(dir, filepath.Join(dir, "archive"), 3, 0, filepath.Join(dir, "state", "procs.json"))
	it := &Item{Rel: "demo", Path: dir, Name: "demo", Kind: KindGit}
	s.mu.Lock()
	s.items = []*Item{it}
	s.mu.Unlock()
	t.Cleanup(func() { s.procs.StopAll(2 * time.Second) })
	return s, it
}

// until polls because the processes are real: `true` exits on its own time.
func until(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestServerRunsAGraph(t *testing.T) {
	s, it := testServer(t, "true")
	run, err := s.runs.New(it.Rel, "a house-hunting site", 2)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := readJobs(writeTemp(t, defaultGraph))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.runs.Submit(run.ID, jobs); err != nil {
		t.Fatal(err)
	}

	// First pass: repo and stories start (max-parallel 2), as real processes.
	s.tickRuns()
	got, _ := s.runs.Get(run.ID)
	for _, id := range []string{"repo", "stories"} {
		i, _ := got.job(id)
		if got.Jobs[i].Status != JobRunning || got.Jobs[i].ProcID == "" {
			t.Fatalf("job %s is %+v, want running with a process", id, got.Jobs[i])
		}
		if _, ok := s.procs.Find(got.Jobs[i].ProcID); !ok {
			t.Fatalf("job %s has no process %q", id, got.Jobs[i].ProcID)
		}
	}

	// `true` exits straight away and reports nothing, which is a failure: the
	// run cannot know what it built.
	until(t, "the sessions to end", func() bool {
		i, _ := got.job("repo")
		p, ok := s.procs.Find(got.Jobs[i].ProcID)
		return ok && !p.alive()
	})
	s.tickRuns()
	got, _ = s.runs.Get(run.ID)
	i, _ := got.job("repo")
	if got.Jobs[i].Status != JobFailed || got.Jobs[i].Note == "" {
		t.Fatalf("an ended session did not become a failed job: %+v", got.Jobs[i])
	}
	if j, _ := got.job("scaffold"); got.Jobs[j].Status != JobBlocked {
		t.Fatalf("scaffold is %s, want blocked after repo failed", got.Jobs[j].Status)
	}
	until(t, "the run to end", func() bool {
		s.tickRuns()
		r, _ := s.runs.Get(run.ID)
		return r.Status == RunFailed
	})
}

// A job that reports back the normal way (what an agent does) counts as done,
// and the next job starts.
func TestServerSchedulesAfterAJobReports(t *testing.T) {
	s, it := testServer(t, "sleep 5 #")
	run, err := s.runs.New(it.Rel, "goal", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.runs.Submit(run.ID, []Job{
		{ID: "stories", Role: "stories", Title: "t", Prompt: "p"},
		{ID: "tickets", Role: "tickets", Title: "t", Prompt: "p", Needs: []string{"stories"}},
	}); err != nil {
		t.Fatal(err)
	}
	s.tickRuns()
	got, _ := s.runs.Get(run.ID)
	if i, _ := got.job("tickets"); got.Jobs[i].Status != JobPending {
		t.Fatalf("tickets is %s before stories reported", got.Jobs[i].Status)
	}
	if _, err := s.runs.Finish(run.ID, "stories", JobDone, "nine stories"); err != nil {
		t.Fatal(err)
	}
	s.tickRuns()
	got, _ = s.runs.Get(run.ID)
	i, _ := got.job("tickets")
	if got.Jobs[i].Status != JobRunning {
		t.Fatalf("tickets is %s after stories was done", got.Jobs[i].Status)
	}

	// Cancelling stops the sessions, not just the bookkeeping.
	proc := got.Jobs[i].ProcID
	if _, err := s.runs.Cancel(run.ID, "the user changed direction"); err != nil {
		t.Fatal(err)
	}
	s.stopCancelled()
	until(t, "the cancelled session to stop", func() bool {
		p, ok := s.procs.Find(proc)
		return ok && !p.alive()
	})
}

// An unstartable job fails with a reason instead of stalling the run.
func TestServerFailsJobsItCannotStart(t *testing.T) {
	s, it := testServer(t, "definitely-not-a-program-"+t.Name())
	run, _ := s.runs.New(it.Rel, "goal", 2)
	if _, err := s.runs.Submit(run.ID, []Job{{ID: "repo", Role: "git", Title: "t", Prompt: "p"}}); err != nil {
		t.Fatal(err)
	}
	s.tickRuns()
	got, _ := s.runs.Get(run.ID)
	if got.Jobs[0].Status != JobFailed || got.Status != RunFailed {
		t.Fatalf("job %s, run %s; want both failed", got.Jobs[0].Status, got.Status)
	}
}
