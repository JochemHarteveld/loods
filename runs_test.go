package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func runStore(t *testing.T) *RunStore {
	t.Helper()
	return newRunStore(filepath.Join(t.TempDir(), "runs.json"))
}

func job(id string, needs ...string) Job {
	return Job{ID: id, Role: id, Title: "do " + id, Prompt: "build " + id, Needs: needs}
}

// started is what a fake scheduler pass recorded, in the order it started things.
type started struct {
	ids  []string
	fail map[string]bool
}

func (s *started) start(r AgentRun, j Job) (string, error) {
	if s.fail[j.ID] {
		return "", errors.New("claude is not on PATH")
	}
	s.ids = append(s.ids, j.ID)
	return fmt.Sprintf("%s#job:%s:%s", r.Project, r.ID, j.ID), nil
}

// submitted is the shape every test needs: a run with a graph, ready to tick.
func submitted(t *testing.T, rs *RunStore, maxPar int, jobs ...Job) AgentRun {
	t.Helper()
	r, err := rs.New("g/p", "a house-hunting site", maxPar)
	if err != nil {
		t.Fatal(err)
	}
	r, err = rs.Submit(r.ID, jobs)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != RunRunning {
		t.Fatalf("submitted run is %s, want running", r.Status)
	}
	return r
}

func statuses(r AgentRun) map[string]string {
	out := map[string]string{}
	for _, j := range r.Jobs {
		out[j.ID] = j.Status
	}
	return out
}

func want(t *testing.T, rs *RunStore, id string, states map[string]string) {
	t.Helper()
	r, ok := rs.Get(id)
	if !ok {
		t.Fatalf("run %s is gone", id)
	}
	got := statuses(r)
	for job, state := range states {
		if got[job] != state {
			t.Errorf("job %s is %q, want %q (all: %v)", job, got[job], state, got)
		}
	}
}

func TestRunNewAndSubmit(t *testing.T) {
	rs := runStore(t)
	r := submitted(t, rs, 3, job("repo"), job("stories"), job("tickets", "stories"))

	// A second store (the server reading what the CLI wrote) sees the same run.
	other := newRunStore(rs.path)
	other.Refresh()
	got, ok := other.Get(r.ID)
	if !ok || got.Goal != "a house-hunting site" || len(got.Jobs) != 3 {
		t.Fatalf("read back %+v (ok %v)", got, ok)
	}
	if list := other.Of("g/p"); len(list) != 1 || list[0].ID != r.ID {
		t.Fatalf("Of = %+v", list)
	}
	if list := other.Live(); len(list) != 1 {
		t.Fatalf("Live = %+v", list)
	}
	if _, err := rs.Submit(r.ID, []Job{job("again")}); err == nil {
		t.Error("submitting twice was allowed")
	}
}

func TestRunNewRejectsBadInput(t *testing.T) {
	rs := runStore(t)
	if _, err := rs.New("g/p", "   ", 0); err == nil {
		t.Error("a run without a goal was allowed")
	}
	if _, err := rs.New("g/p", strings.Repeat("x", maxGoalLen+1), 0); err == nil {
		t.Error("an endless goal was allowed")
	}
	if _, err := rs.New("g/p", "ok", hardMaxPar+1); err == nil {
		t.Error("max-parallel above the hard limit was allowed")
	}
	r, err := rs.New("g/p", "ok", 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.MaxPar != defaultMaxPar || r.Status != RunPlanning {
		t.Fatalf("defaults wrong: %+v", r)
	}
}

func TestRunValidate(t *testing.T) {
	cases := []struct {
		name string
		jobs []Job
		says string
	}{
		{"no jobs", nil, "at least one job"},
		{"dangling need", []Job{job("tickets", "stories")}, "not a job of this run"},
		{"duplicate id", []Job{job("repo"), job("repo")}, "two jobs are called"},
		{"needs itself", []Job{job("repo", "repo")}, "needs itself"},
		{"cycle", []Job{job("a", "b"), job("b", "c"), job("c", "a")}, "wait for each other"},
		{"bad id", []Job{job("Repo Setup")}, "lowercase letters"},
		{"no title", []Job{{ID: "repo", Prompt: "x"}}, "no title"},
		{"no prompt", []Job{{ID: "repo", Title: "x"}}, "no prompt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := runStore(t)
			r, err := rs.New("g/p", "goal", 3)
			if err != nil {
				t.Fatal(err)
			}
			_, err = rs.Submit(r.ID, c.jobs)
			if err == nil || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("Submit error = %v, want it to mention %q", err, c.says)
			}
			// Nothing landed: a refused graph leaves the run planning.
			if again, _ := rs.Get(r.ID); again.Status != RunPlanning || len(again.Jobs) != 0 {
				t.Fatalf("a refused graph changed the run: %+v", again)
			}
		})
	}
}

