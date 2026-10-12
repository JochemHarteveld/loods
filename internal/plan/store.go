package plan

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"

	"loods/internal/store"
)

const header = `# loods plans: status, priority, next step, notes, tasks and a log per project
# (keys are paths relative to ~/Projects). Written by the board, ` + "`loods plan`" + ` and
# Claude's /wrapup. Hand edits are picked up within seconds; comments are not kept.
# status: idea | active | paused | shipped | dead    priority: 1 (high) … 3 (low)
# tasks: a checklist; "[x] " is done, "[~] " is in progress, no box is to do.
`

// doc is the file: a plan per project, keyed by path relative to the root.
type doc struct {
	Projects map[string]*Plan `yaml:"projects"`
}

type codec struct{}

func (codec) Empty() *doc { return &doc{Projects: map[string]*Plan{}} }

func (c codec) Decode(b []byte) (*doc, error) {
	d := c.Empty()
	if err := yaml.Unmarshal(b, d); err != nil {
		return d, err
	}
	if d.Projects == nil {
		d.Projects = map[string]*Plan{}
	}
	return d, nil
}

func (codec) Encode(d *doc) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(header + "\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(d); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}

// Store reads and writes plans.yaml. Every change goes through an exclusive
// lock, so the server, the CLI and Claude never lose each other's edits.
type Store struct {
	file *store.File[*doc]
}

func NewStore(path string) *Store {
	return &Store{file: store.NewFile(path, codec{})}
}

func (s *Store) Path() string { return s.file.Path() }

// Refresh re-reads the file when it changed on disk and reports whether it did.
func (s *Store) Refresh() bool { return s.file.Refresh() }

// All returns a copy of every plan, plus the error of the last read: a broken
// file keeps the last good plans on screen and says so.
func (s *Store) All() (map[string]Plan, error) {
	d, err := s.file.Doc()
	out := make(map[string]Plan, len(d.Projects))
	for rel, p := range d.Projects {
		if p != nil {
			out[rel] = *p
		}
	}
	return out, err
}

func (s *Store) Get(rel string) Plan {
	d, _ := s.file.Doc()
	if p := d.Projects[rel]; p != nil {
		return *p
	}
	return Plan{}
}

// Update applies fn to one project's plan: lock, re-read, change, validate,
// write. A plan that ends up empty leaves the file altogether.
func (s *Store) Update(rel string, fn func(*Plan) error) (Plan, error) {
	var out Plan
	err := s.file.Update(func(d *doc) error {
		p := Plan{}
		if cur := d.Projects[rel]; cur != nil {
			p = *cur
		}
		if err := fn(&p); err != nil {
			return err
		}
		if err := p.validate(); err != nil {
			return err
		}
		if p.Empty() {
			delete(d.Projects, rel)
		} else {
			p.Updated = time.Now().Truncate(time.Second)
			d.Projects[rel] = &p
		}
		out = p
		return nil
	})
	var broken *store.ReadError
	if errors.As(err, &broken) {
		return Plan{}, fmt.Errorf("%w (fix it by hand first)", err)
	}
	if err != nil {
		return Plan{}, err
	}
	return out, nil
}
