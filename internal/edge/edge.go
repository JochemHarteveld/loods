// Package edge is the one origin behind the Cloudflare Tunnel. Every hostname
// under the domain arrives here — the tunnel has a single wildcard rule — and
// this is what turns a Host header into the deploy it belongs to.
//
// Keeping the per-project routing here, rather than in the tunnel's own
// ingress rules, is what makes a deploy cheap: publishing a project writes a
// route, and nothing touches DNS, the certificate or the connector. The same
// reasoning puts the routes in a file instead of in memory — the board writes
// it, the proxy re-reads it when it moves, and `loods edge` can run on its own
// on another machine with no board at all.
//
// The routes file is written by the deploy store; this package only reads it.
package edge

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"loods/internal/store"
)

// Route is one deploy, as the proxy needs to see it: a hostname, and either a
// directory to serve or an upstream to proxy to. A route with neither is a
// deploy that exists on the board but is not serving anything right now, which
// is a different answer than "no such host" and is said so on the page.
//
// Status is what the board thinks of the deploy, and it never decides whether
// to serve: a rebuild that fails leaves the directory of the last good build in
// place, and keeping that up is more useful than taking the site down over a
// typo in a commit nobody has seen yet. It only writes the page for a deploy
// that has nothing to serve at all.
//
// The deploy store writes this file with more fields than these; unknown ones
// are ignored here on purpose, so the proxy's contract stays this small.
type Route struct {
	Host     string `json:"host"`               // deploy.loods.site, without a port
	Project  string `json:"project,omitempty"`  // group/project, for the status pages
	Dir      string `json:"dir,omitempty"`      // static: the directory to serve
	SPA      bool   `json:"spa,omitempty"`      // static: unknown paths get index.html
	Upstream string `json:"upstream,omitempty"` // proxy: host:port to forward to
	Status   string `json:"status,omitempty"`   // empty = serving; else stopped / failed / building
	Note     string `json:"note,omitempty"`     // why it is not serving, shown on the page
}

// Table is the routes file.
type Table struct {
	Routes []Route `json:"routes"`
}

type codec struct{}

func (codec) Empty() *Table { return &Table{} }

func (c codec) Decode(b []byte) (*Table, error) {
	t := c.Empty()
	if err := json.Unmarshal(b, t); err != nil {
		return c.Empty(), err
	}
	return t, nil
}

func (codec) Encode(t *Table) ([]byte, error) {
	b, err := json.MarshalIndent(t, "", "  ")
	return append(b, '\n'), err
}

// Store is the routes file on disk, read when it changed.
type Store struct{ file *store.File[*Table] }

func NewStore(path string) *Store { return &Store{file: store.NewFile(path, codec{})} }

func (s *Store) Path() string { return s.file.Path() }

// Refresh re-reads the file when it moved and reports whether it did. A file
// that cannot be parsed keeps the last good table: a half-written deploy
// should not take every other deploy down with it.
func (s *Store) Refresh() bool {
	changed := s.file.Refresh()
	return changed && s.file.Err() == nil
}

func (s *Store) Err() error { return s.file.Err() }

func (s *Store) Table() *Table {
	t, _ := s.file.Doc()
	return t
}

// Write replaces the routes file. Only the deploy store calls this; the proxy
// itself never writes.
func (s *Store) Write(routes []Route) error {
	return s.file.Update(func(t *Table) error {
		t.Routes = routes
		return nil
	})
}

// Server answers for every hostname in the table. It is safe to use from many
// goroutines while Refresh swaps the routes underneath.
type Server struct {
	store *Store

	mu     sync.RWMutex
	hosts  map[string]*handler
	listed []Route // what Refresh last loaded, sorted by host, for the "no such deploy" page
}

func NewServer(s *Store) *Server {
	srv := &Server{store: s, hosts: map[string]*handler{}}
	srv.Refresh()
	return srv
}

// Refresh rebuilds the routing table when the file changed, and reports
// whether anything was swapped. Building the handlers once per change rather
// than once per request is what keeps a proxied deploy from re-parsing its
// upstream URL on every hit.
func (s *Server) Refresh() bool {
	if !s.store.Refresh() && len(s.hosts) > 0 {
		return false
	}
	routes := s.store.Table().Routes
	hosts := make(map[string]*handler, len(routes))
	listed := make([]Route, 0, len(routes))
	for _, r := range routes {
		host := normalizeHost(r.Host)
		if host == "" {
			continue
		}
		r.Host = host
		hosts[host] = newHandler(r)
		listed = append(listed, r)
	}
	sort.Slice(listed, func(i, j int) bool { return listed[i].Host < listed[j].Host })

	s.mu.Lock()
	s.hosts, s.listed = hosts, listed
	s.mu.Unlock()
	return true
}

// Watch re-reads the routes every interval, the same way the board picks up
// plan edits. Stops when done is closed.
func (s *Server) Watch(every time.Duration, done <-chan struct{}) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			s.Refresh()
		}
	}
}

// Hosts is what the table currently answers for, sorted.
func (s *Server) Hosts() []Route {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Route(nil), s.listed...)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := normalizeHost(r.Host)
	s.mu.RLock()
	h, ok := s.hosts[host]
	listed := s.listed
	s.mu.RUnlock()
	if !ok {
		writeUnknownHost(w, host, listed)
		return
	}
	h.ServeHTTP(w, r)
}

// normalizeHost strips the port, a trailing dot and the case, so "App.Loods.Site:443"
// and "app.loods.site" are the same deploy.
func normalizeHost(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}

// handler is one route, resolved: a file server, a reverse proxy, or a page
// that explains why there is neither.
type handler struct {
	route Route
	files http.Handler
	proxy *httputil.ReverseProxy
}

func newHandler(r Route) *handler {
	h := &handler{route: r}
	switch {
	case r.Dir != "":
		h.files = staticHandler(r)
	case r.Upstream != "":
		h.proxy = proxyHandler(r)
		// Anything else is a known deploy with nothing behind it: stopped,
		// or a first build that never produced one. Say so rather than 404.
	}
	return h
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case h.files != nil:
		h.files.ServeHTTP(w, r)
	case h.proxy != nil:
		h.proxy.ServeHTTP(w, r)
	default:
		writeNotServing(w, h.route)
	}
}

func proxyHandler(r Route) *httputil.ReverseProxy {
	target := &url.URL{Scheme: "http", Host: r.Upstream}
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			// The app should see the hostname the visitor typed, not the
			// upstream's, so its own redirects and absolute URLs stay right.
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
			// SetXForwarded decides the protocol from the hop it can see,
			// which is plain HTTP from the tunnel. What the visitor used is
			// what Cloudflare already told us.
			if proto := pr.In.Header.Get("X-Forwarded-Proto"); proto != "" {
				pr.Out.Header.Set("X-Forwarded-Proto", proto)
			}
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeUpstreamDown(w, r, err)
		},
		FlushInterval: -1, // stream SSE and websocket upgrades straight through
	}
}