// The wave the whole feature exists for: repo and stories start together,
// tickets waits for stories, scaffold waits for repo.
func TestTickRunsIndependentJobsTogether(t *testing.T) {
	rs := runStore(t)
	r := submitted(t, rs, 3, job("repo"), job("stories"), job("tickets", "stories"), job("scaffold", "repo"))

	var s started
	rs.Tick(s.start)
	if len(s.ids) != 2 || s.ids[0] != "repo" || s.ids[1] != "stories" {
		t.Fatalf("first pass started %v, want repo and stories", s.ids)
	}
	want(t, rs, r.ID, map[string]string{"repo": JobRunning, "stories": JobRunning, "tickets": JobPending, "scaffold": JobPending})

	// Running jobs are not started again, and nothing new is ready yet.
	s.ids = nil
	rs.Tick(s.start)
	if len(s.ids) != 0 {
		t.Fatalf("second pass started %v, want nothing", s.ids)
	}

	if _, err := rs.Finish(r.ID, "stories", JobDone, "wrote docs/userstories.md"); err != nil {
		t.Fatal(err)
	}
	s.ids = nil
	rs.Tick(s.start)
	if len(s.ids) != 1 || s.ids[0] != "tickets" {
		t.Fatalf("after stories: started %v, want tickets", s.ids)
	}
	got, _ := rs.Get(r.ID)
	i, _ := got.job("stories")
	if got.Jobs[i].Note != "wrote docs/userstories.md" || got.Jobs[i].EndedAt.IsZero() {
		t.Errorf("finished job did not keep its note: %+v", got.Jobs[i])
	}
	if i, _ := got.job("tickets"); got.Jobs[i].ProcID != fmt.Sprintf("g/p#job:%s:tickets", r.ID) {
		t.Errorf("job did not keep its process id: %+v", got.Jobs[i])
	}
}

func TestTickHonoursMaxParallel(t *testing.T) {
	rs := runStore(t)
	r := submitted(t, rs, 2, job("a"), job("b"), job("c"))

	var s started
	rs.Tick(s.start)
	if len(s.ids) != 2 {
		t.Fatalf("started %v with max-parallel 2", s.ids)
	}
	if _, err := rs.Finish(r.ID, s.ids[0], JobDone, "done it"); err != nil {
		t.Fatal(err)
	}
	s.ids = nil
	rs.Tick(s.start)
	if len(s.ids) != 1 || s.ids[0] != "c" {
		t.Fatalf("a slot came free: started %v, want c", s.ids)
	}
}

// Two jobs that commit in one working tree never run at the same time, however
// free their dependencies are.
func TestTickKeepsExclusiveJobsApart(t *testing.T) {
	rs := runStore(t)
	repo, scaffold := job("repo"), job("scaffold")
	repo.Exclusive, scaffold.Exclusive = "repo", "repo"
	r := submitted(t, rs, 3, repo, scaffold, job("stories"))

	var s started
	rs.Tick(s.start)
	if len(s.ids) != 2 || s.ids[0] != "repo" || s.ids[1] != "stories" {
		t.Fatalf("started %v, want repo and stories (not scaffold)", s.ids)
	}
	if _, err := rs.Finish(r.ID, "repo", JobDone, "pushed to github"); err != nil {
		t.Fatal(err)
	}
	s.ids = nil
	rs.Tick(s.start)
	if len(s.ids) != 1 || s.ids[0] != "scaffold" {
		t.Fatalf("after repo: started %v, want scaffold", s.ids)
	}
}

