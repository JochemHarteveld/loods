package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// Plans live in ~/.config/loods/plans.yaml: status, priority, next step,
// notes, tasks and a short log per project. loods rewrites this file (the
// board, `loods plan`, Claude's /wrapup), so it is kept apart from the
// hand-written config.yaml. Hand edits are fine too; comments are not kept.

var Statuses = []string{"idea", "active", "paused", "shipped", "dead"}

const (
	maxNext  = 300
	maxNotes = 20_000
	maxTasks = 200
	maxLog   = 30
)

type Plan struct {
	Status   string    `yaml:"status,omitempty" json:"status,omitempty"`
	Priority int       `yaml:"priority,omitempty" json:"priority,omitempty"` // 1 high … 3 low, 0 none
	Next     string    `yaml:"next,omitempty" json:"next,omitempty"`
	Notes    string    `yaml:"notes,omitempty" json:"notes,omitempty"`
	Tasks    []Task    `yaml:"tasks,omitempty" json:"tasks,omitempty"`
	Log      []PlanLog `yaml:"log,omitempty" json:"log,omitempty"`
	Updated  time.Time `yaml:"updated,omitempty" json:"updated,omitzero"`
}

// Task is written as a plain string, "[x] " marks it done, so the file reads
// like a markdown checklist.
type Task struct {
	Text string `json:"text"`
	Done bool   `json:"done,omitempty"`
}

func (t Task) MarshalYAML() (any, error) {
	if t.Done {
		return "[x] " + t.Text, nil
	}
	return t.Text, nil
}

func (t *Task) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "[ ]"))
	if rest, ok := strings.CutPrefix(s, "[x]"); ok {
		*t = Task{Text: strings.TrimSpace(rest), Done: true}
	} else {
		*t = Task{Text: s}
	}
	return nil
}

type PlanLog struct {
	At   time.Time `yaml:"at" json:"at"`
	By   string    `yaml:"by,omitempty" json:"by,omitempty"` // "you", "claude"
	Text string    `yaml:"text" json:"text"`
}

func (p *Plan) empty() bool {
	return p.Status == "" && p.Priority == 0 && p.Next == "" && p.Notes == "" && len(p.Tasks) == 0 && len(p.Log) == 0
}

func (p *Plan) validate() error {
	if p.Status != "" && !slices.Contains(Statuses, p.Status) {
		return fmt.Errorf("status must be one of %s", strings.Join(Statuses, ", "))
	}
	if p.Priority < 0 || p.Priority > 3 {
		return errors.New("priority must be 0 (none) to 3")
	}
	p.Next = strings.TrimSpace(strings.ReplaceAll(p.Next, "\n", " "))
	if len(p.Next) > maxNext {
		return fmt.Errorf("next step is longer than %d characters", maxNext)
	}
	if len(p.Notes) > maxNotes {
		return fmt.Errorf("notes are longer than %d characters", maxNotes)
	}
	p.Tasks = slices.DeleteFunc(p.Tasks, func(t Task) bool { return strings.TrimSpace(t.Text) == "" })
	if len(p.Tasks) > maxTasks {
		return fmt.Errorf("more than %d tasks", maxTasks)
	}
	if len(p.Log) > maxLog {
		p.Log = p.Log[len(p.Log)-maxLog:]
	}
	return nil
}

func (p *Plan) addLog(by, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	p.Log = append(p.Log, PlanLog{At: time.Now().Truncate(time.Second), By: by, Text: text})
}

type planFile struct {
	Projects map[string]*Plan `yaml:"projects"`
}

const plansHeader = `# loods plans: status, priority, next step, notes, tasks and a log per project
# (keys are paths relative to ~/Projects). Written by the board, ` + "`loods plan`" + ` and
# Claude's /wrapup. Hand edits are picked up within seconds; comments are not kept.
# status: idea | active | paused | shipped | dead    priority: 1 (high) … 3 (low)
`

// PlanStore reads plans.yaml and writes it under an exclusive flock, so the
// server, the CLI and Claude never lose each other's edits.
type PlanStore struct {
	path string

	mu    sync.Mutex
	stamp string // mtime+size of the last read
	plans map[string]Plan
	err   error
}

func plansPath() string { return filepath.Join(filepath.Dir(configPath()), "plans.yaml") }

func newPlanStore(path string) *PlanStore {
	return &PlanStore{path: path, plans: map[string]Plan{}}
}

func fileStamp(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d/%d", info.ModTime().UnixNano(), info.Size())
}

