package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"syscall"
	"time"
)

// A claim says an agent is working on one task right now. Claims are kept apart
// from plans.yaml on purpose: a plan is durable, a claim is not (a session ends,
// a laptop closes, Claude is interrupted). They live next to procs.json in the
// state dir, and carry a heartbeat so an abandoned claim can be told from a live
// one instead of making the board lie about what is being worked on.

const (
	claimStale  = 10 * time.Minute // no heartbeat for this long: probably abandoned
	claimMaxAge = 48 * time.Hour   // dropped from the file altogether
	maxClaims   = 500
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

func claimKey(project string, task int) string { return fmt.Sprintf("%s#%d", project, task) }

func (c Claim) key() string { return claimKey(c.Project, c.Task) }

func (c Claim) stale(now time.Time) bool { return now.Sub(c.Beat) > claimStale }

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

type ClaimStore struct {
	path string

	mu     sync.Mutex
	stamp  string
	claims map[string]Claim
}

func newClaimStore(path string) *ClaimStore {
	return &ClaimStore{path: path, claims: map[string]Claim{}}
}

func claimsPath() string { return filepath.Join(stateDir(), "claims.json") }

type claimFile struct {
	Claims map[string]Claim `json:"claims"`
}

func readClaims(path string) (claimFile, error) {
	cf := claimFile{Claims: map[string]Claim{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cf, nil
	}
	if err != nil {
		return cf, err
	}
	if err := json.Unmarshal(b, &cf); err != nil {
		return cf, err
	}
	if cf.Claims == nil {
		cf.Claims = map[string]Claim{}
	}
	return cf, nil
}

func writeClaims(path string, cf claimFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Refresh re-reads the file when it changed on disk and reports whether it did,
// so the server picks up claims made by the CLI.
func (cs *ClaimStore) Refresh() bool {
	stamp := fileStamp(cs.path)
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if stamp == cs.stamp && cs.stamp != "" {
		return false
	}
	cf, err := readClaims(cs.path)
	cs.stamp = stamp
	if err != nil {
		return false // keep what we had; a broken file is not worth clearing the board
	}
	cs.claims = cf.Claims
	return true
}

// All returns every claim, keyed "<project>#<task>", with Stale filled in.
func (cs *ClaimStore) All() map[string]Claim {
	now := time.Now()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	out := make(map[string]Claim, len(cs.claims))
	for k, c := range cs.claims {
		c.Stale = c.stale(now)
		out[k] = c
	}
	return out
}

// Of returns the claims of one project, lowest task number first.
func (cs *ClaimStore) Of(project string) []Claim {
	var out []Claim
	for _, c := range cs.All() {
		if c.Project == project {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Task < out[j].Task })
	return out
}

func (cs *ClaimStore) Get(project string, task int) (Claim, bool) {
	all := cs.All()
	c, ok := all[claimKey(project, task)]
	return c, ok
}

// update takes the lock, re-reads the file, applies fn and writes it back, so
// the server, the CLI and several agents never lose each other's changes.
func (cs *ClaimStore) update(fn func(map[string]Claim) error) error {
	if err := os.MkdirAll(filepath.Dir(cs.path), 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(cs.path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	cf, err := readClaims(cs.path)
	if err != nil {
		return fmt.Errorf("%s: %w", cs.path, err)
	}
	now := time.Now()
	for k, c := range cf.Claims {
		if now.Sub(c.Beat) > claimMaxAge {
			delete(cf.Claims, k)
		}
	}
	if err := fn(cf.Claims); err != nil {
		return err
	}
	if len(cf.Claims) > maxClaims {
		return fmt.Errorf("more than %d claims; something is wrong", maxClaims)
	}
	if err := writeClaims(cs.path, cf); err != nil {
		return err
	}
	cs.Refresh()
	return nil
}

// Take claims a task for an agent. A task someone else is actively on is refused
// unless steal is set; retaking your own claim just refreshes it.
func (cs *ClaimStore) Take(c Claim, steal bool) (Claim, error) {
	if c.Project == "" || c.Task <= 0 {
		return Claim{}, errors.New("a claim needs a project and a task number")
	}
	if c.Agent == "" {
		c.Agent = "claude"
	}
	now := time.Now().Truncate(time.Second)
	c.ClaimedAt, c.Beat = now, now
	err := cs.update(func(m map[string]Claim) error {
		if old, ok := m[c.key()]; ok {
			if old.held(c, now) && !steal {
				who := old.Agent
				if old.Session != "" {
					who += " (session " + short(old.Session) + ")"
				}
				return fmt.Errorf("%s is already on %s since %s; pass --steal to take it over",
					who, c.key(), ago(old.ClaimedAt, now))
			}
			c.ClaimedAt = old.ClaimedAt // same work carrying on
			if old.held(c, now) {
				c.ClaimedAt = now // a different agent: its own stretch of work
			}
		}
		m[c.key()] = c
		return nil
	})
	return c, err
}

// Beat refreshes a claim so the board keeps showing it as live work.
func (cs *ClaimStore) Heartbeat(project string, task int, session string) (Claim, error) {
	var out Claim
	err := cs.update(func(m map[string]Claim) error {
		c, ok := m[claimKey(project, task)]
		if !ok {
			return fmt.Errorf("nothing claimed %s", claimKey(project, task))
		}
		if session != "" && c.Session != "" && c.Session != session {
			return fmt.Errorf("%s is claimed by another session (%s)", c.key(), short(c.Session))
		}
		c.Beat = time.Now().Truncate(time.Second)
		m[c.key()] = c
		out = c
		return nil
	})
	return out, err
}

// Release drops a claim and reports whether there was one.
func (cs *ClaimStore) Release(project string, task int) (Claim, bool, error) {
	var out Claim
	var had bool
	err := cs.update(func(m map[string]Claim) error {
		k := claimKey(project, task)
		out, had = m[k]
		delete(m, k)
		return nil
	})
	return out, had, err
}

// ReleaseGone drops claims for projects that are no longer on the board, so a
// renamed or archived project does not keep a claim alive forever.
func (cs *ClaimStore) ReleaseGone(rels []string) {
	if len(rels) == 0 {
		return
	}
	cs.update(func(m map[string]Claim) error {
		for k, c := range m {
			if !slices.Contains(rels, c.Project) {
				delete(m, k)
			}
		}
		return nil
	})
}

// short trims a session id to something readable in a sentence.
func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// ago is a compact "4m" / "2h" / "3d" for CLI output and claim messages.
func ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
