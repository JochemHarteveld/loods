package server

import (
	"encoding/json"
	"net/http"

	"loods/internal/plan"
)

func (s *Server) updatePlan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string     `json:"project"`
		Patch   plan.Patch `json:"patch"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if s.find(req.Project) == nil {
		http.Error(w, "unknown project", http.StatusNotFound)
		return
	}
	p, err := s.plans.Update(req.Project, func(p *plan.Plan) error {
		req.Patch.Apply(p, "you")
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.publish(false)
	writeJSON(w, p)
}

// releaseClaim drops an agent's claim from the board, for when a session died
// and left a task looking like someone is on it.
func (s *Server) releaseClaim(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string `json:"project"`
		Task    int    `json:"task"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if s.find(req.Project) == nil {
		http.Error(w, "unknown project", http.StatusNotFound)
		return
	}
	c, had, err := s.claims.Release(req.Project, req.Task)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publish(false)
	writeJSON(w, map[string]any{"released": had, "claim": c})
}
