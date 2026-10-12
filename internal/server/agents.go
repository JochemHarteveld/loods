package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"loods/internal/plan"
	"loods/internal/project"
	"loods/internal/run"
	"loods/internal/scaffold"
)

// startAgent hands one task of a planboard to a Claude session. Like
// startProc, the client only names a project and a task number; the command
// line is built here. The task moves to in progress straight away, so the
// board shows the work as taken the moment the terminal opens.
func (s *Server) startAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string `json:"project"`
		Task    int    `json:"task"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	it := s.find(req.Project)
	if it == nil {
		http.Error(w, "unknown project", http.StatusNotFound)
		return
	}
	p := s.plans.Get(it.Rel)
	i, ok := p.TaskByID(req.Task)
	if !ok {
		http.Error(w, fmt.Sprintf("%s has no task #%d", it.Rel, req.Task), http.StatusNotFound)
		return
	}
	task := p.Tasks[i]
	if err := s.agents.Available(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	spec := s.agents.TaskSpec(it, task)
	if err := s.procs.Start(spec); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if task.State != "doing" {
		if _, err := s.plans.Update(it.Rel, func(p *plan.Plan) error {
			p.SetState(task.ID, "doing")
			p.AddLog("you", fmt.Sprintf("#%d handed to an agent: %s", task.ID, task.Text))
			return nil
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	s.publish(false)
	writeJSON(w, map[string]any{"id": spec.ID, "task": task.ID})
}

// startRun is the board's "new project" button and `loods new`: it takes a goal
// and a project that already exists on the board, makes the run and starts the
// orchestrator session in it. The client never sends a command line, same as
// startProc and startAgent.
func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string `json:"project"`
		Goal    string `json:"goal"`
		MaxPar  int    `json:"max_parallel"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	it := s.find(req.Project)
	if it == nil {
		// `loods new` creates the folder and asks straight away, so the board
		// may not have seen it yet. One scan, then decide.
		s.rescan()
		it = s.find(req.Project)
	}
	if it == nil {
		http.Error(w, "unknown project", http.StatusNotFound)
		return
	}
	if err := s.agents.Available(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	r2, err := s.orchestrate(it, req.Goal, req.MaxPar)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.publish(false)
	writeJSON(w, r2)
}

// orchestrate makes the run and starts the session that owns it.
func (s *Server) orchestrate(it *project.Item, goal string, maxPar int) (run.Run, error) {
	r, err := s.runs.New(it.Rel, goal, maxPar)
	if err != nil {
		return run.Run{}, err
	}
	spec := s.agents.OrchestratorSpec(it, r)
	if err := s.procs.Start(spec); err != nil {
		return run.Run{}, err
	}
	r, err = s.runs.Attach(r.ID, it.Rel, spec.ID)
	if err != nil {
		return run.Run{}, err
	}
	s.plans.Update(it.Rel, func(p *plan.Plan) error {
		p.AddLog("you", "run "+r.ID+" handed to an orchestrator: "+r.Goal)
		return nil
	})
	return r, nil
}

// newProject is the board's + button: `loods new` without the terminal. The
// folder, the repository and the plan are made here, then an orchestrator is
// started in it, so the card appears on the board and fills itself in.
func (s *Server) newProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Group  string `json:"group"`
		Goal   string `json:"goal"`
		MaxPar int    `json:"max_parallel"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.agents.Available(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rel, _, err := scaffold.New(s.root, req.Group, req.Name, req.Goal, s.plans)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.rescan() // the folder is new: put it on the board before using it
	it := s.find(rel)
	if it == nil {
		http.Error(w, "created "+rel+" but it is not on the board", http.StatusInternalServerError)
		return
	}
	r2, err := s.orchestrate(it, req.Goal, req.MaxPar)
	if err != nil {
		// The project exists; say so, so the user is not left guessing.
		http.Error(w, "created "+rel+", but no agent was started: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.publish(false)
	writeJSON(w, r2)
}

// cancelRun stops a run from the board: no more work is handed out and the
// sessions it started are stopped.
func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Run  string `json:"run"`
		Note string `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	r2, err := s.runs.Cancel(req.Run, req.Note)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.stopCancelled()
	s.publish(false)
	writeJSON(w, r2)
}
