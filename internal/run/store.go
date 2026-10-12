package run

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"loods/internal/store"
)

// doc is the file: every run, keyed by id. Runs live next to procs.json and
// claims.json in the state dir, not in plans.yaml: a plan is durable, a run is
// as alive as the sessions in it.
type doc struct {
	Runs map[string]Run `json:"runs"`
}

type codec struct{}

func (codec) Empty() *doc { return &doc{Runs: map[string]Run{}} }

func (c codec) Decode(b []byte) (*doc, error) {
	d := c.Empty()
	if err := json.Unmarshal(b, d); err != nil {
		return d, err
	}
	if d.Runs == nil {
		d.Runs = map[string]Run{}
	}
	return d, nil
}

func (codec) Encode(d *doc) ([]byte, error) {
	b, err := json.MarshalIndent(d, "", "  ")
	return append(b, '\n'), err
}

type Store struct {
	file *store.File[*doc]
}

func NewStore(path string) *Store { return &Store{file: store.NewFile(path, codec{})} }

func (s *Store) Path() string { return s.file.Path() }

// Refresh re-reads the file when it changed and reports whether it did, so the
// server picks up what the CLI (an agent reporting back) wrote.
func (s *Store) Refresh() bool {
	changed := s.file.Refresh()
	return changed && s.file.Err() == nil
}

// All returns every run, keyed by id.
func (s *Store) All() map[string]Run {
	d, _ := s.file.Doc()
	out := make(map[string]Run, len(d.Runs))
	for k, r := range d.Runs {
		out[k] = r
	}
	return out
}

func (s *Store) Get(id string) (Run, bool) {
	d, _ := s.file.Doc()
	r, ok := d.Runs[id]
	return r, ok
}

// Of returns the runs of one project, newest first.
func (s *Store) Of(project string) []Run {
	var out []Run
	for _, r := range s.All() {
		if r.Project == project {
			out = append(out, r)
		}
	}
	Sort(out)
	return out
}

// Live returns the runs that are not finished, newest first.
func (s *Store) Live() []Run {
	var out []Run
	for _, r := range s.All() {
		if !r.Over() {
			out = append(out, r)
		}
	}
	Sort(out)
	return out
}

// Sort puts the newest run first, ties broken by id so the order is stable.
func Sort(rr []Run) {
	sort.Slice(rr, func(i, j int) bool {
		if rr[i].CreatedAt.Equal(rr[j].CreatedAt) {
			return rr[i].ID < rr[j].ID
		}
		return rr[i].CreatedAt.After(rr[j].CreatedAt)
	})
}

// update prunes old history, then applies fn under the file lock, so the
// server, the orchestrator and every job agent never lose each other's changes.
func (s *Store) update(fn func(map[string]Run) error) error {
	return s.file.Update(func(d *doc) error {
		prune(d.Runs)
		if err := fn(d.Runs); err != nil {
			return err
		}
		if len(d.Runs) > maxRuns {
			return fmt.Errorf("more than %d runs; something is wrong", maxRuns)
		}
		return nil
	})
}

// prune drops finished runs once they are older than maxAge, and the oldest
// finished ones when the file is full, so history does not grow without end.
func prune(m map[string]Run) {
	now := time.Now()
	var finished []Run
	for id, r := range m {
		if !r.Over() {
			continue
		}
		at := r.EndedAt
		if at.IsZero() {
			at = r.CreatedAt
		}
		if now.Sub(at) > maxAge {
			delete(m, id)
			continue
		}
		finished = append(finished, r)
	}
	if len(m) <= maxRuns {
		return
	}
	Sort(finished)
	for i := len(finished) - 1; i >= 0 && len(m) > maxRuns; i-- {
		delete(m, finished[i].ID)
	}
}

// New starts a run with no jobs yet: the orchestrator has the goal and submits
// its graph once it has one. Project may be empty while the folder is still
// being created.
func (s *Store) New(project, goal string, maxPar int) (Run, error) {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return Run{}, errors.New("a run needs a goal")
	}
	if len(goal) > maxGoalLen {
		return Run{}, fmt.Errorf("the goal is longer than %d characters", maxGoalLen)
	}
	if maxPar == 0 {
		maxPar = DefaultMaxPar
	}
	if maxPar < 1 || maxPar > HardMaxPar {
		return Run{}, fmt.Errorf("max-parallel is %d; keep it between 1 and %d", maxPar, HardMaxPar)
	}
	r := Run{
		Project: project, Goal: goal, Status: Planning, MaxPar: maxPar,
		CreatedAt: time.Now().Truncate(time.Second),
	}
	err := s.update(func(m map[string]Run) error {
		for {
			r.ID = newID()
			if _, taken := m[r.ID]; !taken {
				break
			}
		}
		m[r.ID] = r
		return nil
	})
	return r, err
}

