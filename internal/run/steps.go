package run

import (
	"fmt"
	"strings"
	"time"
)

// Steps are the checklist a job is handed before it starts: the orchestrator
// writes them with the job, the agent ticks them off one by one, and the board
// turns them into a percentage and a time left.
//
// They exist because a job agent is a black box otherwise: a session that has
// been "running" for twenty minutes says nothing about whether it is nearly
// done or stuck. A tick is one line in a terminal the agent is in anyway
// (`loods run step <run>:<job> <n>`), and it buys a progress bar, a forecast
// and — when a job fails halfway — a record of how far it got.
//
// The forecast is deliberately naive: the steps a job already finished set the
// pace for the ones it has left. That is only honest if the steps are of
// roughly the same size, which is why every prompt that asks for steps asks for
// small ones.

const (
	maxStepsPerJob = 24
	maxStepLen     = 140
)

// Step is one item of a job's checklist.
type Step struct {
	Text   string    `json:"text"`
	Done   bool      `json:"done,omitempty"`
	DoneAt time.Time `json:"done_at,omitzero"`
}

// validateSteps cleans up a job's checklist and refuses one that cannot be
// shown: empty items, too many, or a step too long to read in a row.
func (j *Job) validateSteps() error {
	out := make([]Step, 0, len(j.Steps))
	for _, s := range j.Steps {
		s.Text = strings.TrimSpace(strings.ReplaceAll(s.Text, "\n", " "))
		if s.Text == "" {
			continue
		}
		if len(s.Text) > maxStepLen {
			return fmt.Errorf("job %s: step %q is longer than %d characters; make it a step, not a paragraph",
				j.ID, Trim(s.Text, 40), maxStepLen)
		}
		// A graph file says what to do, not what is already done.
		s.Done, s.DoneAt = false, time.Time{}
		out = append(out, s)
	}
	if len(out) > maxStepsPerJob {
		return fmt.Errorf("job %s has %d steps, more than the %d a job may have; split the job instead",
			j.ID, len(out), maxStepsPerJob)
	}
	j.Steps = out
	return nil
}

