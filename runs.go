package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// A run is one goal handed to a tree of agents: an orchestrator that decides
// what has to happen, and a job per piece of work it hands out. Jobs are
// ordinary agent processes (Spec kind "agent", see agents.go), so they get the
// same terminal, backlog and stop / restart as a dev server, and the dock shows
// them while you walk the board.
//
// The orchestrator declares the graph; loods decides what may run *now*. That
// split is on purpose: "can these two run at the same time?" is a question
// about dependencies, a repo that only one job may write to, and how many
// sessions you want open at once — all of it checkable, none of it worth
// re-deciding from a prompt on every pass. The orchestrator stays free to
// extend the graph while it runs (Extend), which is where its judgement goes.
//
// Runs live next to procs.json and claims.json in the state dir, not in
// plans.yaml: a plan is durable, a run is as alive as the sessions in it.

const (
	maxJobsPerRun = 40
	maxRuns       = 200
	runMaxAge     = 7 * 24 * time.Hour // finished runs stay this long, as history
	defaultMaxPar = 3
	hardMaxPar    = 8
	maxGoalLen    = 500
	maxTitleLen   = 200
	maxPromptLen  = 8000
)

// Job states. "blocked" is not a failure of this job: something it needed
// failed, so it will never become ready and says so instead of sitting in
// pending forever.
const (
	JobPending   = "pending"
	JobRunning   = "running"
	JobDone      = "done"
	JobFailed    = "failed"
	JobBlocked   = "blocked"
	JobCancelled = "cancelled"
)

// Run states. A run is "planning" until the orchestrator submits its graph.
const (
	RunPlanning  = "planning"
	RunRunning   = "running"
	RunDone      = "done"
	RunFailed    = "failed"
	RunCancelled = "cancelled"
)

type Job struct {
	ID    string `json:"id"` // "stories", unique within the run; what needs refers to
	Role  string `json:"role"`
	Title string `json:"title"`
	// Prompt is what the agent session starts with. The reporting loop is added
	// by jobPrompt, so a job prompt only has to say what to build.
	Prompt string `json:"prompt"`
	Needs  []string `json:"needs,omitempty"`
	// Exclusive names something only one job may hold at a time, scoped to the
	// project: "repo" for anything that commits, because two sessions in one
	// working tree race over the index whatever their dependencies say.
	Exclusive string `json:"exclusive,omitempty"`
	Task      int    `json:"task,omitempty"` // planboard task this job owns, if any

	Status    string    `json:"status"`
	ProcID    string    `json:"proc,omitempty"` // the Manager process, once started
	Note      string    `json:"note,omitempty"` // what the agent reported back
	StartedAt time.Time `json:"started_at,omitzero"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
}

func (j Job) over() bool {
	return j.Status == JobDone || j.Status == JobFailed || j.Status == JobBlocked || j.Status == JobCancelled
}

// AgentRun is one run: the goal, the graph of jobs and where it stands. The
// name says agent because github.go already has a Run (a CI workflow run).
type AgentRun struct {
	ID      string `json:"id"`
	Project string `json:"project"` // project rel; set as soon as the folder exists
	Goal    string `json:"goal"`
	Status  string `json:"status"`
	MaxPar  int    `json:"max_parallel"`
	// Orchestrator is the process id of the session that owns this run, so the
	// Agents view can put the children under it.
	Orchestrator string    `json:"orchestrator,omitempty"`
	Jobs         []Job     `json:"jobs,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	EndedAt      time.Time `json:"ended_at,omitzero"`
}

func (r AgentRun) over() bool {
	return r.Status == RunDone || r.Status == RunFailed || r.Status == RunCancelled
}

func (r AgentRun) job(id string) (int, bool) {
	for i, j := range r.Jobs {
		if j.ID == id {
			return i, true
		}
	}
	return 0, false
}

func (r AgentRun) running() int {
	n := 0
	for _, j := range r.Jobs {
		if j.Status == JobRunning {
			n++
		}
	}
	return n
}

// exclusiveKey scopes an exclusive lock to the project, so two runs on one repo
// still take turns while runs on different projects never wait for each other.
func (r AgentRun) exclusiveKey(j Job) string {
	if j.Exclusive == "" {
		return ""
	}
	return r.Project + "/" + j.Exclusive
}

