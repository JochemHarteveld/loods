package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

//go:embed all:web/dist
var webDist embed.FS

type Server struct {
	root, archive string
	depth         int
	port          int
	devOrigins    []string // extra websocket origins (vite dev server) when started with --dev
	cfgPath       string

	procs  *Manager
	plans  *PlanStore
	claims *ClaimStore
	claude *claudeIndex
	gh     *githubPoller
	gitlog *gitLog
	grave  graveCache

	mu        sync.RWMutex
	items     []*Item
	stacks    []Stack
	cfgErr    string
	githubOff bool
	snapshot  []byte // JSON sent to clients, cached between scans
	procsJSON []byte
	subs      map[*sub]struct{}

	trigger chan struct{}
	ghKick  chan struct{}
}

// sub is one connected window; each flag is set when that kind of data changed.
type sub struct{ snap, procs chan struct{} }

type Snapshot struct {
	Root        string    `json:"root"`
	ScannedAt   time.Time `json:"scanned_at"`
	Scanning    bool      `json:"scanning"`
	ConfigPath  string    `json:"config_path"`
	ConfigError string    `json:"config_error,omitempty"`
	Projects    []*Item   `json:"projects"`
	Stacks      []Stack   `json:"stacks"`
	// Plans and Claude are keyed by project rel.
	Plans      map[string]Plan          `json:"plans"`
	PlansPath  string                   `json:"plans_path"`
	PlansError string                   `json:"plans_error,omitempty"`
	Claude     map[string]ClaudeSummary `json:"claude"`
	// Claims are keyed "<project rel>#<task number>": which agent is on which task.
	Claims map[string]Claim `json:"claims"`
	// GitHub is keyed by project rel; GitHubStatus says why it is empty.
	GitHub       map[string]GitHubInfo `json:"github"`
	GitHubStatus string                `json:"github_status,omitempty"`
}

type ProcsEvent struct {
	Procs        json.RawMessage `json:"procs"`
	MemTotal     int64           `json:"mem_total"`
	MemAvailable int64           `json:"mem_available"`
}

