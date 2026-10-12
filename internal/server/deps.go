package server

import (
	"time"

	"loods/internal/claim"
	"loods/internal/claude"
	"loods/internal/deploy"
	"loods/internal/github"
	"loods/internal/gitops"
	"loods/internal/ops"
	"loods/internal/plan"
	"loods/internal/proc"
	"loods/internal/project"
	"loods/internal/run"
)

// The server talks to the rest of loods through these, and asks each of them
// only for what a request or a loop actually needs. They exist for two
// reasons: a handler reads as what it does rather than how, and a test can put
// a stand-in behind one without starting real sessions or touching real files.

// Processes is the one thing that holds pids: everything loods started.
type Processes interface {
	Start(s proc.Spec) error
	Stop(id string) error
	Restart(id string) error
	Remove(id string) error
	Input(id string, data []byte) error
	Resize(id string, cols, rows int)
	Attach(id string) (backlog []byte, ch chan []byte, detach func(), ok bool)
	Find(id string) (proc.Proc, bool)
	Running(project string) bool
	Stats() bool
	JSON() []byte
	StopAll(timeout time.Duration)
}

// Plans is where every project stands.
type Plans interface {
	Path() string
	Refresh() bool
	All() (map[string]plan.Plan, error)
	Get(rel string) plan.Plan
	Update(rel string, fn func(*plan.Plan) error) (plan.Plan, error)
}

// Claims is which agent is on which task.
type Claims interface {
	Refresh() bool
	All() map[string]claim.Claim
	Release(project string, task int) (claim.Claim, bool, error)
	ReleaseGone(rels []string)
}

// Runs is the graphs of jobs and where they stand. The server is the only
// thing that schedules them, because only it can start sessions.
type Runs interface {
	Refresh() bool
	All() map[string]run.Run
	Get(id string) (run.Run, bool)
	Live() []run.Run
	New(project, goal string, maxPar int) (run.Run, error)
	Finish(runID, job, status, note string) (run.Run, error)
	Cancel(id, note string) (run.Run, error)
	Attach(id, project, orchestrator string) (run.Run, error)
	Reconcile(alive func(procID string) bool)
	Tick(start func(run.Run, run.Job) (procID string, err error))
}

// Deploys is what is published and where it is routed. The server is the only
// thing that publishes, because only it can start the build and keep the
// process that comes out of it.
type Deploys interface {
	Refresh() bool
	List() []deploy.Deploy
	Get(rel string) (deploy.Deploy, bool)
	Put(rel string, fn func(*deploy.Deploy) error) (deploy.Deploy, error)
	Remove(rel string) (deploy.Deploy, bool, error)
	FreePort(rel string, taken func(port int) bool) (int, error)
}

// Agents builds the Claude sessions. The client never sends a command line:
// it names a project and a task, and the session is built here.
type Agents interface {
	Available() error
	TaskSpec(it *project.Item, task plan.Task) proc.Spec
	OrchestratorSpec(it *project.Item, r run.Run) proc.Spec
	JobSpec(it *project.Item, r run.Run, j run.Job) proc.Spec
}

// Sessions is Claude's own history, read from its transcripts.
type Sessions interface {
	Refresh() bool
	Sessions(items []*project.Item) map[string][]claude.Session
}

// GitHubSource is PRs, issues and CI, as of the last poll.
type GitHubSource interface {
	Snapshot() (map[string]github.Info, string)
	Refresh(items []*project.Item, maxAge time.Duration) bool
}

// Cleanup is branch and worktree cleanup, with its undo log.
type Cleanup interface {
	DeleteBranches(repo string, names []string) []ops.Result
	Undo(repo string) ([]ops.Result, error)
	LastBatch(repo string) (*gitops.UndoInfo, []gitops.Op)
}
