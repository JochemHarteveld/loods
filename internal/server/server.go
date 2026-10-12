// Package server is the board: one scan of every project under the root, kept
// current, pushed to the window over server-sent events, plus the endpoints
// that act on what it shows.
//
// Two rules run through all of it. The client only ever names things loods
// found or the user configured — a project id, a command name, a task number —
// and never a path or a command line, which is built here. And anything that
// has to outlive a request (a dev server, an agent session) belongs to the
// process manager, not to the request that asked for it.
package server

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"sync"
	"time"

	"loods/internal/agent"
	"loods/internal/claim"
	"loods/internal/claude"
	"loods/internal/config"
	"loods/internal/deploy"
	"loods/internal/github"
	"loods/internal/gitops"
	"loods/internal/plan"
	"loods/internal/proc"
	"loods/internal/project"
	"loods/internal/run"
	"loods/internal/xdg"
)

// Options is how the board is set up. Everything below DevOrigins defaults to
// the real thing; a test can put a stand-in in its place.
type Options struct {
	Root       string
	Archive    string
	Depth      int
	Port       int
	StateFile  string
	DevOrigins []string // extra websocket origins (the vite dev server) with --dev

	Procs    Processes
	Plans    Plans
	Claims   Claims
	Runs     Runs
	Deploys  Deploys
	Agents   Agents
	Sessions Sessions
	GitHub   GitHubSource
	Cleanup  Cleanup
}

type Server struct {
	root, archive string
	depth         int
	port          int
	devOrigins    []string
	cfgPath       string

	procs    Processes
	plans    Plans
	claims   Claims
	runs     Runs
	deploys  Deploys
	agents   Agents
	sessions Sessions
	gh       GitHubSource
	cleanup  Cleanup
	grave    graveCache

	mu        sync.RWMutex
	items     []*project.Item
	stacks    []config.Stack
	cfgErr    string
	githubOff bool
	snapshot  []byte // JSON sent to clients, cached between scans
	procsJSON []byte
	subs      map[*sub]struct{}

	trigger chan struct{}
	ghKick  chan struct{}
}

// sub is one connected window; each channel is poked when that kind of data changed.
type sub struct{ snap, procs chan struct{} }

// Snapshot is the whole board as one message. Everything keyed by project rel,
// so the window can put the pieces together itself.
type Snapshot struct {
	Root        string          `json:"root"`
	ScannedAt   time.Time       `json:"scanned_at"`
	Scanning    bool            `json:"scanning"`
	ConfigPath  string          `json:"config_path"`
	ConfigError string          `json:"config_error,omitempty"`
	Projects    []*project.Item `json:"projects"`
	Stacks      []config.Stack  `json:"stacks"`

	Plans      map[string]plan.Plan      `json:"plans"`
	PlansPath  string                    `json:"plans_path"`
	PlansError string                    `json:"plans_error,omitempty"`
	Claude     map[string]claude.Summary `json:"claude"`
	// Claims are keyed "<project rel>#<task number>": which agent is on which task.
	Claims map[string]claim.Claim `json:"claims"`
	// Runs are keyed by run id: an orchestrator and the jobs it handed out.
	Runs map[string]run.Run `json:"runs"`
	// Deploys are keyed by project rel: what is published, and where.
	Deploys map[string]deploy.Deploy `json:"deploys"`
	// GitHub is keyed by project rel; GitHubStatus says why it is empty.
	GitHub       map[string]github.Info `json:"github"`
	GitHubStatus string                 `json:"github_status,omitempty"`
}

// ProcsEvent is the other message: what is running, and how much room is left.
type ProcsEvent struct {
	Procs        json.RawMessage `json:"procs"`
	MemTotal     int64           `json:"mem_total"`
	MemAvailable int64           `json:"mem_available"`
}

func New(o Options) *Server {
	s := &Server{
		root: o.Root, archive: o.Archive, depth: o.Depth, port: o.Port,
		devOrigins: o.DevOrigins,
		cfgPath:    xdg.ConfigPath(),
		plans:      o.Plans,
		claims:     o.Claims,
		runs:       o.Runs,
		deploys:    o.Deploys,
		agents:     o.Agents,
		sessions:   o.Sessions,
		gh:         o.GitHub,
		cleanup:    o.Cleanup,
		procs:      o.Procs,
		subs:       map[*sub]struct{}{},
		trigger:    make(chan struct{}, 1),
		ghKick:     make(chan struct{}, 1),
	}
	if s.plans == nil {
		s.plans = plan.NewStore(xdg.PlansPath())
	}
	if s.claims == nil {
		s.claims = claim.NewStore(xdg.ClaimsPath())
	}
	if s.runs == nil {
		s.runs = run.NewStore(xdg.RunsPath())
	}
	if s.deploys == nil {
		s.deploys = deploy.NewStore(xdg.DeploysPath())
	}
	if s.agents == nil {
		s.agents = agent.New()
	}
	if s.sessions == nil {
		s.sessions = claude.NewIndex(xdg.ClaudeProjectsDir())
	}
	if s.gh == nil {
		s.gh = github.NewPoller()
	}
	if s.cleanup == nil {
		s.cleanup = gitops.NewLog(filepath.Dir(o.StateFile))
	}
	s.plans.Refresh()
	s.claims.Refresh()
	s.runs.Refresh()
	s.deploys.Refresh()
	if s.procs == nil {
		s.procs = proc.NewManager(o.StateFile, s.publishProcs)
	}
	// A job whose session did not survive the last loods must not hold up its
	// run; adopting orphans has already decided which processes are still there.
	s.runs.Reconcile(func(procID string) bool {
		p, ok := s.procs.Find(procID)
		return ok && p.Alive()
	})
	return s
}

