package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"sync"
	"time"
)

//go:embed all:web/dist
var webDist embed.FS

type Server struct {
	root, archive string
	depth         int
	port          int

	mu       sync.RWMutex
	items    []*Item
	snapshot []byte // JSON sent to clients, cached between scans
	subs     map[chan struct{}]struct{}

	trigger chan struct{}
}

type Snapshot struct {
	Root      string    `json:"root"`
	ScannedAt time.Time `json:"scanned_at"`
	Scanning  bool      `json:"scanning"`
	Projects  []*Item   `json:"projects"`
}

func newServer(root, archive string, depth, port int) *Server {
	return &Server{
		root: root, archive: archive, depth: depth, port: port,
		subs:    map[chan struct{}]struct{}{},
		trigger: make(chan struct{}, 1),
	}
}

// loop rescans on a timer and whenever a client asks. One scan at a time.
func (s *Server) loop(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		s.rescan()
		select {
		case <-t.C:
		case <-s.trigger:
		}
	}
}

func (s *Server) requestScan() {
	select {
	case s.trigger <- struct{}{}:
	default: // one already pending
	}
}

func (s *Server) rescan() {
	s.publish(true)
	items := Discover(s.root, s.depth, s.archive)
	var board []*Item
	for _, it := range items {
		if it.Kind == KindGit || it.Kind == KindProj {
			board = append(board, it)
		}
	}
	scanAll(board, false)
	s.mu.Lock()
	s.items = board
	s.mu.Unlock()
	s.publish(false)
}

// publish rebuilds the snapshot and wakes subscribers if anything changed.
func (s *Server) publish(scanning bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := Snapshot{Root: s.root, ScannedAt: time.Now(), Scanning: scanning, Projects: s.items}
	if snap.Projects == nil {
		snap.Projects = []*Item{}
	}
	b, _ := json.Marshal(snap)
	if s.snapshot != nil && sameExceptTime(s.snapshot, b) {
		return
	}
	s.snapshot = b
	for ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// sameExceptTime compares two snapshots ignoring scanned_at, so an idle board
// does not re-render every scan.
func sameExceptTime(a, b []byte) bool {
	var x, y Snapshot
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	x.ScannedAt, y.ScannedAt = time.Time{}, time.Time{}
	ja, _ := json.Marshal(x)
	jb, _ := json.Marshal(y)
	return bytes.Equal(ja, jb)
}

func (s *Server) find(rel string) *Item {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, it := range s.items {
		if it.Rel == rel {
			return it
		}
	}
	return nil
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "loods") })
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("GET /api/branches", s.branches)
	mux.HandleFunc("POST /api/open", s.open)
	mux.HandleFunc("POST /api/rescan", func(w http.ResponseWriter, r *http.Request) {
		s.requestScan()
		w.WriteHeader(http.StatusAccepted)
	})
	dist, _ := fs.Sub(webDist, "web/dist")
	mux.Handle("/", http.FileServerFS(dist))
	return s.guard(mux)
}

// guard keeps other websites out: this server launches programs, so it only
// answers requests addressed to localhost (blocks DNS rebinding), and every
// state-changing request must carry X-Loods, which a cross-origin page cannot
// send without a CORS preflight we never approve.
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

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()

	send := func() {
		s.mu.RLock()
		b := s.snapshot
		s.mu.RUnlock()
		if b != nil {
			fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", b)
			fl.Flush()
		}
	}
	send()
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			send()
		case <-keepalive.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

func (s *Server) branches(w http.ResponseWriter, r *http.Request) {
	it := s.find(r.URL.Query().Get("id"))
	if it == nil || it.Kind != KindGit {
		http.Error(w, "unknown git project", http.StatusNotFound)
		return
	}
	bl, err := listBranches(it.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, bl)
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
	if err := openTarget(it, req.Target); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// listen binds to loopback only.
func (s *Server) listen() (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
}
