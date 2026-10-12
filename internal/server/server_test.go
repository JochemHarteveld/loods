package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"loods/internal/agent"
	"loods/internal/project"
	"loods/internal/run"
)

func TestGuard(t *testing.T) {
	s := New(Options{Root: t.TempDir(), Archive: t.TempDir(), Depth: 1, Port: 7777,
		StateFile: filepath.Join(t.TempDir(), "procs.json")})
	h := s.Handler()
	cases := []struct {
		name, method, host, header string
		want                       int
	}{
		{"ping", "GET", "127.0.0.1:7777", "", http.StatusOK},
		{"localhost ok", "GET", "localhost:7777", "", http.StatusOK},
		{"rebinding host", "GET", "evil.example:7777", "", http.StatusForbidden},
		{"post without header", "POST", "127.0.0.1:7777", "", http.StatusForbidden},
		{"post with header", "POST", "127.0.0.1:7777", "1", http.StatusAccepted},
	}
	for _, c := range cases {
		path := "/api/ping"
		if c.method == "POST" {
			path = "/api/rescan"
		}
		req := httptest.NewRequest(c.method, path, nil)
		req.Host = c.host
		if c.header != "" {
			req.Header.Set("X-Loods", c.header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: got %d, want %d", c.name, rec.Code, c.want)
		}
	}
}

// A run end to end through the server, with a harmless command standing in for
// Claude: the scheduler starts what may run, the process manager really runs
// it, and the pass after a session ends turns it into a finished job.
//
// The run store is handed in as the concrete one, because the test plays the
// orchestrator too: submitting a graph is the CLI's half of the conversation.
func testServer(t *testing.T, agentCmd string) (*Server, *project.Item, *run.Store) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("LOODS_AGENT_COMMAND", agentCmd)
	runs := run.NewStore(filepath.Join(dir, "state", "runs.json"))
	s := New(Options{
		Root: dir, Archive: filepath.Join(dir, "archive"), Depth: 3,
		StateFile: filepath.Join(dir, "state", "procs.json"),
		Runs:      runs,
	})
	it := &project.Item{Rel: "demo", Path: dir, Name: "demo", Kind: project.KindGit}
	s.mu.Lock()
	s.items = []*project.Item{it}
	s.mu.Unlock()
	t.Cleanup(func() { s.procs.StopAll(2 * time.Second) })
	return s, it, runs
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
	s, it, runs := testServer(t, "true")
	r, err := runs.New(it.Rel, "a house-hunting site", 2)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := run.ParseJobs([]byte(agent.DefaultGraph))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Submit(r.ID, jobs); err != nil {
		t.Fatal(err)
	}

	// First pass: repo and stories start (max-parallel 2), as real processes.
	s.tickRuns()
	got, _ := runs.Get(r.ID)
	for _, id := range []string{"repo", "stories"} {
		i, _ := got.JobIndex(id)
		if got.Jobs[i].Status != run.JobRunning || got.Jobs[i].ProcID == "" {
			t.Fatalf("job %s is %+v, want running with a process", id, got.Jobs[i])
		}
		if _, ok := s.procs.Find(got.Jobs[i].ProcID); !ok {
			t.Fatalf("job %s has no process %q", id, got.Jobs[i].ProcID)
		}
	}

	// `true` exits straight away and reports nothing, which is a failure: the
	// run cannot know what it built.
	until(t, "the sessions to end", func() bool {
		i, _ := got.JobIndex("repo")
		p, ok := s.procs.Find(got.Jobs[i].ProcID)
		return ok && !p.Alive()
	})
	s.tickRuns()
	got, _ = runs.Get(r.ID)
	i, _ := got.JobIndex("repo")
	if got.Jobs[i].Status != run.JobFailed || got.Jobs[i].Note == "" {
		t.Fatalf("an ended session did not become a failed job: %+v", got.Jobs[i])
	}
	if j, _ := got.JobIndex("scaffold"); got.Jobs[j].Status != run.JobBlocked {
		t.Fatalf("scaffold is %s, want blocked after repo failed", got.Jobs[j].Status)
	}
	until(t, "the run to end", func() bool {
		s.tickRuns()
		cur, _ := runs.Get(r.ID)
		return cur.Status == run.Failed
	})
}

// A job that reports back the normal way (what an agent does) counts as done,
// and the next job starts.
func TestServerSchedulesAfterAJobReports(t *testing.T) {
	s, it, runs := testServer(t, "sleep 5 #")
	r, err := runs.New(it.Rel, "goal", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Submit(r.ID, []run.Job{
		{ID: "stories", Role: "stories", Title: "t", Prompt: "p"},
		{ID: "tickets", Role: "tickets", Title: "t", Prompt: "p", Needs: []string{"stories"}},
	}); err != nil {
		t.Fatal(err)
	}
	s.tickRuns()
	got, _ := runs.Get(r.ID)
	if i, _ := got.JobIndex("tickets"); got.Jobs[i].Status != run.JobPending {
		t.Fatalf("tickets is %s before stories reported", got.Jobs[i].Status)
	}
	if _, err := runs.Finish(r.ID, "stories", run.JobDone, "nine stories"); err != nil {
		t.Fatal(err)
	}
	s.tickRuns()
	got, _ = runs.Get(r.ID)
	i, _ := got.JobIndex("tickets")
	if got.Jobs[i].Status != run.JobRunning {
		t.Fatalf("tickets is %s after stories was done", got.Jobs[i].Status)
	}

	// Cancelling stops the sessions, not just the bookkeeping.
	procID := got.Jobs[i].ProcID
	if _, err := runs.Cancel(r.ID, "the user changed direction"); err != nil {
		t.Fatal(err)
	}
	s.stopCancelled()
	until(t, "the cancelled session to stop", func() bool {
		p, ok := s.procs.Find(procID)
		return ok && !p.Alive()
	})
}

// An unstartable job fails with a reason instead of stalling the run.
func TestServerFailsJobsItCannotStart(t *testing.T) {
	s, it, runs := testServer(t, "definitely-not-a-program-"+t.Name())
	r, _ := runs.New(it.Rel, "goal", 2)
	if _, err := runs.Submit(r.ID, []run.Job{{ID: "repo", Role: "git", Title: "t", Prompt: "p"}}); err != nil {
		t.Fatal(err)
	}
	s.tickRuns()
	got, _ := runs.Get(r.ID)
	if got.Jobs[0].Status != run.JobFailed || got.Status != run.Failed {
		t.Fatalf("job %s, run %s; want both failed", got.Jobs[0].Status, got.Status)
	}
}