// Procs is the process manager, so the program that owns the server can stop
// everything on the way out.
func (s *Server) Procs() Processes { return s.procs }

// ConfigPath is where the config loods read lives, for the startup line.
func (s *Server) ConfigPath() string { return s.cfgPath }

// Scan rescans on a timer and whenever a client asks. One scan at a time.
func (s *Server) Scan(every time.Duration) {
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

// Watch refreshes process RAM/ports and system memory, and picks up plan edits
// made outside the board (the CLI, Claude, an editor). Claude transcripts are
// checked every fifth tick.
func (s *Server) Watch(every time.Duration) {
	tick := 0
	for range time.Tick(every) {
		s.procs.Stats()
		s.publishProcs()
		tick++
		plans := s.plans.Refresh()
		claims := s.claims.Refresh()
		sessions := tick%5 == 0 && s.sessions.Refresh()
		runs := s.tickRuns()
		deploys := s.tickDeploys()
		if plans || claims || sessions || runs || deploys {
			s.publish(false)
		}
	}
}

// WatchGitHub refreshes PRs, issues and CI every few minutes, sooner after R.
func (s *Server) WatchGitHub() {
	maxAge := github.Every
	for {
		s.mu.RLock()
		items, off := s.items, s.githubOff
		s.mu.RUnlock()
		if items != nil && !off && s.gh.Refresh(items, maxAge) {
			s.publish(false)
		}
		maxAge = github.Every
		select {
		case <-time.After(time.Minute):
		case <-s.ghKick:
			maxAge = 30 * time.Second
		}
	}
}

// IndexSessions reads all Claude transcripts once at startup, off the scan path.
func (s *Server) IndexSessions() {
	if s.sessions.Refresh() {
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
	board := project.Board(s.root, s.depth, s.archive)
	project.ScanAll(board, false)
	cfg, err := config.Load(s.cfgPath)
	cfgErr := ""
	if err != nil {
		cfgErr = err.Error()
	}
	cfg.Apply(board)
	stacks := cfg.FindStacks(s.root, board)
	s.sessions.Refresh() // skipped while the startup index is still running
	s.mu.Lock()
	first := s.items == nil
	s.items, s.stacks, s.cfgErr = board, stacks, cfgErr
	s.githubOff = !cfg.GitHubEnabled()
	s.mu.Unlock()
	// A renamed or archived project should not keep a task claimed forever.
	s.claims.ReleaseGone(project.Rels(board))
	s.publish(false)
	if first {
		notify(s.ghKick)
	}
}

// publish rebuilds the snapshot and wakes subscribers if anything changed.
func (s *Server) publish(scanning bool) {
	plans, plansErr := s.plans.All()
	claims := s.claims.All()
	runs := s.runs.All()
	deploys := map[string]deploy.Deploy{}
	for _, d := range s.deploys.List() {
		deploys[d.Project] = d
	}
	gh, ghStatus := s.gh.Snapshot()
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := Snapshot{
		Root: s.root, ScannedAt: time.Now(), Scanning: scanning, ConfigPath: s.cfgPath, ConfigError: s.cfgErr,
		Projects: s.items, Stacks: s.stacks,
		Plans: plans, PlansPath: s.plans.Path(), Claude: map[string]claude.Summary{}, Claims: claims,
		Runs: runs, Deploys: deploys, GitHub: gh, GitHubStatus: ghStatus,
	}
	if s.githubOff {
		snap.GitHub, snap.GitHubStatus = map[string]github.Info{}, "off in config.yaml"
	}
	if plansErr != nil {
		snap.PlansError = plansErr.Error()
	}
	now := time.Now()
	for rel, list := range s.sessions.Sessions(s.items) {
		snap.Claude[rel] = claude.Summarize(list, now)
	}
	if snap.Projects == nil {
		snap.Projects = []*project.Item{}
	}
	if snap.Stacks == nil {
		snap.Stacks = []config.Stack{}
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
	total, avail := proc.MemInfo()
	// 64 MB steps: no event per byte of drift.
	ev := ProcsEvent{Procs: s.procs.JSON(), MemTotal: total, MemAvailable: avail &^ (64<<20 - 1)}
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

// find is how every handler resolves what the client named: a project id the
// board has actually seen, never a path.
func (s *Server) find(rel string) *project.Item {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, it := range s.items {
		if it.Rel == rel {
			return it
		}
	}
	return nil
}
