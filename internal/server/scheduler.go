package server

import (
	"fmt"

	"loods/internal/agent"
	"loods/internal/gitops"
	"loods/internal/run"
)

// Runs are scheduled here, in the server, because only this process has the
// process manager: a session has to outlive the command that asked for it and
// show up in the dock. `loods run` only writes runs.json; this pass picks that
// up, notices sessions that ended, and starts whatever may run now.

// tickRuns is one pass of three steps, in this order: read what the agents
// wrote, turn ended sessions into finished jobs, then hand out work. Returns
// whether anything changed, so the watch loop knows to publish.
func (s *Server) tickRuns() bool {
	changed := s.runs.Refresh()
	changed = s.reapJobs() || changed
	before := s.runs.All()
	s.runs.Tick(s.startJob)
	s.stopCancelled()
	return changed || !sameRuns(before, s.runs.All())
}

// reapJobs finishes jobs whose session is over but never reported back. A job
// that says nothing is a failure, even when its shell exited 0: the run has no
// way to know what it built, and anything waiting for it would wait forever.
func (s *Server) reapJobs() bool {
	changed := false
	for _, r := range s.runs.Live() {
		for _, j := range r.Jobs {
			if j.Status != run.JobRunning || j.ProcID == "" {
				continue
			}
			p, ok := s.procs.Find(j.ProcID)
			if !ok || p.Alive() {
				continue
			}
			note := "its session ended without reporting back"
			if p.Stopped {
				note = "its session was stopped"
			} else if p.ExitCode != 0 {
				note = fmt.Sprintf("its session exited %d without reporting back", p.ExitCode)
			}
			if _, err := s.runs.Finish(r.ID, j.ID, run.JobFailed, note); err == nil {
				changed = true
			}
		}
	}
	return changed
}

// startJob is what the scheduler calls for a job it decided may run: build the
// session and hand it to the process manager, exactly like a dev server.
//
// A job that asked for isolation gets its worktree here, before its session
// exists. Returning an error is the right way to fail: Tick turns it into a
// failed job with the reason as its note, so "no commits yet to branch from"
// reaches the orchestrator instead of an agent sitting in a folder that is not
// there.
func (s *Server) startJob(r run.Run, j run.Job) (string, error) {
	it := s.find(r.Project)
	if it == nil {
		return "", fmt.Errorf("unknown project %q", r.Project)
	}
	if err := s.agents.Available(); err != nil {
		return "", err
	}
	if j.Isolated() {
		path := agent.JobWorktree(it, r, j)
		if _, err := gitops.AddWorktree(it.Path, path, run.WorktreeBranch(r.ID, j.ID)); err != nil {
			return "", err
		}
	}
	spec := s.agents.JobSpec(it, r, j)
	if err := s.procs.Start(spec); err != nil {
		return "", err
	}
	return spec.ID, nil
}

// stopCancelled stops the sessions of jobs that were cancelled while they were
// running, so `loods run cancel` really ends the work and not just the
// bookkeeping.
func (s *Server) stopCancelled() {
	for _, r := range s.runs.All() {
		if r.Status != run.Cancelled {
			continue
		}
		for _, j := range r.Jobs {
			if j.Status == run.JobCancelled && j.ProcID != "" {
				s.stopIfAlive(j.ProcID)
			}
		}
		if r.Orchestrator != "" {
			s.stopIfAlive(r.Orchestrator)
		}
	}
}

func (s *Server) stopIfAlive(procID string) {
	if p, ok := s.procs.Find(procID); ok && p.Alive() {
		s.procs.Stop(procID)
	}
}

// sameRuns compares two reads of the store on what the board shows, so a pass
// that changed nothing does not publish a snapshot.
func sameRuns(a, b map[string]run.Run) bool {
	if len(a) != len(b) {
		return false
	}
	for id, x := range a {
		y, ok := b[id]
		if !ok || x.Status != y.Status || len(x.Jobs) != len(y.Jobs) || x.Orchestrator != y.Orchestrator {
			return false
		}
		for i := range x.Jobs {
			if x.Jobs[i].Status != y.Jobs[i].Status || x.Jobs[i].Note != y.Jobs[i].Note {
				return false
			}
		}
	}
	return true
}