func newServer(root, archive string, depth, port int, stateFile string) *Server {
	s := &Server{
		root: root, archive: archive, depth: depth, port: port,
		cfgPath: configPath(),
		plans:   newPlanStore(plansPath()),
		claims:  newClaimStore(claimsPath()),
		claude:  newClaudeIndex(claudeProjectsDir()),
		subs:    map[*sub]struct{}{},
		trigger: make(chan struct{}, 1),
		ghKick:  make(chan struct{}, 1),
		gh:      newGitHubPoller(),
	}
	s.plans.Refresh()
	s.claims.Refresh()
	s.procs = newManager(stateFile, s.publishProcs)
	s.gitlog = &gitLog{path: gitLogPath(filepath.Dir(stateFile))}
	return s
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

// statsLoop refreshes process RAM/ports and system memory, and picks up
// plan edits made outside the board (CLI, Claude, an editor). Claude
// transcripts are checked every fifth tick.
func (s *Server) statsLoop(every time.Duration) {
	tick := 0
	for range time.Tick(every) {
		s.procs.Stats()
		s.publishProcs()
		tick++
		plans := s.plans.Refresh()
		claims := s.claims.Refresh()
		claude := tick%5 == 0 && s.claude.Refresh()
		if plans || claims || claude {
			s.publish(false)
		}
	}
}

// githubLoop refreshes PRs, issues and CI every few minutes, sooner after R.
func (s *Server) githubLoop() {
	maxAge := githubEvery
	for {
		s.mu.RLock()
		items, off := s.items, s.githubOff
		s.mu.RUnlock()
		if items != nil && !off && s.gh.refresh(items, maxAge) {
			s.publish(false)
		}
		maxAge = githubEvery
		select {
		case <-time.After(time.Minute):
		case <-s.ghKick:
			maxAge = 30 * time.Second
		}
	}
}

// indexClaude reads all transcripts once at startup, off the scan path.
func (s *Server) indexClaude() {
	if s.claude.Refresh() {
		s.publish(false)
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
	cfg, err := loadConfig(s.cfgPath)
	cfgErr := ""
	if err != nil {
		cfgErr = err.Error()
	}
	applyConfig(board, cfg)
	stacks := findStacks(s.root, board, cfg)
	s.claude.Refresh() // skipped while the startup index is still running
	s.mu.Lock()
	first := s.items == nil
	s.items, s.stacks, s.cfgErr = board, stacks, cfgErr
	s.githubOff = cfg.GitHub != nil && !*cfg.GitHub
	s.mu.Unlock()
	// A renamed or archived project should not keep a task claimed forever.
	rels := make([]string, 0, len(board))
	for _, it := range board {
		rels = append(rels, it.Rel)
	}
	s.claims.ReleaseGone(rels)
	s.publish(false)
	if first {
		notify(s.ghKick)
	}
}

// publish rebuilds the snapshot and wakes subscribers if anything changed.
func (s *Server) publish(scanning bool) {
	plans, plansErr := s.plans.All()
	claims := s.claims.All()
	github, ghStatus := s.gh.snapshot()
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := Snapshot{
		Root: s.root, ScannedAt: time.Now(), Scanning: scanning, ConfigPath: s.cfgPath, ConfigError: s.cfgErr,
		Projects: s.items, Stacks: s.stacks,
		Plans: plans, PlansPath: s.plans.path, Claude: map[string]ClaudeSummary{}, Claims: claims,
		GitHub: github, GitHubStatus: ghStatus,
	}
	if s.githubOff {
		snap.GitHub, snap.GitHubStatus = map[string]GitHubInfo{}, "off in config.yaml"
	}
	if plansErr != nil {
		snap.PlansError = plansErr.Error()
	}
	now := time.Now()
	for rel, list := range s.claude.Sessions(s.items) {
		snap.Claude[rel] = summarize(list, now)
	}
	if snap.Projects == nil {
		snap.Projects = []*Item{}
	}
	if snap.Stacks == nil {
		snap.Stacks = []Stack{}
	}
	b, _ := json.Marshal(snap)
	if s.snapshot != nil && sameExceptTime(s.snapshot, b) {
		return
	}
	s.snapshot = b
	for sb := range s.subs {
		notify(sb.snap)
	}
}

func (s *Server) publishProcs() {
	total, avail := memInfo()
	ev := ProcsEvent{Procs: s.procs.JSON(), MemTotal: total, MemAvailable: avail &^ (64<<20 - 1)} // 64 MB steps: no event per byte of drift
	b, _ := json.Marshal(ev)
	s.mu.Lock()
	defer s.mu.Unlock()
	if bytes.Equal(b, s.procsJSON) {
		return
	}
	s.procsJSON = b
	for sb := range s.subs {
		notify(sb.procs)
	}
}

func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
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
		notify(s.ghKick)
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("POST /api/procs/start", s.startProc)
	mux.HandleFunc("POST /api/procs/{action}", s.procAction)
	mux.HandleFunc("GET /api/procs/term", s.term)
	mux.HandleFunc("POST /api/plan", s.updatePlan)
	mux.HandleFunc("POST /api/claims/release", s.releaseClaim)
	mux.HandleFunc("GET /api/graveyard", s.graveyard)
	mux.HandleFunc("POST /api/graveyard/undo", s.unbury)
	mux.HandleFunc("POST /api/graveyard/{action}", s.bury)
	mux.HandleFunc("POST /api/git/{action}", s.gitOp)
	mux.HandleFunc("GET /api/claude", func(w http.ResponseWriter, r *http.Request) {
		rel := r.URL.Query().Get("id")
		if s.find(rel) == nil {
			http.Error(w, "unknown project", http.StatusNotFound)
			return
		}
		s.mu.RLock()
		items := s.items
		s.mu.RUnlock()
		list := s.claude.Sessions(items)[rel]
		writeJSON(w, append([]ClaudeSession{}, list[:min(len(list), 25)]...))
	})
	dist, _ := fs.Sub(webDist, "web/dist")
	mux.Handle("/", http.FileServerFS(dist))
	return s.guard(mux)
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
	undo, _ := s.gitlog.lastBatch(it.Path)
	writeJSON(w, branchesWithUndo{BranchList: bl, Undo: undo})
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

// startProc runs a project command or a stack. Like open, the client only
// names things loods detected or the user configured; it never sends a command line.
func (s *Server) startProc(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string `json:"project"`
		Command string `json:"command"` // empty: the project's default
		Stack   string `json:"stack"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var err error
	if req.Stack != "" {
		err = s.startStack(req.Stack)
	} else {
		var spec Spec
		if spec, err = s.projectSpec(req.Project, req.Command); err == nil {
			err = s.procs.Start(spec)
		}
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) projectSpec(rel, command string) (Spec, error) {
	it := s.find(rel)
	if it == nil {
		return Spec{}, errors.New("unknown project " + rel)
	}
	if command == "" {
		command = it.DefaultCommand
	}
	i := slices.IndexFunc(it.Commands, func(c Command) bool { return c.Name == command })
	if i < 0 {
		return Spec{}, fmt.Errorf("%s has no command %q", it.Name, command)
	}
	c := it.Commands[i]
	return Spec{
		ID: it.Rel + "#" + c.Name, Project: it.Rel, Name: c.Name, Run: c.Run,
		Dir: filepath.Join(it.Path, c.Dir), Keys: c.Keys, URL: c.URL,
	}, nil
}

func (s *Server) startStack(name string) error {
	s.mu.RLock()
	i := slices.IndexFunc(s.stacks, func(st Stack) bool { return st.Name == name })
	var st Stack
	if i >= 0 {
		st = s.stacks[i]
	}
	s.mu.RUnlock()
	if i < 0 {
		return errors.New("unknown stack " + name)
	}
	if st.Run != "" {
		if _, err := os.Stat(st.Dir); err != nil {
			return err
		}
		return s.procs.Start(Spec{ID: "stack:" + st.Name, Name: st.Name, Run: st.Run, Dir: st.Dir})
	}
	var errs []error
	for _, ref := range st.Commands {
		rel, cmd, _ := strings.Cut(ref, ":")
		spec, err := s.projectSpec(rel, cmd)
		if err == nil {
			err = s.procs.Start(spec)
		}
		if err != nil && !strings.Contains(err.Error(), "already running") {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Server) procAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Data string `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var err error
	switch r.PathValue("action") {
	case "stop":
		err = s.procs.Stop(req.ID)
	case "restart":
		err = s.procs.Restart(req.ID)
	case "remove":
		err = s.procs.Remove(req.ID)
	case "input":
		err = s.procs.Input(req.ID, []byte(req.Data))
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) updatePlan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string    `json:"project"`
		Patch   PlanPatch `json:"patch"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if s.find(req.Project) == nil {
		http.Error(w, "unknown project", http.StatusNotFound)
		return
	}
	p, err := s.plans.Update(req.Project, func(p *Plan) error {
		req.Patch.apply(p, "you")
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.publish(false)
	writeJSON(w, p)
}

// releaseClaim drops an agent's claim from the board, for when a session died and
// left a task looking like someone is on it.
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

// term streams a process terminal over a websocket: binary frames out
// (raw pty bytes), JSON messages in ({"type":"input","data":…} or {"type":"resize",…}).
func (s *Server) term(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	backlog, ch, detach, ok := s.procs.Attach(id)
	if !ok {
		http.Error(w, "unknown process", http.StatusNotFound)
		return
	}
	defer detach()
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.devOrigins})
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go func() {
		defer cancel()
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var msg struct {
				Type       string `json:"type"`
				Data       string `json:"data"`
				Cols, Rows int
			}
			if json.Unmarshal(data, &msg) != nil {
				continue
			}
			switch msg.Type {
			case "input":
				s.procs.Input(id, []byte(msg.Data))
			case "resize":
				s.procs.Resize(id, msg.Cols, msg.Rows)
			}
		}
	}()

	if err := c.Write(ctx, websocket.MessageBinary, backlog); err != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case b, ok := <-ch:
			if !ok {
				c.Close(websocket.StatusNormalClosure, "detached")
				return
			}
			// Programs redraw in bursts of tiny writes (docker compose's menu bar:
			// a dozen writes over ~2ms per log line). Sent one by one, the browser
			// paints half-finished redraws, which flickers. So gather output until
			// it goes quiet briefly and send it as one frame. Chunks are shared
			// between viewers, so build a fresh buffer.
			b = append([]byte(nil), b...)
			flush := time.After(16 * time.Millisecond)
			for gather := true; gather && len(b) < 1<<20; {
				quiet := time.NewTimer(3 * time.Millisecond)
				select {
				case next, ok := <-ch:
					if !ok {
						gather = false
						break
					}
					if bytes.HasPrefix(next, []byte("\x1bc")) {
						b = b[:0] // a resync replaces everything before it
					}
					b = append(b, next...)
				case <-quiet.C:
					gather = false
				case <-flush:
					gather = false
				case <-ctx.Done():
					return
				}
				quiet.Stop()
			}
			if err := c.Write(ctx, websocket.MessageBinary, b); err != nil {
				return
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// listen binds to loopback only.
func (s *Server) listen() (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
}