func newID() string {
	var b [3]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Submit gives a planning run its graph and sets it running. Submitting twice
// is refused: use Extend to add work to a run that is already going.
func (s *Store) Submit(id string, jobs []Job) (Run, error) {
	return s.change(id, func(r *Run) error {
		if r.Status != Planning {
			return fmt.Errorf("run %s is %s; use `loods run extend` to add jobs", r.ID, r.Status)
		}
		r.Jobs = jobs
		if err := r.validate(); err != nil {
			return err
		}
		r.Status = Running
		return nil
	})
}

// Extend adds jobs to a run that is already going, which is how the
// orchestrator reacts to what it learns: new jobs may depend on ones that are
// already done. The graph is validated as a whole, so an extension that would
// make a cycle is refused and nothing changes.
func (s *Store) Extend(id string, jobs []Job) (Run, error) {
	return s.change(id, func(r *Run) error {
		if r.Over() {
			return fmt.Errorf("run %s is %s", r.ID, r.Status)
		}
		if r.Status == Planning {
			return fmt.Errorf("run %s has no graph yet; submit it first", r.ID)
		}
		for _, j := range jobs {
			if _, exists := r.JobIndex(j.ID); exists {
				return fmt.Errorf("run %s already has a job %q", r.ID, j.ID)
			}
		}
		r.Jobs = append(r.Jobs, jobs...)
		return r.validate()
	})
}

// Starting marks a job as running and records its process. The scheduler calls
// this before it starts the process, so two schedulers (or a scheduler and a
// retry) cannot start the same job twice.
func (s *Store) Starting(runID, job, procID string) (Run, error) {
	return s.change(runID, func(r *Run) error {
		i, ok := r.JobIndex(job)
		if !ok {
			return fmt.Errorf("run %s has no job %q", runID, job)
		}
		if r.Jobs[i].Status != JobPending {
			return fmt.Errorf("job %s:%s is %s, not pending", runID, job, r.Jobs[i].Status)
		}
		r.Jobs[i].Status, r.Jobs[i].ProcID = JobRunning, procID
		r.Jobs[i].StartedAt = time.Now().Truncate(time.Second)
		return nil
	})
}

// Finish is how a job agent reports back: done with what it did, or failed with
// why. Both notes end up in the Agents view, so "done" is not a useful note.
func (s *Store) Finish(runID, job, status, note string) (Run, error) {
	if status != JobDone && status != JobFailed {
		return Run{}, fmt.Errorf("a job finishes done or failed, not %q", status)
	}
	return s.change(runID, func(r *Run) error {
		i, ok := r.JobIndex(job)
		if !ok {
			return fmt.Errorf("run %s has no job %q", runID, job)
		}
		if r.Jobs[i].Over() {
			return fmt.Errorf("job %s:%s is already %s", runID, job, r.Jobs[i].Status)
		}
		r.Jobs[i].Status, r.Jobs[i].Note = status, strings.TrimSpace(note)
		r.Jobs[i].EndedAt = time.Now().Truncate(time.Second)
		return nil
	})
}

// Cancel stops handing out work: everything not finished is cancelled. The
// processes themselves are stopped by the caller, because a run only knows
// about jobs, not about pids.
func (s *Store) Cancel(id, note string) (Run, error) {
	return s.change(id, func(r *Run) error {
		if r.Over() {
			return fmt.Errorf("run %s is already %s", r.ID, r.Status)
		}
		now := time.Now().Truncate(time.Second)
		for i, j := range r.Jobs {
			if !j.Over() {
				r.Jobs[i].Status, r.Jobs[i].EndedAt = JobCancelled, now
				if note != "" {
					r.Jobs[i].Note = note
				}
			}
		}
		r.Status, r.EndedAt = Cancelled, now
		return nil
	})
}

// Attach records which session owns the run, and the project once its folder
// exists (`loods new` creates the run before the project is on the board).
func (s *Store) Attach(id, project, orchestrator string) (Run, error) {
	return s.change(id, func(r *Run) error {
		if project != "" {
			r.Project = project
		}
		if orchestrator != "" {
			r.Orchestrator = orchestrator
		}
		return nil
	})
}

// change applies fn to one run, settles it (failure propagation, run status)
// and writes it back under the file lock.
func (s *Store) change(id string, fn func(*Run) error) (Run, error) {
	var out Run
	err := s.update(func(m map[string]Run) error {
		r, ok := m[id]
		if !ok {
			return fmt.Errorf("no run %s", id)
		}
		if err := fn(&r); err != nil {
			return err
		}
		r.settle()
		m[r.ID] = r
		out = r
		return nil
	})
	return out, err
}

// Reconcile is the startup pass: a job the state file says is running whose
// process did not survive (loods was killed, the machine rebooted) is marked
// failed, so the run moves on instead of waiting for a session that is gone.
func (s *Store) Reconcile(alive func(procID string) bool) {
	s.update(func(m map[string]Run) error {
		now := time.Now().Truncate(time.Second)
		for id, r := range m {
			changed := false
			for i, j := range r.Jobs {
				if j.Status == JobRunning && !alive(j.ProcID) {
					r.Jobs[i].Status, r.Jobs[i].EndedAt = JobFailed, now
					r.Jobs[i].Note = "its session was gone when loods started again"
					changed = true
				}
			}
			if r.settle() || changed {
				m[id] = r
			}
		}
		return nil
	})
}

// Tick is one scheduler pass over every live run: it picks the jobs that may
// start now and starts them, newest run last so a long-running one does not
// starve. start does the real work (build a session and hand it to the process
// manager); a job whose process fails to start is marked failed, not left
// pending.
func (s *Store) Tick(start func(Run, Job) (procID string, err error)) {
	live := s.Live()
	if len(live) == 0 {
		return
	}
	// Exclusive locks are global across runs: two runs on one repo take turns.
	held := map[string]bool{}
	for _, r := range live {
		for _, k := range r.locks() {
			held[k] = true
		}
	}
	for i := len(live) - 1; i >= 0; i-- {
		r := live[i]
		for _, j := range r.ready(held) {
			procID, err := start(r, j)
			if err != nil {
				s.Finish(r.ID, j.ID, JobFailed, "could not start: "+err.Error())
				continue
			}
			if _, err := s.Starting(r.ID, j.ID, procID); err != nil {
				continue
			}
			if updated, ok := s.Get(r.ID); ok {
				r = updated
			}
		}
	}
}