// ready picks the jobs that may start now: pending, everything they need is
// done, within the run's parallel limit, and not after an exclusive lock
// someone else holds. held carries the locks taken by jobs already running
// elsewhere (other runs, this same pass), and is extended as jobs are picked.
// Declaration order decides, so the same graph always starts in the same order.
func (r AgentRun) ready(held map[string]bool) []Job {
	if r.Status != RunRunning {
		return nil
	}
	free := r.MaxPar - r.running()
	var out []Job
	for _, j := range r.Jobs {
		if free <= 0 {
			break
		}
		if j.Status != JobPending || !r.needsMet(j) {
			continue
		}
		if k := r.exclusiveKey(j); k != "" {
			if held[k] {
				continue
			}
			held[k] = true
		}
		out = append(out, j)
		free--
	}
	return out
}

func (r AgentRun) needsMet(j Job) bool {
	for _, need := range j.Needs {
		i, ok := r.job(need)
		if !ok || r.Jobs[i].Status != JobDone {
			return false
		}
	}
	return true
}

// locks are the exclusive keys this run holds right now, so the scheduler can
// collect them across runs before it picks anything.
func (r AgentRun) locks() []string {
	var out []string
	for _, j := range r.Jobs {
		if j.Status == JobRunning {
			if k := r.exclusiveKey(j); k != "" {
				out = append(out, k)
			}
		}
	}
	return out
}

// settle propagates failure and finishes the run. A job that needs something
// which failed, was blocked or cancelled can never become ready, so it is
// blocked instead — the Agents view says "waits for stories" and means it.
// Returns whether anything changed.
func (r *AgentRun) settle() bool {
	changed := false
	for again := true; again; {
		again = false
		for i, j := range r.Jobs {
			if j.Status != JobPending {
				continue
			}
			for _, need := range j.Needs {
				k, ok := r.job(need)
				if !ok {
					continue
				}
				if s := r.Jobs[k].Status; s == JobFailed || s == JobBlocked || s == JobCancelled {
					r.Jobs[i].Status = JobBlocked
					r.Jobs[i].Note = "never ran: " + need + " " + s
					r.Jobs[i].EndedAt = time.Now().Truncate(time.Second)
					changed, again = true, true
					break
				}
			}
		}
	}
	if r.Status != RunRunning || len(r.Jobs) == 0 {
		return changed
	}
	failed := false
	for _, j := range r.Jobs {
		if !j.over() {
			return changed
		}
		if j.Status != JobDone {
			failed = true
		}
	}
	r.Status, r.EndedAt = RunDone, time.Now().Truncate(time.Second)
	if failed {
		r.Status = RunFailed
	}
	return true
}

// validate checks the whole job list: ids usable as process ids and as `needs`
// references, no dangling need, no cycle. A graph is refused as a whole, so a
// submit either lands complete or not at all.
func (r *AgentRun) validate() error {
	if len(r.Jobs) == 0 {
		return errors.New("a run needs at least one job")
	}
	if len(r.Jobs) > maxJobsPerRun {
		return fmt.Errorf("%d jobs is more than the %d a run may have", len(r.Jobs), maxJobsPerRun)
	}
	seen := map[string]bool{}
	for i := range r.Jobs {
		j := &r.Jobs[i]
		j.ID, j.Role = strings.TrimSpace(j.ID), strings.TrimSpace(j.Role)
		j.Title, j.Prompt = strings.TrimSpace(j.Title), strings.TrimSpace(j.Prompt)
		if err := validJobID(j.ID); err != nil {
			return err
		}
		if seen[j.ID] {
			return fmt.Errorf("two jobs are called %q", j.ID)
		}
		seen[j.ID] = true
		if j.Role == "" {
			j.Role = j.ID
		}
		if j.Title == "" {
			return fmt.Errorf("job %s has no title", j.ID)
		}
		if len(j.Title) > maxTitleLen {
			return fmt.Errorf("job %s: title is longer than %d characters", j.ID, maxTitleLen)
		}
		if j.Prompt == "" {
			return fmt.Errorf("job %s has no prompt: say what the agent should build", j.ID)
		}
		if len(j.Prompt) > maxPromptLen {
			return fmt.Errorf("job %s: prompt is longer than %d characters", j.ID, maxPromptLen)
		}
		if j.Status == "" {
			j.Status = JobPending
		}
	}
	for _, j := range r.Jobs {
		for _, need := range j.Needs {
			if !seen[need] {
				return fmt.Errorf("job %s needs %q, which is not a job of this run", j.ID, need)
			}
			if need == j.ID {
				return fmt.Errorf("job %s needs itself", j.ID)
			}
		}
	}
	return r.acyclic()
}

