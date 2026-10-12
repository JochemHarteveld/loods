package server

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"time"

	"loods/internal/claude"
	"loods/internal/desktop"
	"loods/internal/gitx"
	"loods/internal/project"
	"loods/web"
)

// Handler is the whole API plus the window itself, behind the guard below.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "loods") })
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("GET /api/branches", s.branches)
	mux.HandleFunc("GET /api/claude", s.claudeSessions)
	mux.HandleFunc("POST /api/open", s.open)
	mux.HandleFunc("POST /api/rescan", s.rescanNow)
	mux.HandleFunc("POST /api/agents/start", s.startAgent)
	mux.HandleFunc("POST /api/projects/new", s.newProject)
	mux.HandleFunc("POST /api/runs/start", s.startRun)
	mux.HandleFunc("POST /api/runs/cancel", s.cancelRun)
	mux.HandleFunc("POST /api/deploys/start", s.startDeploy)
	mux.HandleFunc("GET /api/deploys/log", s.deployLog)
	mux.HandleFunc("POST /api/deploys/{action}", s.deployAction)
	mux.HandleFunc("POST /api/procs/start", s.startProc)
	mux.HandleFunc("POST /api/procs/{action}", s.procAction)
	mux.HandleFunc("GET /api/procs/term", s.term)
	mux.HandleFunc("POST /api/plan", s.updatePlan)
	mux.HandleFunc("POST /api/claims/release", s.releaseClaim)
	mux.HandleFunc("GET /api/graveyard", s.graveyard)
	mux.HandleFunc("POST /api/graveyard/undo", s.unbury)
	mux.HandleFunc("POST /api/graveyard/{action}", s.bury)
	mux.HandleFunc("POST /api/git/{action}", s.gitOp)
	dist, _ := fs.Sub(web.Dist, "dist")
	mux.Handle("/", http.FileServerFS(dist))
	return s.guard(mux)
}

// Listen binds to loopback only.
func (s *Server) Listen() (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
}

// guard keeps other websites out: this server launches programs, so it only
// answers requests addressed to localhost (blocks DNS rebinding), and every
// state-changing request must carry X-Loods, which a cross-origin page cannot
// send without a CORS preflight we never approve. The terminal websocket is
// covered by the Host check plus the library's same-origin check.
func (s *Server) guard(next http.Handler) http.Handler {
	allowed := map[string]bool{
		fmt.Sprintf("127.0.0.1:%d", s.port): true,
		fmt.Sprintf("localhost:%d", s.port): true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[r.Host] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Loods") != "1" {
			http.Error(w, "missing X-Loods header", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// events is the one stream to the window: a snapshot whenever the board
// changed, process state whenever that did, and a ping to keep it open.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	sb := &sub{snap: make(chan struct{}, 1), procs: make(chan struct{}, 1)}
	s.mu.Lock()
	s.subs[sb] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, sb)
		s.mu.Unlock()
	}()

	send := func(event string, field *[]byte) {
		s.mu.RLock()
		b := *field
		s.mu.RUnlock()
		if b != nil {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
			fl.Flush()
		}
	}
	s.mu.RLock()
	noProcs := s.procsJSON == nil
	s.mu.RUnlock()
	if noProcs {
		s.publishProcs()
	}
	send("snapshot", &s.snapshot)
	send("procs", &s.procsJSON)
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sb.snap:
			send("snapshot", &s.snapshot)
		case <-sb.procs:
			send("procs", &s.procsJSON)
		case <-keepalive.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

func (s *Server) rescanNow(w http.ResponseWriter, r *http.Request) {
	s.requestScan()
	notify(s.ghKick)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) branches(w http.ResponseWriter, r *http.Request) {
	it := s.find(r.URL.Query().Get("id"))
	if it == nil || it.Kind != project.KindGit {
		http.Error(w, "unknown git project", http.StatusNotFound)
		return
	}
	bl, err := gitx.Branches(it.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	undo, _ := s.cleanup.LastBatch(it.Path)
	writeJSON(w, branchesWithUndo{BranchList: bl, Undo: undo})
}

func (s *Server) claudeSessions(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("id")
	if s.find(rel) == nil {
		http.Error(w, "unknown project", http.StatusNotFound)
		return
	}
	s.mu.RLock()
	items := s.items
	s.mu.RUnlock()
	list := s.sessions.Sessions(items)[rel]
	writeJSON(w, append([]claude.Session{}, list[:min(len(list), 25)]...))
}

func (s *Server) open(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     string `json:"id"`
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Only known projects: the client names an id, never a path or command.
	it := s.find(req.ID)
	if it == nil {
		http.Error(w, "unknown project", http.StatusNotFound)
		return
	}
	if err := desktop.Open(it, req.Target); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