// Refresh re-reads the file when it changed on disk and reports whether it did.
func (ps *PlanStore) Refresh() bool {
	stamp := fileStamp(ps.path)
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if stamp == ps.stamp && ps.stamp != "" {
		return false
	}
	pf, err := readPlans(ps.path)
	ps.stamp = stamp
	if err != nil {
		changed := ps.err == nil || ps.err.Error() != err.Error()
		ps.err = err // keep the last good plans on screen
		return changed
	}
	ps.err = nil
	ps.plans = map[string]Plan{}
	for rel, p := range pf.Projects {
		if p != nil {
			ps.plans[rel] = *p
		}
	}
	return true
}

// All returns a copy of every plan, plus the last read error.
func (ps *PlanStore) All() (map[string]Plan, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	out := make(map[string]Plan, len(ps.plans))
	for k, v := range ps.plans {
		out[k] = v
	}
	return out, ps.err
}

func (ps *PlanStore) Get(rel string) Plan {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.plans[rel]
}

// Update applies fn to one project's plan: lock, re-read, change, write.
func (ps *PlanStore) Update(rel string, fn func(*Plan) error) (Plan, error) {
	if err := os.MkdirAll(filepath.Dir(ps.path), 0o755); err != nil {
		return Plan{}, err
	}
	lock, err := os.OpenFile(ps.path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return Plan{}, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return Plan{}, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	pf, err := readPlans(ps.path)
	if err != nil {
		return Plan{}, fmt.Errorf("%s: %w (fix it by hand first)", ps.path, err)
	}
	p := Plan{}
	if cur := pf.Projects[rel]; cur != nil {
		p = *cur
	}
	if err := fn(&p); err != nil {
		return Plan{}, err
	}
	if err := p.validate(); err != nil {
		return Plan{}, err
	}
	if p.empty() {
		delete(pf.Projects, rel)
	} else {
		p.Updated = time.Now().Truncate(time.Second)
		pf.Projects[rel] = &p
	}
	if err := writePlans(ps.path, pf); err != nil {
		return Plan{}, err
	}
	ps.Refresh()
	return p, nil
}

func readPlans(path string) (planFile, error) {
	pf := planFile{Projects: map[string]*Plan{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return pf, nil
	}
	if err != nil {
		return pf, err
	}
	if err := yaml.Unmarshal(b, &pf); err != nil {
		return pf, err
	}
	if pf.Projects == nil {
		pf.Projects = map[string]*Plan{}
	}
	return pf, nil
}

func writePlans(path string, pf planFile) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(pf); err != nil {
		return err
	}
	b := buf.Bytes()
	tmp, err := os.CreateTemp(filepath.Dir(path), ".plans-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(plansHeader + "\n"); err == nil {
		_, err = tmp.Write(b)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// PlanPatch is a partial update from the board; nil fields are left alone.
type PlanPatch struct {
	Status   *string `json:"status"`
	Priority *int    `json:"priority"`
	Next     *string `json:"next"`
	Notes    *string `json:"notes"`
	Tasks    *[]Task `json:"tasks"`
	Note     string  `json:"note"` // appended to the log
}

func (pp PlanPatch) apply(p *Plan, by string) {
	if pp.Status != nil && *pp.Status != p.Status {
		p.logStatus(by, p.Status, *pp.Status)
		p.Status = *pp.Status
	}
	if pp.Priority != nil {
		p.Priority = *pp.Priority
	}
	if pp.Next != nil {
		p.Next = *pp.Next
	}
	if pp.Notes != nil {
		p.Notes = *pp.Notes
	}
	if pp.Tasks != nil {
		p.Tasks = *pp.Tasks
	}
	p.addLog(by, pp.Note)
}

// logStatus records a status change. Changes by the same author within ten
// minutes (dragging a card across columns) fold into one entry, and vanish
// when the status ends up where it started.
func (p *Plan) logStatus(by, from, to string) {
	if n := len(p.Log); n > 0 {
		last := p.Log[n-1]
		if orig, _, ok := strings.Cut(strings.TrimPrefix(last.Text, "status "), " → "); ok &&
			strings.HasPrefix(last.Text, "status ") && last.By == by && time.Since(last.At) < 10*time.Minute {
			p.Log = p.Log[:n-1]
			if orig == orDash(to) {
				return
			}
			from = strings.TrimSuffix(orig, "—")
		}
	}
	p.addLog(by, fmt.Sprintf("status %s → %s", orDash(from), orDash(to)))
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