// The same lock also holds across two runs on one project, and not across
// projects.
func TestTickExclusiveAcrossRuns(t *testing.T) {
	rs := runStore(t)
	mk := func(project string) AgentRun {
		r, err := rs.New(project, "goal", 3)
		if err != nil {
			t.Fatal(err)
		}
		j := job("repo")
		j.Exclusive = "repo"
		if r, err = rs.Submit(r.ID, []Job{j}); err != nil {
			t.Fatal(err)
		}
		return r
	}
	first, second, elsewhere := mk("g/p"), mk("g/p"), mk("g/other")

	var s started
	rs.Tick(s.start)
	if len(s.ids) != 2 {
		t.Fatalf("started %v, want one repo job per project", s.ids)
	}
	one, _ := rs.Get(first.ID)
	two, _ := rs.Get(second.ID)
	other, _ := rs.Get(elsewhere.ID)
	if one.Jobs[0].Status == two.Jobs[0].Status {
		t.Errorf("both runs on g/p are %s; they should take turns", one.Jobs[0].Status)
	}
	if other.Jobs[0].Status != JobRunning {
		t.Errorf("the run on another project waited: %s", other.Jobs[0].Status)
	}
}

func TestFailureBlocksWhatNeededIt(t *testing.T) {
	rs := runStore(t)
	r := submitted(t, rs, 3, job("stories"), job("tickets", "stories"), job("review", "tickets"), job("repo"))

	var s started
	rs.Tick(s.start)
	if _, err := rs.Finish(r.ID, "stories", JobFailed, "no idea what the user wants"); err != nil {
		t.Fatal(err)
	}
	// Blocking is transitive: review needed tickets, which never ran.
	want(t, rs, r.ID, map[string]string{"stories": JobFailed, "tickets": JobBlocked, "review": JobBlocked, "repo": JobRunning})

	got, _ := rs.Get(r.ID)
	if got.Status != RunRunning {
		t.Errorf("run is %s while repo is still going", got.Status)
	}
	s.ids = nil
	rs.Tick(s.start)
	if len(s.ids) != 0 {
		t.Fatalf("started %v after the graph was blocked", s.ids)
	}

	if _, err := rs.Finish(r.ID, "repo", JobDone, "pushed"); err != nil {
		t.Fatal(err)
	}
	got, _ = rs.Get(r.ID)
	if got.Status != RunFailed || got.EndedAt.IsZero() {
		t.Fatalf("run ended as %s, want failed with a time", got.Status)
	}
	if _, err := rs.Finish(r.ID, "repo", JobDone, "again"); err == nil {
		t.Error("a finished job was finished twice")
	}
}

func TestRunFinishesWhenEveryJobIsDone(t *testing.T) {
	rs := runStore(t)
	r := submitted(t, rs, 3, job("a"), job("b", "a"))

	var s started
	rs.Tick(s.start)
	rs.Finish(r.ID, "a", JobDone, "one")
	rs.Tick(s.start)
	got, _ := rs.Get(r.ID)
	if got.Status != RunRunning {
		t.Fatalf("run is %s with b still running", got.Status)
	}
	rs.Finish(r.ID, "b", JobDone, "two")
	got, _ = rs.Get(r.ID)
	if got.Status != RunDone || got.EndedAt.IsZero() {
		t.Fatalf("run ended as %s, want done", got.Status)
	}
	if len(rs.Live()) != 0 {
		t.Error("a finished run is still live")
	}
	// Nothing more can be added to it.
	if _, err := rs.Extend(r.ID, []Job{job("c")}); err == nil {
		t.Error("a finished run was extended")
	}
}

// A job that cannot even be started (no claude on PATH) is a failure, not a job
// that stays pending and quietly stalls the run.
func TestTickMarksUnstartableJobsFailed(t *testing.T) {
	rs := runStore(t)
	r := submitted(t, rs, 3, job("a"), job("b", "a"))

	s := started{fail: map[string]bool{"a": true}}
	rs.Tick(s.start)
	want(t, rs, r.ID, map[string]string{"a": JobFailed, "b": JobBlocked})
	got, _ := rs.Get(r.ID)
	if got.Status != RunFailed {
		t.Fatalf("run is %s, want failed", got.Status)
	}
	if i, _ := got.job("a"); !strings.Contains(got.Jobs[i].Note, "could not start") {
		t.Errorf("note does not say why: %q", got.Jobs[i].Note)
	}
}

