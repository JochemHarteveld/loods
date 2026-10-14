package run

import (
	"strings"
	"testing"
	"time"
)

func TestParseJobsReadsTheChecklist(t *testing.T) {
	jobs, err := ParseJobs([]byte(`
jobs:
  - id: stories
    title: t
    prompt: p
    steps:
      - List the user roles
      - "   "
      - Write the stories
`))
	if err != nil {
		t.Fatal(err)
	}
	// The blank line is dropped, and nothing in the file may claim to be done.
	if n := len(jobs[0].Steps); n != 3 {
		t.Fatalf("read %d steps, want 3 before validation", n)
	}
	r := Run{ID: "a", Status: Planning, MaxPar: 1, Jobs: jobs}
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	got := r.Jobs[0].Steps
	if len(got) != 2 || got[0].Text != "List the user roles" || got[1].Text != "Write the stories" {
		t.Fatalf("checklist is %+v", got)
	}
	if got[0].Done {
		t.Error("a step came out of the file already done")
	}
}

func TestValidateRefusesUnshowableSteps(t *testing.T) {
	many := make([]Step, maxStepsPerJob+1)
	for i := range many {
		many[i] = Step{Text: "a step"}
	}
	for name, steps := range map[string][]Step{
		"too many": many,
		"too long": {{Text: strings.Repeat("x", maxStepLen+1)}},
	} {
		r := Run{ID: "a", Jobs: []Job{{ID: "j", Title: "t", Prompt: "p", Steps: steps}}}
		if err := r.validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// Ticking is what an agent does while it works, and it may say the same thing
// twice without that being a failure.
func TestStepTicksOnce(t *testing.T) {
	s, r := running(t, "one", "two", "three")
	if _, err := s.Step(r.ID, "job", 2); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Step(r.ID, "job", 2)
	j := got.Jobs[0]
	if j.StepsDone() != 1 || !j.Steps[1].Done || j.Steps[1].DoneAt.IsZero() {
		t.Fatalf("one tick left %+v", j.Steps)
	}
	for _, n := range []int{0, 4, -1} {
		if _, err := s.Step(r.ID, "job", n); err == nil {
			t.Errorf("step %d was accepted", n)
		}
	}
	if _, err := s.Step(r.ID, "nope", 1); err == nil {
		t.Error("an unknown job was ticked")
	}
}

// A job that reports done is done, whatever it forgot to tick: a finished job
// at 60% would only be the agent being sloppy, and the bar has to agree with
// the status.
func TestFinishFillsTheChecklist(t *testing.T) {
	s, r := running(t, "one", "two", "three")
	s.Step(r.ID, "job", 1)
	got, err := s.Finish(r.ID, "job", JobDone, "built it")
	if err != nil {
		t.Fatal(err)
	}
	if p := got.Jobs[0].Progress(); p != 1 {
		t.Fatalf("a finished job is at %v, want 1", p)
	}
	if n := got.Jobs[0].StepsDone(); n != 3 {
		t.Fatalf("%d of 3 steps ticked after done", n)
	}
	// A failed job keeps its checklist: how far it got is the point.
	s2, r2 := running(t, "one", "two", "three", "four")
	s2.Step(r2.ID, "job", 1)
	got2, _ := s2.Finish(r2.ID, "job", JobFailed, "stuck on two")
	if n := got2.Jobs[0].StepsDone(); n != 1 {
		t.Fatalf("a failed job ticked %d steps", n)
	}
	if p := got2.Jobs[0].Progress(); p != 0.25 {
		t.Fatalf("a failed job is at %v, want 0.25", p)
	}
	if _, err := s2.Step(r2.ID, "job", 2); err == nil {
		t.Error("a job that is over was still ticked")
	}
}

// A job with no checklist is all-or-nothing: it never told anybody more.
func TestProgressWithoutAChecklist(t *testing.T) {
	if p := (Job{Status: JobRunning}).Progress(); p != 0 {
		t.Errorf("running without steps is at %v", p)
	}
	if p := (Job{Status: JobDone}).Progress(); p != 1 {
		t.Errorf("done without steps is at %v", p)
	}
	if _, ok := (Job{Status: JobRunning}).ETA(time.Now()); ok {
		t.Error("a job without steps got a forecast")
	}
}

// The forecast is the pace of the steps a job finished, applied to the ones it
// has left, minus the time it is already spending on the one it is on.
func TestJobETA(t *testing.T) {
	start := time.Now().Add(-10 * time.Minute)
	j := Job{
		ID: "j", Status: JobRunning, StartedAt: start,
		Steps: []Step{
			{Text: "one", Done: true, DoneAt: start.Add(2 * time.Minute)},
			{Text: "two", Done: true, DoneAt: start.Add(6 * time.Minute)},
			{Text: "three"},
			{Text: "four"},
		},
	}
	// Two steps in six minutes is three minutes a step, two steps left is six,
	// and it has been on step three for four minutes already.
	d, ok := j.ETA(start.Add(10 * time.Minute))
	if !ok || d != 2*time.Minute {
		t.Fatalf("ETA = %v (%v), want 2m", d, ok)
	}
	// Nothing ticked yet: no pace, so no forecast rather than a made-up one.
	cold := Job{Status: JobRunning, StartedAt: start, Steps: []Step{{Text: "one"}}}
	if _, ok := cold.ETA(time.Now()); ok {
		t.Error("a job that ticked nothing got a forecast")
	}
	// Long past its own forecast: zero left, never negative.
	late, _ := j.ETA(start.Add(2 * time.Hour))
	if late != 0 {
		t.Fatalf("an overdue job forecasts %v, want 0", late)
	}
}

// The run's percentage is its jobs, and its forecast is the longest job running
// now plus the waves of jobs still waiting for a session.
func TestRunProgressAndETA(t *testing.T) {
	now := time.Now()
	start := now.Add(-10 * time.Minute)
	r := Run{
		ID: "a", Status: Running, MaxPar: 1,
		Jobs: []Job{
			{ID: "done", Status: JobDone, StartedAt: start, EndedAt: start.Add(4 * time.Minute),
				Steps: []Step{{Text: "x", Done: true}}},
			{ID: "now", Status: JobRunning, StartedAt: start.Add(4 * time.Minute), Steps: []Step{
				{Text: "a", Done: true, DoneAt: start.Add(7 * time.Minute)},
				{Text: "b"},
			}},
			{ID: "later", Status: JobPending, Steps: []Step{{Text: "a"}, {Text: "b"}}},
		},
	}
	if p := r.Progress(); p != (1+0.5+0)/3 {
		t.Fatalf("run is at %v", p)
	}
	// The running job: three minutes a step, one step left, three minutes on it
	// already, so nothing left on it. One pending job at the one finished job's
	// four minutes, one at a time.
	d, ok := r.ETA(now)
	if !ok || d != 4*time.Minute {
		t.Fatalf("run ETA = %v (%v), want 4m", d, ok)
	}
	r.Status, r.EndedAt = Done, now
	if _, ok := r.ETA(now); ok {
		t.Error("a finished run got a forecast")
	}
}

// running is a store with one running job that has the given checklist.
func running(t *testing.T, steps ...string) (*Store, Run) {
	t.Helper()
	rs := newStore(t)
	j := job("job")
	for _, text := range steps {
		j.Steps = append(j.Steps, Step{Text: text})
	}
	r := submitted(t, rs, 1, j)
	r, err := rs.Starting(r.ID, "job", "g/p#job:x:job")
	if err != nil {
		t.Fatal(err)
	}
	return rs, r
}
