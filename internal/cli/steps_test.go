package cli

import (
	"strings"
	"testing"
	"time"

	"loods/internal/run"
)

// What an agent sees after a tick, and what `loods run status` prints: the two
// places a checklist turns into a line of text.
func TestProgressLine(t *testing.T) {
	start := time.Now().Add(-6 * time.Minute)
	now := start.Add(6 * time.Minute)
	j := run.Job{
		ID: "stories", Status: run.JobRunning, StartedAt: start,
		Steps: []run.Step{
			{Text: "one", Done: true, DoneAt: start.Add(2 * time.Minute)},
			{Text: "two", Done: true, DoneAt: start.Add(4 * time.Minute)},
			{Text: "three"},
			{Text: "four"},
		},
	}
	// Two minutes a step, two left, two minutes into the third.
	if got := progress(j, now); got != "50% ~2m" {
		t.Errorf("progress = %q, want 50%% ~2m", got)
	}
	// A job without a checklist has nothing to say beyond its status.
	if got := progress(run.Job{ID: "x", Status: run.JobRunning}, now); got != "" {
		t.Errorf("a job without steps printed %q", got)
	}
	// A pending job has no pace to forecast from, so it shows how far it is only.
	if got := progress(run.Job{ID: "x", Status: run.JobPending, Steps: j.Steps}, now); got != "50%" {
		t.Errorf("pending printed %q, want \"50%%\"", got)
	}
}

func TestPrintSteps(t *testing.T) {
	var b strings.Builder
	printSteps(&b, run.Job{Steps: []run.Step{{Text: "one", Done: true}, {Text: "two"}}})
	want := "    ✓ 1. one\n      2. two\n"
	if b.String() != want {
		t.Errorf("printSteps =\n%q\nwant\n%q", b.String(), want)
	}
}