// acyclic reports the first cycle it finds, naming the jobs in it: a graph that
// can never run is a mistake in the orchestrator's plan, and it has to read why.
func (r *AgentRun) acyclic() error {
	const (
		open = 1
		shut = 2
	)
	mark := map[string]int{}
	var path []string
	var walk func(id string) error
	walk = func(id string) error {
		switch mark[id] {
		case shut:
			return nil
		case open:
			if i := indexOf(path, id); i >= 0 {
				return fmt.Errorf("these jobs wait for each other: %s", strings.Join(append(path[i:], id), " → "))
			}
			return fmt.Errorf("job %s waits for itself", id)
		}
		mark[id] = open
		path = append(path, id)
		i, _ := r.job(id)
		for _, need := range r.Jobs[i].Needs {
			if err := walk(need); err != nil {
				return err
			}
		}
		path = path[:len(path)-1]
		mark[id] = shut
		return nil
	}
	for _, j := range r.Jobs {
		if err := walk(j.ID); err != nil {
			return err
		}
	}
	return nil
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

// validJobID keeps job ids to what can go in a process id and be typed in a
// `loods run done <run>:<job>` ref.
func validJobID(id string) error {
	if id == "" {
		return errors.New("a job needs an id")
	}
	if len(id) > 32 {
		return fmt.Errorf("job id %q is longer than 32 characters", id)
	}
	for _, c := range id {
		ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_'
		if !ok {
			return fmt.Errorf("job id %q: use lowercase letters, digits, - and _", id)
		}
	}
	return nil
}

type RunStore struct {
	path string

	mu    sync.Mutex
	stamp string
	runs  map[string]AgentRun
}

func runsPath() string { return filepath.Join(stateDir(), "runs.json") }

func newRunStore(path string) *RunStore {
	return &RunStore{path: path, runs: map[string]AgentRun{}}
}

type runFile struct {
	Runs map[string]AgentRun `json:"runs"`
}

func readRuns(path string) (runFile, error) {
	rf := runFile{Runs: map[string]AgentRun{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return rf, nil
	}
	if err != nil {
		return rf, err
	}
	if err := json.Unmarshal(b, &rf); err != nil {
		return rf, err
	}
	if rf.Runs == nil {
		rf.Runs = map[string]AgentRun{}
	}
	return rf, nil
}

func writeRuns(path string, rf runFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rf, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Refresh re-reads the file when it changed and reports whether it did, so the
// server picks up what the CLI (an agent reporting back) wrote.
func (rs *RunStore) Refresh() bool {
	stamp := fileStamp(rs.path)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if stamp == rs.stamp && rs.stamp != "" {
		return false
	}
	rf, err := readRuns(rs.path)
	rs.stamp = stamp
	if err != nil {
		return false // keep what we had; a broken file is not worth clearing the view
	}
	rs.runs = rf.Runs
	return true
}

// All returns every run, keyed by id.
func (rs *RunStore) All() map[string]AgentRun {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make(map[string]AgentRun, len(rs.runs))
	for k, r := range rs.runs {
		out[k] = r
	}
	return out
}

func (rs *RunStore) Get(id string) (AgentRun, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	r, ok := rs.runs[id]
	return r, ok
}

// Of returns the runs of one project, newest first.
func (rs *RunStore) Of(project string) []AgentRun {
	var out []AgentRun
	for _, r := range rs.All() {
		if r.Project == project {
			out = append(out, r)
		}
	}
	sortRuns(out)
	return out
}

// Live returns the runs that are not finished, newest first.
func (rs *RunStore) Live() []AgentRun {
	var out []AgentRun
	for _, r := range rs.All() {
		if !r.over() {
			out = append(out, r)
		}
	}
	sortRuns(out)
	return out
}

func sortRuns(rr []AgentRun) {
	sort.Slice(rr, func(i, j int) bool {
		if rr[i].CreatedAt.Equal(rr[j].CreatedAt) {
			return rr[i].ID < rr[j].ID
		}
		return rr[i].CreatedAt.After(rr[j].CreatedAt)
	})
}

// update takes the lock, re-reads the file, applies fn and writes it back, so
// the server, the orchestrator and every job agent never lose each other's
// changes — the same shape as ClaimStore.update and PlanStore.Update.
func (rs *RunStore) update(fn func(map[string]AgentRun) error) error {
	if err := os.MkdirAll(filepath.Dir(rs.path), 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(rs.path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	rf, err := readRuns(rs.path)
	if err != nil {
		return fmt.Errorf("%s: %w", rs.path, err)
	}
	prune(rf.Runs)
	if err := fn(rf.Runs); err != nil {
		return err
	}
	if len(rf.Runs) > maxRuns {
		return fmt.Errorf("more than %d runs; something is wrong", maxRuns)
	}
	if err := writeRuns(rs.path, rf); err != nil {
		return err
	}
	rs.Refresh()
	return nil
}

// prune drops finished runs once they are older than runMaxAge, and the oldest
// finished ones when the file is full, so history does not grow without end.
func prune(m map[string]AgentRun) {
	now := time.Now()
	var finished []AgentRun
	for id, r := range m {
		if !r.over() {
			continue
		}
		at := r.EndedAt
		if at.IsZero() {
			at = r.CreatedAt
		}
		if now.Sub(at) > runMaxAge {
			delete(m, id)
			continue
		}
		finished = append(finished, r)
	}
	if len(m) <= maxRuns {
		return
	}
	sortRuns(finished)
	for i := len(finished) - 1; i >= 0 && len(m) > maxRuns; i-- {
		delete(m, finished[i].ID)
	}
}

// New starts a run with no jobs yet: the orchestrator has the goal and submits
// its graph once it has one. Project may be empty while the folder is still
// being created.
func (rs *RunStore) New(project, goal string, maxPar int) (AgentRun, error) {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return AgentRun{}, errors.New("a run needs a goal")
	}
	if len(goal) > maxGoalLen {
		return AgentRun{}, fmt.Errorf("the goal is longer than %d characters", maxGoalLen)
	}
	if maxPar == 0 {
		maxPar = defaultMaxPar
	}
	if maxPar < 1 || maxPar > hardMaxPar {
		return AgentRun{}, fmt.Errorf("max-parallel is %d; keep it between 1 and %d", maxPar, hardMaxPar)
	}
	r := AgentRun{
		Project: project, Goal: goal, Status: RunPlanning, MaxPar: maxPar,
		CreatedAt: time.Now().Truncate(time.Second),
	}
	err := rs.update(func(m map[string]AgentRun) error {
		for {
			r.ID = newRunID()
			if _, taken := m[r.ID]; !taken {
				break
			}
		}
		m[r.ID] = r
		return nil
	})
	return r, err
}

func newRunID() string {
	var b [3]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Submit gives a planning run its graph and sets it running. Submitting twice
// is refused: use Extend to add work to a run that is already going.
func (rs *RunStore) Submit(id string, jobs []Job) (AgentRun, error) {
	return rs.change(id, func(r *AgentRun) error {
		if r.Status != RunPlanning {
			return fmt.Errorf("run %s is %s; use `loods run extend` to add jobs", r.ID, r.Status)
		}
		r.Jobs = jobs
		if err := r.validate(); err != nil {
			return err
		}
		r.Status = RunRunning
		return nil
	})
}

// Extend adds jobs to a run that is already going, which is how the
// orchestrator reacts to what it learns: new jobs may depend on ones that are
// already done. The graph is validated as a whole, so an extension that would
// make a cycle is refused and nothing changes.
func (rs *RunStore) Extend(id string, jobs []Job) (AgentRun, error) {
	return rs.change(id, func(r *AgentRun) error {
		if r.over() {
			return fmt.Errorf("run %s is %s", r.ID, r.Status)
		}
		if r.Status == RunPlanning {
			return fmt.Errorf("run %s has no graph yet; submit it first", r.ID)
		}
		for _, j := range jobs {
			if _, exists := r.job(j.ID); exists {
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
func (rs *RunStore) Starting(run, job, procID string) (AgentRun, error) {
	return rs.change(run, func(r *AgentRun) error {
		i, ok := r.job(job)
		if !ok {
			return fmt.Errorf("run %s has no job %q", run, job)
		}
		if r.Jobs[i].Status != JobPending {
			return fmt.Errorf("job %s:%s is %s, not pending", run, job, r.Jobs[i].Status)
		}
		r.Jobs[i].Status, r.Jobs[i].ProcID = JobRunning, procID
		r.Jobs[i].StartedAt = time.Now().Truncate(time.Second)
		return nil
	})
}

// Finish is how a job agent reports back: done with what it did, or failed with
// why. Both notes end up in the Agents view, so "done" is not a useful note.
func (rs *RunStore) Finish(run, job, status, note string) (AgentRun, error) {
	if status != JobDone && status != JobFailed {
		return AgentRun{}, fmt.Errorf("a job finishes done or failed, not %q", status)
	}
	return rs.change(run, func(r *AgentRun) error {
		i, ok := r.job(job)
		if !ok {
			return fmt.Errorf("run %s has no job %q", run, job)
		}
		if r.Jobs[i].over() {
			return fmt.Errorf("job %s:%s is already %s", run, job, r.Jobs[i].Status)
		}
		r.Jobs[i].Status, r.Jobs[i].Note = status, strings.TrimSpace(note)
		r.Jobs[i].EndedAt = time.Now().Truncate(time.Second)
		return nil
	})
}

// Cancel stops handing out work: everything not finished is cancelled. The
// processes themselves are stopped by the caller (the server, through the
// Manager), because a run only knows about jobs, not about pids.
func (rs *RunStore) Cancel(id, note string) (AgentRun, error) {
	return rs.change(id, func(r *AgentRun) error {
		if r.over() {
			return fmt.Errorf("run %s is already %s", r.ID, r.Status)
		}
		now := time.Now().Truncate(time.Second)
		for i, j := range r.Jobs {
			if !j.over() {
				r.Jobs[i].Status, r.Jobs[i].EndedAt = JobCancelled, now
				if note != "" {
					r.Jobs[i].Note = note
				}
			}
		}
		r.Status, r.EndedAt = RunCancelled, now
		return nil
	})
}

// Attach records which session owns the run, and the project once its folder
// exists (`loods new` creates the run before the project is on the board).
func (rs *RunStore) Attach(id, project, orchestrator string) (AgentRun, error) {
	return rs.change(id, func(r *AgentRun) error {
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
func (rs *RunStore) change(id string, fn func(*AgentRun) error) (AgentRun, error) {
	var out AgentRun
	err := rs.update(func(m map[string]AgentRun) error {
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
func (rs *RunStore) Reconcile(alive func(procID string) bool) {
	rs.update(func(m map[string]AgentRun) error {
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
// starve. start does the real work (build a Spec, hand it to the Manager); a
// job whose process fails to start is marked failed, not left pending.
func (rs *RunStore) Tick(start func(AgentRun, Job) (procID string, err error)) {
	live := rs.Live()
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
				rs.Finish(r.ID, j.ID, JobFailed, "could not start: "+err.Error())
				continue
			}
			if _, err := rs.Starting(r.ID, j.ID, procID); err != nil {
				continue
			}
			if updated, ok := rs.Get(r.ID); ok {
				r = updated
			}
		}
	}
}

// parseJobRef reads "<run>:<job>", the ref an agent is given on its command
// line and types back when it reports in.
func parseJobRef(ref string) (run, job string, err error) {
	run, job, ok := strings.Cut(strings.TrimSpace(ref), ":")
	if !ok || run == "" || job == "" {
		return "", "", fmt.Errorf("%q is not a job: use <run>:<job>, like 4f2a1c:stories", ref)
	}
	return run, job, nil
}
