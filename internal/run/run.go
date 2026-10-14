// Package run is one goal handed to a tree of agents: an orchestrator that
// decides what has to happen, and a job per piece of work it hands out.
//
// The orchestrator declares the graph; loods decides what may run *now*. That
// split is on purpose: "can these two run at the same time?" is a question
// about dependencies, a repo that only one job may write to, and how many
// sessions you want open at once — all of it checkable, none of it worth
// re-deciding from a prompt on every pass. The orchestrator stays free to
// extend the graph while it runs (Extend), which is where its judgement goes.
//
// This package decides and records; it never starts a process. Starting is the
// server's job, because a session has to outlive the command that asked for it.
package run

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	maxJobsPerRun = 40
	maxRuns       = 200
	maxAge        = 7 * 24 * time.Hour // finished runs stay this long, as history
	maxGoalLen    = 500
	maxTitleLen   = 200
	maxPromptLen  = 8000

	// DefaultMaxPar and HardMaxPar bound how many sessions one run may have
	// open at a time.
	DefaultMaxPar = 3
	HardMaxPar    = 8
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
	Planning  = "planning"
	Running   = "running"
	Done      = "done"
	Failed    = "failed"
	Cancelled = "cancelled"
)

// How a job gets a working tree. Shared is the default: every job of a project
// works in the project folder, which is why anything that commits has to take
// the repo lock. Worktree gives the job a git worktree of its own on a branch
// of its own, so it can commit while another session commits in the project
// folder — at the price of a branch someone has to merge.
const (
	IsolationShared   = ""
	IsolationWorktree = "worktree"
)