// Extending is how the orchestrator reacts to what it learns: new work may hang
// off jobs that are already done, and a cycle is still refused.
func TestExtendAddsJobsToALiveRun(t *testing.T) {
	rs := runStore(t)
	r := submitted(t, rs, 3, job("stories"))

	var s started
	rs.Tick(s.start)
	rs.Finish(r.ID, "stories", JobDone, "nine stories")
	got, _ := rs.Get(r.ID)
	if got.Status != RunDone {
		t.Fatalf("run is %s", got.Status)
	}

	// A run that finished is closed; one still going can be extended.
	r2 := submitted(t, rs, 3, job("stories"), job("repo"))
	rs.Tick(s.start)
	rs.Finish(r2.ID, "stories", JobDone, "nine stories")
	if _, err := rs.Extend(r2.ID, []Job{job("tickets", "stories")}); err != nil {
		t.Fatal(err)
	}
	if _, err := rs.Extend(r2.ID, []Job{job("tickets", "stories")}); err == nil {
		t.Error("the same job id was added twice")
	}
	if _, err := rs.Extend(r2.ID, []Job{job("x", "y")}); err == nil {
		t.Error("a job needing an unknown job was added")
	}
	s.ids = nil
	rs.Tick(s.start)
	if len(s.ids) != 1 || s.ids[0] != "tickets" {
		t.Fatalf("started %v, want the new tickets job", s.ids)
	}
}

func TestCancelStopsHandingOutWork(t *testing.T) {
	rs := runStore(t)
	r := submitted(t, rs, 3, job("a"), job("b", "a"))

	var s started
	rs.Tick(s.start)
	if _, err := rs.Cancel(r.ID, "the user changed direction"); err != nil {
		t.Fatal(err)
	}
	want(t, rs, r.ID, map[string]string{"a": JobCancelled, "b": JobCancelled})
	s.ids = nil
	rs.Tick(s.start)
	if len(s.ids) != 0 {
		t.Fatalf("a cancelled run started %v", s.ids)
	}
	if _, err := rs.Cancel(r.ID, ""); err == nil {
		t.Error("a cancelled run was cancelled again")
	}
}

// After loods is killed, a job the file says is running whose session is gone
// must not hold up the run forever.
func TestReconcileFailsJobsWhoseSessionIsGone(t *testing.T) {
	rs := runStore(t)
	r := submitted(t, rs, 3, job("a"), job("b"), job("c", "a"))

	var s started
	rs.Tick(s.start)
	alive := func(procID string) bool { return strings.HasSuffix(procID, ":b") }
	rs.Reconcile(alive)
	want(t, rs, r.ID, map[string]string{"a": JobFailed, "b": JobRunning, "c": JobBlocked})
	got, _ := rs.Get(r.ID)
	if i, _ := got.job("a"); !strings.Contains(got.Jobs[i].Note, "session was gone") {
		t.Errorf("note does not say why: %q", got.Jobs[i].Note)
	}
}

func TestAttachAndJobRefs(t *testing.T) {
	rs := runStore(t)
	r, err := rs.New("", "a house-hunting site", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rs.Attach(r.ID, "g/huizen", "g/huizen#run:"+r.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := rs.Get(r.ID)
	if got.Project != "g/huizen" || got.Orchestrator == "" {
		t.Fatalf("attach did not land: %+v", got)
	}

	run, j, err := parseJobRef(got.ID + ":stories")
	if err != nil || run != got.ID || j != "stories" {
		t.Fatalf("parseJobRef = %q %q %v", run, j, err)
	}
	for _, bad := range []string{"", "stories", "4f2a1c:", ":stories"} {
		if _, _, err := parseJobRef(bad); err == nil {
			t.Errorf("parseJobRef(%q) was accepted", bad)
		}
	}
	if _, err := rs.Finish("nosuchrun", "a", JobDone, ""); err == nil {
		t.Error("a job of an unknown run was finished")
	}
	if _, err := rs.Finish(got.ID, "nosuchjob", JobDone, ""); err == nil {
		t.Error("an unknown job was finished")
	}
	if _, err := rs.Finish(got.ID, "a", "whatever", ""); err == nil {
		t.Error("a job finished in a made-up state")
	}
}
