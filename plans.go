package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	// LastTaskID is the highest task number handed out here. Numbers stay stable
	// and are not reused while the project has a plan, because you refer to a task
	// by its number ("do todo #4 of loods") and it must keep meaning that task.
	LastTaskID int `yaml:"last_task_id,omitempty" json:"last_task_id,omitempty"`
}

// Task is written as a plain string: its number, then a markdown-style box.
// "[x] " marks it done, "[~] " marks it in progress, no box (or "[ ] ") means to
// do. The three boxes are the three columns of a project's planboard.
//
//	- '#8 [~] Shoot new store screenshots'
type Task struct {
	ID    int    `json:"id,omitempty"` // stable per project, see Plan.LastTaskID
	Text  string `json:"text"`
	State string `json:"state,omitempty"` // "" (to do), "doing", "done"
}

// TaskStates are the planboard columns, left to right. "" is the first one.
var TaskStates = []string{"", "doing", "done"}

func (t Task) Done() bool { return t.State == "done" }

func (t Task) box() string {
	switch t.State {
	case "done":
		return "[x] "
	case "doing":
		return "[~] "
	default:
		return ""
	}
}

func (t Task) MarshalYAML() (any, error) {
	if t.ID > 0 {
		return fmt.Sprintf("#%d %s%s", t.ID, t.box(), t.Text), nil
	}
	return t.box() + t.Text, nil
}

func (t *Task) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	*t = parseTask(s)
	return nil
}

// parseTask reads one checklist line: an optional "#<n>" number, then an
// optional box. Unknown boxes are left in the text, so a hand-written line is
// never silently swallowed; a line without a number gets one on the next write.
func parseTask(s string) Task {
	s = strings.TrimSpace(s)
	var t Task
	if rest, ok := strings.CutPrefix(s, "#"); ok {
		digits := rest
		if i := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
			digits = rest[:i]
		}
		if n, err := strconv.Atoi(digits); err == nil && n > 0 {
			t.ID = n
			s = strings.TrimSpace(rest[len(digits):])
		}
	}
	for _, b := range [][2]string{{"[x]", "done"}, {"[X]", "done"}, {"[~]", "doing"}, {"[ ]", ""}, {"[]", ""}} {
		if rest, ok := strings.CutPrefix(s, b[0]); ok {
			t.Text, t.State = strings.TrimSpace(rest), b[1]
			return t
		}
	}
	t.Text = s
	return t
}

type PlanLog struct {
	At   time.Time `yaml:"at" json:"at"`
	By   string    `yaml:"by,omitempty" json:"by,omitempty"` // "you", "claude"
	Text string    `yaml:"text" json:"text"`
}

// empty reports whether nothing worth storing is left. LastTaskID alone does not
// count: a plan wiped down to its counter leaves the file, and numbering for that
// project starts over.
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
	for i := range p.Tasks {
		p.Tasks[i].Text = strings.TrimSpace(p.Tasks[i].Text)
		if !slices.Contains(TaskStates, p.Tasks[i].State) {
			return fmt.Errorf("task state must be empty, doing or done, not %q", p.Tasks[i].State)
		}
	}
	p.numberTasks()
	if len(p.Log) > maxLog {
		p.Log = p.Log[len(p.Log)-maxLog:]
	}
	return nil
}

// numberTasks hands a number to every task that has none, and takes a duplicate
// number (a hand edit, a copied line) away from the later of the two. Numbers
// only ever go up, so a number you used in a conversation keeps its meaning.
func (p *Plan) numberTasks() {
	seen := map[int]bool{}
	for i, t := range p.Tasks {
		if t.ID > p.LastTaskID {
			p.LastTaskID = t.ID
		}
		if t.ID > 0 && !seen[t.ID] {
			seen[t.ID] = true
			continue
		}
		p.LastTaskID++
		p.Tasks[i].ID = p.LastTaskID
		seen[p.LastTaskID] = true
	}
}

// taskByID finds a task by the number shown on the board.
func (p *Plan) taskByID(id int) (int, bool) {
	for i, t := range p.Tasks {
		if t.ID == id {
			return i, true
		}
	}
	return 0, false
}

// openTasks are the ones still worth picking up, to do first.
func (p *Plan) openTasks() []Task {
	var todo, doing []Task
	for _, t := range p.Tasks {
		switch t.State {
		case "":
			todo = append(todo, t)
		case "doing":
			doing = append(doing, t)
		}
	}
	return append(todo, doing...)
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
# tasks: a checklist; "[x] " is done, "[~] " is in progress, no box is to do.
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