type Job struct {
	ID    string `json:"id"` // "stories", unique within the run; what needs refers to
	Role  string `json:"role"`
	Title string `json:"title"`
	// Prompt is what the agent session starts with. The reporting loop is added
	// when the session is built, so a job prompt only has to say what to build.
	Prompt string   `json:"prompt"`
	Needs  []string `json:"needs,omitempty"`
	// Exclusive names something only one job may hold at a time, scoped to the
	// project: "repo" for anything that commits, because two sessions in one
	// working tree race over the index whatever their dependencies say.
	Exclusive string `json:"exclusive,omitempty"`
	// Isolation is where the job works: "" in the project folder with everyone
	// else, "worktree" in a git worktree of its own. An isolated job holds its
	// exclusive lock against its own tree only, so three worktree jobs that all
	// declare `exclusive: repo` still run at the same time.
	Isolation string `json:"isolation,omitempty"`
	Task      int    `json:"task,omitempty"` // planboard task this job owns, if any
	// Owns is the paths only this job may change: the lane boundary between
	// jobs that run at the same time, checked when the graph is submitted
	// rather than left to a design doc nobody enforces. See owns.go.
	Owns []string `json:"owns,omitempty"`
	// Steps is the checklist the job is handed before it starts: small pieces
	// of work it ticks off as it goes, which is where the board's percentage
	// and its forecast come from. See steps.go.
	Steps []Step `json:"steps,omitempty"`

	Status    string    `json:"status"`
	ProcID    string    `json:"proc,omitempty"` // the process it runs as, once started
	Note      string    `json:"note,omitempty"` // what the agent reported back
	StartedAt time.Time `json:"started_at,omitzero"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
}

// Over reports whether this job will not change by itself any more.
func (j Job) Over() bool {
	return j.Status == JobDone || j.Status == JobFailed || j.Status == JobBlocked || j.Status == JobCancelled
}

// Run is the goal, the graph of jobs and where it stands.
type Run struct {
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

func (r Run) Over() bool {
	return r.Status == Done || r.Status == Failed || r.Status == Cancelled
}

// JobIndex finds a job by its id within this run.
func (r Run) JobIndex(id string) (int, bool) {
	for i, j := range r.Jobs {
		if j.ID == id {
			return i, true
		}
	}
	return 0, false
}

// Tally is what a run looks like in one line: how many jobs are done, and how
// many sessions are open right now.
func (r Run) Tally() (done, running int) {
	for _, j := range r.Jobs {
		switch j.Status {
		case JobDone:
			done++
		case JobRunning:
			running++
		}
	}
	return done, running
}

func (r Run) running() int {
	_, n := r.Tally()
	return n
}

// exclusiveKey scopes an exclusive lock to the project, so two runs on one repo
// still take turns while runs on different projects never wait for each other.
//
// A job in a worktree of its own is scoped to that tree instead. That is the
// whole point of isolation: "repo" means the working tree the job commits in,
// and an isolated job commits in nobody else's, so it waits for nobody. The
// orchestrator keeps writing `exclusive: repo` and gets the parallelism for
// free rather than having to reason about lock names.
func (r Run) exclusiveKey(j Job) string {
	if j.Exclusive == "" {
		return ""
	}
	if j.Isolation == IsolationWorktree {
		return r.Project + "/" + WorktreeBranch(r.ID, j.ID) + "/" + j.Exclusive
	}
	return r.Project + "/" + j.Exclusive
}

// ready picks the jobs that may start now: pending, everything they need is
// done, within the run's parallel limit, and not after an exclusive lock
// someone else holds. held carries the locks taken by jobs already running
// elsewhere (other runs, this same pass), and is extended as jobs are picked.
// Declaration order decides, so the same graph always starts in the same order.
func (r Run) ready(held map[string]bool) []Job {
	if r.Status != Running {
		return nil
	}
	free := r.MaxPar - r.running()
	var out []Job
	for _, j := range r.Jobs {
		if free <= 0 {
			break
		}
		if j.Status != JobPending || !r.NeedsMet(j) {
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

// NeedsMet reports whether everything a job waits for is done.
func (r Run) NeedsMet(j Job) bool {
	for _, need := range j.Needs {
		i, ok := r.JobIndex(need)
		if !ok || r.Jobs[i].Status != JobDone {
			return false
		}
	}
	return true
}

// Waiting lists what a pending job is still waiting for, which is what the
// Agents view and `loods run status` show in the right-hand column.
func (r Run) Waiting(j Job) []string {
	var out []string
	for _, need := range j.Needs {
		if i, ok := r.JobIndex(need); ok && r.Jobs[i].Status != JobDone {
			out = append(out, need)
		}
	}
	return out
}

// LockHolder names the *other* running job of this run that holds the exclusive
// lock j needs, if any. Only a pending job is ever asked, but a job is not
// waiting for itself even when it is the one running.
func (r Run) LockHolder(j Job) string {
	k := r.exclusiveKey(j)
	if k == "" {
		return ""
	}
	for _, other := range r.Jobs {
		if other.ID != j.ID && other.Status == JobRunning && r.exclusiveKey(other) == k {
			return other.ID
		}
	}
	return ""
}

// locks are the exclusive keys this run holds right now, so the scheduler can
// collect them across runs before it picks anything.
func (r Run) locks() []string {
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
func (r *Run) settle() bool {
	changed := false
	for again := true; again; {
		again = false
		for i, j := range r.Jobs {
			if j.Status != JobPending {
				continue
			}
			for _, need := range j.Needs {
				k, ok := r.JobIndex(need)
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
	if r.Status != Running || len(r.Jobs) == 0 {
		return changed
	}
	failed := false
	for _, j := range r.Jobs {
		if !j.Over() {
			return changed
		}
		if j.Status != JobDone {
			failed = true
		}
	}
	r.Status, r.EndedAt = Done, time.Now().Truncate(time.Second)
	if failed {
		r.Status = Failed
	}
	return true
}

// validate checks the whole job list: ids usable as process ids and as `needs`
// references, no dangling need, no cycle. A graph is refused as a whole, so a
// submit either lands complete or not at all.
func (r *Run) validate() error {
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
		if err := ValidID(j.ID); err != nil {
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
		j.Isolation = strings.ToLower(strings.TrimSpace(j.Isolation))
		switch j.Isolation {
		case IsolationShared, IsolationWorktree:
		default:
			return fmt.Errorf("job %s: isolation %q is not a thing; use %q or leave it out",
				j.ID, j.Isolation, IsolationWorktree)
		}
		if j.Status == "" {
			j.Status = JobPending
		}
		if err := j.validateSteps(); err != nil {
			return err
		}
		if err := j.validateOwns(); err != nil {
			return err
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
	if err := r.acyclic(); err != nil {
		return err
	}
	return r.ownersDisjoint()
}

// acyclic reports the first cycle it finds, naming the jobs in it: a graph that
// can never run is a mistake in the orchestrator's plan, and it has to read why.
func (r *Run) acyclic() error {
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
		i, _ := r.JobIndex(id)
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

// ValidID keeps job ids (and, through `loods new`, project names) to what can
// go in a process id and be typed in a `loods run done <run>:<job>` ref.
func ValidID(id string) error {
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

// ParseRef reads "<run>:<job>", the ref an agent is given on its command line
// and types back when it reports in.
func ParseRef(ref string) (run, job string, err error) {
	run, job, ok := strings.Cut(strings.TrimSpace(ref), ":")
	if !ok || run == "" || job == "" {
		return "", "", fmt.Errorf("%q is not a job: use <run>:<job>, like 4f2a1c:stories", ref)
	}
	return run, job, nil
}
