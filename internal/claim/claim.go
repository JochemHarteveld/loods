// Package claim says which agent is working on which task right now.
//
// Claims are kept apart from plans.yaml on purpose: a plan is durable, a claim
// is not (a session ends, a laptop closes, Claude is interrupted). They live
// next to procs.json in the state dir and carry a heartbeat, so an abandoned
// claim can be told from a live one instead of making the board lie about what
// is being worked on.
package claim

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"loods/internal/store"
	"loods/internal/textfmt"
)

const (
	staleAfter = 10 * time.Minute // no heartbeat for this long: probably abandoned
	maxAge     = 48 * time.Hour   // dropped from the file altogether
	maxClaims  = 500
)

type Claim struct {
	Project   string    `json:"project"`
	Task      int       `json:"task"`
	Text      string    `json:"text,omitempty"` // copy of the task text, for the board
	Agent     string    `json:"agent"`          // "claude", or whoever took it
	Session   string    `json:"session,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	ClaimedAt time.Time `json:"claimed_at"`
	Beat      time.Time `json:"last_heartbeat"`
	Stale     bool      `json:"stale,omitempty"` // filled in when read, never stored
}

// Key is how a claim is addressed everywhere: "<project rel>#<task number>".
func Key(project string, task int) string { return fmt.Sprintf("%s#%d", project, task) }

func (c Claim) Key() string { return Key(c.Project, c.Task) }

func (c Claim) stale(now time.Time) bool { return now.Sub(c.Beat) > staleAfter }

// held reports whether this claim blocks someone else from taking the task: a
// live claim by another session (or, without sessions, another agent).
func (c Claim) held(by Claim, now time.Time) bool {
	if c.stale(now) {
		return false
	}
	if c.Session != "" && by.Session != "" {
		return c.Session != by.Session
	}
	return c.Agent != by.Agent
}

// doc is the file: every claim, keyed by Key.
type doc struct {
	Claims map[string]Claim `json:"claims"`
}

type codec struct{}

func (codec) Empty() *doc { return &doc{Claims: map[string]Claim{}} }

func (c codec) Decode(b []byte) (*doc, error) {
	d := c.Empty()
	if err := json.Unmarshal(b, d); err != nil {
		return d, err
	}
	if d.Claims == nil {
		d.Claims = map[string]Claim{}
	}
	return d, nil
}

func (codec) Encode(d *doc) ([]byte, error) {
	b, err := json.MarshalIndent(d, "", "  ")
	return append(b, '\n'), err
}

type Store struct {
	file *store.File[*doc]
}

func NewStore(path string) *Store { return &Store{file: store.NewFile(path, codec{})} }

func (s *Store) Path() string { return s.file.Path() }

// Refresh re-reads the file when it changed on disk and reports whether it did,
// so the server picks up claims made by the CLI. A file it cannot read is kept
// quiet: the board shows the last good claims.
func (s *Store) Refresh() bool {
	changed := s.file.Refresh()
	return changed && s.file.Err() == nil
}

// All returns every claim, keyed "<project>#<task>", with Stale filled in.
func (s *Store) All() map[string]Claim {
	now := time.Now()
	d, _ := s.file.Doc()
	out := make(map[string]Claim, len(d.Claims))
	for k, c := range d.Claims {
		c.Stale = c.stale(now)
		out[k] = c
	}
	return out
}

// Of returns the claims of one project, lowest task number first.
func (s *Store) Of(project string) []Claim {
	var out []Claim
	for _, c := range s.All() {
		if c.Project == project {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Task < out[j].Task })
	return out
}

func (s *Store) Get(project string, task int) (Claim, bool) {
	c, ok := s.All()[Key(project, task)]
	return c, ok
}

// update drops claims nobody came back to, then applies fn under the lock.
func (s *Store) update(fn func(map[string]Claim) error) error {
	return s.file.Update(func(d *doc) error {
		now := time.Now()
		for k, c := range d.Claims {
			if now.Sub(c.Beat) > maxAge {
				delete(d.Claims, k)
			}
		}
		if err := fn(d.Claims); err != nil {
			return err
		}
		if len(d.Claims) > maxClaims {
			return fmt.Errorf("more than %d claims; something is wrong", maxClaims)
		}
		return nil
	})
}

// Take claims a task for an agent. A task someone else is actively on is refused
// unless steal is set; retaking your own claim just refreshes it.
func (s *Store) Take(c Claim, steal bool) (Claim, error) {
	if c.Project == "" || c.Task <= 0 {
		return Claim{}, errors.New("a claim needs a project and a task number")
	}
	if c.Agent == "" {
		c.Agent = "claude"
	}
	now := time.Now().Truncate(time.Second)
	c.ClaimedAt, c.Beat = now, now
	err := s.update(func(m map[string]Claim) error {
		if old, ok := m[c.Key()]; ok {
			if old.held(c, now) && !steal {
				who := old.Agent
				if old.Session != "" {
					who += " (session " + textfmt.Short(old.Session) + ")"
				}
				return fmt.Errorf("%s is already on %s since %s; pass --steal to take it over",
					who, c.Key(), textfmt.Ago(old.ClaimedAt, now))
			}
			c.ClaimedAt = old.ClaimedAt // same work carrying on
			if old.held(c, now) {
				c.ClaimedAt = now // a different agent: its own stretch of work
			}
		}
		m[c.Key()] = c
		return nil
	})
	return c, err
}

// Heartbeat refreshes a claim so the board keeps showing it as live work.
func (s *Store) Heartbeat(project string, task int, session string) (Claim, error) {
	var out Claim
	err := s.update(func(m map[string]Claim) error {
		c, ok := m[Key(project, task)]
		if !ok {
			return fmt.Errorf("nothing claimed %s", Key(project, task))
		}
		if session != "" && c.Session != "" && c.Session != session {
			return fmt.Errorf("%s is claimed by another session (%s)", c.Key(), textfmt.Short(c.Session))
		}
		c.Beat = time.Now().Truncate(time.Second)
		m[c.Key()] = c
		out = c
		return nil
	})
	return out, err
}

// Release drops a claim and reports whether there was one.
func (s *Store) Release(project string, task int) (Claim, bool, error) {
	var out Claim
	var had bool
	err := s.update(func(m map[string]Claim) error {
		k := Key(project, task)
		out, had = m[k]
		delete(m, k)
		return nil
	})
	return out, had, err
}

// ReleaseGone drops claims for projects that are no longer on the board, so a
// renamed or archived project does not keep a claim alive forever. An empty
// list is a failed scan, not an empty world, and changes nothing.
func (s *Store) ReleaseGone(rels []string) {
	if len(rels) == 0 {
		return
	}
	s.update(func(m map[string]Claim) error {
		for k, c := range m {
			if !slices.Contains(rels, c.Project) {
				delete(m, k)
			}
		}
		return nil
	})
}
