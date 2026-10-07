package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"
)

// The Graveyard view: everything under the root (projects, plain folders,
// archive files) with sizes, to archive or trash what is dead. Sizes need a
// full walk including node_modules, so the result is cached for a while.

const graveTTL = 2 * time.Minute

type graveCache struct {
	mu    sync.Mutex
	at    time.Time
	items []*Item
}

type GraveyardResponse struct {
	ScannedAt time.Time `json:"scanned_at"`
	Archive   string    `json:"archive"`
	Items     []*Item   `json:"items"`
	Batches   []Batch   `json:"batches"`
}

func (s *Server) graveyard(w http.ResponseWriter, r *http.Request) {
	fresh := r.URL.Query().Get("fresh") != ""
	s.grave.mu.Lock()
	if fresh || s.grave.items == nil || time.Since(s.grave.at) > graveTTL {
		items := Discover(s.root, s.depth, s.archive)
		markDupes(items)
		scanAll(items, true)
		s.grave.items, s.grave.at = items, time.Now()
	}
	resp := GraveyardResponse{ScannedAt: s.grave.at, Archive: s.archive, Items: s.grave.items, Batches: recentBatches(s.archive, 8)}
	s.grave.mu.Unlock()
	if resp.Items == nil {
		resp.Items = []*Item{}
	}
	writeJSON(w, resp)
}

func (s *Server) invalidateGraveyard() {
	s.grave.mu.Lock()
	s.grave.items = nil
	s.grave.mu.Unlock()
}

// bury archives or trashes items named by their rel path. Like everything
// else the client sends names, never paths; they are resolved against a
// fresh discovery of the root.
func (s *Server) bury(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if action != "archive" && action != "trash" {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Rels []string `json:"rels"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || len(req.Rels) == 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	all := Discover(s.root, s.depth, s.archive)
	now := time.Now()
	batch := now.Format("20060102-150405")
	var results []OpResult
	for _, rel := range req.Rels {
		res := OpResult{Name: rel}
		i := slices.IndexFunc(all, func(it *Item) bool { return it.Rel == rel })
		switch {
		case i < 0:
			res.Error = "not found under " + s.root
		case s.procs.Running(rel):
			res.Error = "running in the Garage: stop it first"
		default:
			it := all[i]
			scanAll([]*Item{it}, false) // risks for the log
			var err error
			to := ""
			if action == "archive" {
				to, err = archiveItem(s.archive, it, now)
			} else {
				err = trashItem(it)
			}
			if err != nil {
				res.Error = err.Error()
				break
			}
			appendLog(s.archive, LogEntry{Time: now, Batch: batch, Action: action, From: it.Path, To: to, Risks: it.Risks})
			res.OK = true
			if to != "" {
				res.Note = "→ " + to
			}
		}
		results = append(results, res)
	}
	s.invalidateGraveyard()
	s.requestScan()
	writeJSON(w, results)
}

func (s *Server) unbury(w http.ResponseWriter, r *http.Request) {
	batch, results, err := undoArchive(s.archive)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if batch == "" {
		http.Error(w, "nothing to undo (trashed items: restore them from the trash)", http.StatusBadRequest)
		return
	}
	s.invalidateGraveyard()
	s.requestScan()
	writeJSON(w, results)
}

// gitOp runs a cleanup action on one repo.
func (s *Server) gitOp(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project  string   `json:"project"`
		Branches []string `json:"branches"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	it := s.find(req.Project)
	if it == nil || it.Kind != KindGit {
		http.Error(w, "unknown git project", http.StatusNotFound)
		return
	}
	var results []OpResult
	var err error
	switch r.PathValue("action") {
	case "delete-branches":
		if len(req.Branches) == 0 {
			err = errors.New("no branches given")
		} else {
			results = s.gitlog.deleteBranches(it.Path, req.Branches)
		}
	case "undo":
		results, err = s.gitlog.undo(it.Path)
	case "prune-worktrees":
		results, err = pruneWorktrees(it.Path)
	case "ignore-env":
		results, err = ignoreEnv(it.Path)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.requestScan()
	if results == nil {
		results = []OpResult{}
	}
	writeJSON(w, results)
}

// branchesWithUndo is the branch drawer's data plus the cleanup that can be undone.
type branchesWithUndo struct {
	BranchList
	Undo *UndoInfo `json:"undo,omitempty"`
}