// Trim folds a text onto one line of at most n characters. It is here rather
// than in textfmt because this package may not depend on it.
func Trim(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// StepsDone counts the ticked steps of a job.
func (j Job) StepsDone() int {
	n := 0
	for _, s := range j.Steps {
		if s.Done {
			n++
		}
	}
	return n
}

// Progress is how far along a job is, 0..1. A job without a checklist only has
// its status to go on: it is 0 until it is over, and a finished job is full
// whatever it did or did not tick, because the job is what the session was for.
func (j Job) Progress() float64 {
	if j.Over() {
		if j.Status == JobDone {
			return 1
		}
		// Failed, blocked, cancelled: show how far it got, not a full bar.
		if len(j.Steps) == 0 {
			return 0
		}
	}
	if len(j.Steps) == 0 {
		return 0
	}
	return float64(j.StepsDone()) / float64(len(j.Steps))
}

// pace is the average time one step of this job took, from the steps it already
// ticked: the first measured from the job's start, every next one from the tick
// before it. It needs at least one ticked step and a start time, so a job that
// has said nothing yet gets no forecast rather than a made-up one.
func (j Job) pace() (time.Duration, bool) {
	if j.StartedAt.IsZero() {
		return 0, false
	}
	last, n := j.StartedAt, 0
	for _, s := range j.Steps {
		if !s.Done || s.DoneAt.IsZero() {
			continue
		}
		if s.DoneAt.Before(last) {
			continue // clocks or an out-of-order tick; do not go backwards
		}
		last = s.DoneAt
		n++
	}
	if n == 0 {
		return 0, false
	}
	return last.Sub(j.StartedAt) / time.Duration(n), true
}

// ETA is how much longer a running job is expected to need: the pace its
// finished steps set, times the steps it has left, minus the time it has
// already spent on the one it is on. Everything else — a job that is over, one
// without a checklist, one that has not ticked anything yet — has no forecast,
// and says so by returning false rather than a zero.
func (j Job) ETA(now time.Time) (time.Duration, bool) {
	if j.Status != JobRunning || len(j.Steps) == 0 {
		return 0, false
	}
	left := len(j.Steps) - j.StepsDone()
	if left == 0 {
		return 0, true // every step ticked; it is writing its report
	}
	per, ok := j.pace()
	if !ok {
		return 0, false
	}
	// Time already spent on the step it is on, which is time it does not need
	// again. A job that is overdue on its current step forecasts the rest only.
	spent := time.Duration(0)
	if at, ok := j.lastTick(); ok && now.After(at) {
		spent = now.Sub(at)
	}
	d := per*time.Duration(left) - spent
	if d < 0 {
		d = 0
	}
	return d, true
}

// lastTick is when this job last reported a step, or when it started.
func (j Job) lastTick() (time.Time, bool) {
	at := j.StartedAt
	for _, s := range j.Steps {
		if s.Done && s.DoneAt.After(at) {
			at = s.DoneAt
		}
	}
	return at, !at.IsZero()
}

// Progress is the run as one fraction, 0..1: every job weighs the same, and a
// job with a checklist contributes how far it got. The jobs an orchestrator has
// not submitted yet are not in it, so a run that extends its graph can see its
// percentage drop — the alternative is a bar that lies about what is left.
func (r Run) Progress() float64 {
	if len(r.Jobs) == 0 {
		return 0
	}
	sum := 0.0
	for _, j := range r.Jobs {
		sum += j.Progress()
	}
	return sum / float64(len(r.Jobs))
}

// ETA is how much longer the whole run is expected to need. Two parts, because
// a run is jobs running now plus jobs waiting for a session:
//
//   - the longest forecast among the jobs running right now, and
//   - the jobs still to come, in waves of MaxPar, each wave as long as a job
//     has taken on average so far.
//
// It is a forecast, not a promise: a job the orchestrator has not thought of
// yet is not in it, and a job that stalls makes a liar of it. ok is false when
// there is nothing to go on at all.
func (r Run) ETA(now time.Time) (time.Duration, bool) {
	if r.Over() {
		return 0, false
	}
	longest, any := time.Duration(0), false
	for _, j := range r.Jobs {
		if d, ok := j.ETA(now); ok {
			any = true
			if d > longest {
				longest = d
			}
		}
	}
	pending := 0
	for _, j := range r.Jobs {
		if j.Status == JobPending {
			pending++
		}
	}
	if pending == 0 {
		return longest, any
	}
	typical, ok := r.typicalJob(now)
	if !ok {
		return longest, any
	}
	per := r.MaxPar
	if per < 1 {
		per = 1
	}
	waves := (pending + per - 1) / per
	return longest + typical*time.Duration(waves), true
}

// typicalJob is how long one job of this run has taken: the mean of the jobs
// that finished, or — before any has — the pace of a running job stretched over
// its whole checklist.
func (r Run) typicalJob(now time.Time) (time.Duration, bool) {
	sum, n := time.Duration(0), 0
	for _, j := range r.Jobs {
		if j.Status == JobDone && !j.StartedAt.IsZero() && !j.EndedAt.IsZero() {
			sum += j.EndedAt.Sub(j.StartedAt)
			n++
		}
	}
	if n > 0 {
		return sum / time.Duration(n), true
	}
	for _, j := range r.Jobs {
		if j.Status != JobRunning {
			continue
		}
		if per, ok := j.pace(); ok {
			return per * time.Duration(max(len(j.Steps), 1)), true
		}
		if !j.StartedAt.IsZero() && now.After(j.StartedAt) {
			return now.Sub(j.StartedAt), true // running, nothing ticked: as long as it has taken so far
		}
	}
	return 0, false
}
