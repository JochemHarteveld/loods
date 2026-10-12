// Package deploy is what is published and where it is routed.
//
// A deploy is deliberately close to a dev server: it is a build, maybe a
// process, and a hostname. That is why it reuses the process manager instead
// of growing its own — a published app gets the same terminal, output backlog,
// stop and orphan recovery as everything else loods starts, and shows up in
// the same dock.
//
// Two files, for the same reason the rest of loods has two: deploys.yaml is
// yours (what each project publishes, how it is built), deploys.json is the
// board's memory of what actually happened (hostname, port, commit, status) and
// is also the routes file `loods edge` reads. One file is both because the
// proxy needs a subset of exactly this, and keeping them in step across two
// files would be a bug waiting to happen.
package deploy

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"loods/internal/edge"
	"loods/internal/store"
)

// What a deploy is built and served as.
const (
	KindStatic  = "static"  // a built directory, served by the edge
	KindProc    = "proc"    // a long-running command on a port of its own
	KindCompose = "compose" // a compose service, routed to its published port
)

// Where a deploy stands. These are the words the board and the edge's pages
// use, so they are phrased for a person reading them on a card.
const (
	StatusBuilding = "building"
	StatusLive     = "live"
	StatusFailed   = "failed"
	StatusStopped  = "stopped"
)

// Deploy is one published project. The embedded Route is the part `loods edge`
// reads; everything else is the board's.
type Deploy struct {
	edge.Route

	Kind   string `json:"kind"`
	Port   int    `json:"port,omitempty"`    // proc: the port it was given
	ProcID string `json:"proc_id,omitempty"` // the process that builds and runs it
	Commit string `json:"commit,omitempty"`  // what was published
	Branch string `json:"branch,omitempty"`

	StartedAt time.Time `json:"started_at,omitzero"` // when this deploy began
	LiveAt    time.Time `json:"live_at,omitzero"`    // when it last came up
}

// Live reports whether the edge has something to send a visitor to. A failed
// rebuild of a static deploy is still live: the previous build is still there.
func (d Deploy) Live() bool { return d.Dir != "" || d.Upstream != "" }

// doc is the file: deploys by project, plus the shape the edge reads.
//
// The edge wants a list and the board wants to look one project up, so the
// file is written as the list the edge expects and indexed by project here.
type doc struct {
	Routes []Deploy `json:"routes"`
}

type codec struct{}

func (codec) Empty() *doc { return &doc{} }

func (c codec) Decode(b []byte) (*doc, error) {
	d := c.Empty()
	if err := json.Unmarshal(b, d); err != nil {
		return c.Empty(), err
	}
	return d, nil
}

func (codec) Encode(d *doc) ([]byte, error) {
	sort.Slice(d.Routes, func(i, j int) bool { return d.Routes[i].Host < d.Routes[j].Host })
	b, err := json.MarshalIndent(d, "", "  ")
	return append(b, '\n'), err
}

type Store struct{ file *store.File[*doc] }

func NewStore(path string) *Store { return &Store{file: store.NewFile(path, codec{})} }

func (s *Store) Path() string { return s.file.Path() }

// Refresh re-reads the file when it changed and reports whether it did.
func (s *Store) Refresh() bool {
	changed := s.file.Refresh()
	return changed && s.file.Err() == nil
}

// All returns every deploy, keyed by project.
func (s *Store) All() map[string]Deploy {
	d, _ := s.file.Doc()
	out := make(map[string]Deploy, len(d.Routes))
	for _, dep := range d.Routes {
		out[dep.Project] = dep
	}
	return out
}

// List returns every deploy, by hostname.
func (s *Store) List() []Deploy {
	d, _ := s.file.Doc()
	out := append([]Deploy(nil), d.Routes...)
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

func (s *Store) Get(rel string) (Deploy, bool) {
	d, ok := s.All()[rel]
	return d, ok
}

// Put writes one deploy, replacing what was there for that project. fn gets
// the current record — the zero value for a project that was never deployed —
// so a caller can keep what it is not changing, like the directory of the last
// good build.
func (s *Store) Put(rel string, fn func(*Deploy) error) (Deploy, error) {
	var out Deploy
	err := s.file.Update(func(d *doc) error {
		at := -1
		for i, dep := range d.Routes {
			if dep.Project == rel {
				at = i
				break
			}
		}
		dep := Deploy{}
		if at >= 0 {
			dep = d.Routes[at]
		}
		dep.Project = rel
		if err := fn(&dep); err != nil {
			return err
		}
		if dep.Host == "" {
			return fmt.Errorf("a deploy needs a hostname")
		}
		if at >= 0 {
			d.Routes[at] = dep
		} else {
			d.Routes = append(d.Routes, dep)
		}
		out = dep
		return nil
	})
	return out, err
}

// Remove drops a project's deploy altogether: the hostname stops answering.
func (s *Store) Remove(rel string) (Deploy, bool, error) {
	var gone Deploy
	var found bool
	err := s.file.Update(func(d *doc) error {
		kept := d.Routes[:0]
		for _, dep := range d.Routes {
			if dep.Project == rel {
				gone, found = dep, true
				continue
			}
			kept = append(kept, dep)
		}
		d.Routes = kept
		return nil
	})
	return gone, found, err
}

// FreePort picks a port for a proc deploy from a range of its own, so a deploy
// never collides with a dev server a project started on its usual port. A
// project keeps the port it already has: restarting a deploy should not move
// it under anything that remembered the old one.
func (s *Store) FreePort(rel string, taken func(port int) bool) (int, error) {
	if d, ok := s.Get(rel); ok && d.Port != 0 {
		return d.Port, nil
	}
	used := map[int]bool{}
	for _, d := range s.List() {
		if d.Port != 0 {
			used[d.Port] = true
		}
	}
	for p := FirstPort; p <= LastPort; p++ {
		if used[p] || (taken != nil && taken(p)) {
			continue
		}
		return p, nil
	}
	return 0, fmt.Errorf("no free port between %d and %d", FirstPort, LastPort)
}

// The range deploys live in: far from 3000/5173/8080, so a project's dev
// server and its deploy can run at the same time without either moving.
const (
	FirstPort = 8900
	LastPort  = 8999
)

// Hostname is where a project is published: its own name under the domain,
// unless deploys.yaml says otherwise.
func Hostname(sub, domain string) string {
	sub = Slug(sub)
	if sub == "" || domain == "" {
		return ""
	}
	return sub + "." + domain
}

// Slug turns a project path into something that can be a subdomain: the last
// element, lowercased, with anything that is not a letter, digit or dash
// collapsed into a dash.
func Slug(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
