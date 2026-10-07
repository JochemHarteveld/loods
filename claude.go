package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Claude Code keeps one JSONL transcript per session under
// ~/.claude/projects/<cwd>/<session>.jsonl. claudeIndex reads them
// incrementally (they only grow) and keeps one summary per session, so the
// board knows when Claude last worked on a project and on what.

type ClaudeSession struct {
	ID      string    `json:"id"`
	Cwd     string    `json:"-"`
	Title   string    `json:"title"` // Claude's own title for the session, else the first prompt
	Branch  string    `json:"branch,omitempty"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Prompts int       `json:"prompts"`
	// ActiveMins sums the gaps between records shorter than idleGap: time
	// spent working, not the session's wall-clock span.
	ActiveMins int `json:"active_mins"`

	firstPrompt string
	lastAt      time.Time
	activeNanos time.Duration
}

// ClaudeSummary is what a board card shows.
type ClaudeSummary struct {
	LastAt     time.Time `json:"last_at"`
	LastTitle  string    `json:"last_title"`
	Live       bool      `json:"live,omitempty"` // a record in the last few minutes
	Sessions7d int       `json:"sessions_7d,omitempty"`
	Mins7d     int       `json:"mins_7d,omitempty"`
}

const (
	idleGap  = 10 * time.Minute
	liveSpan = 3 * time.Minute
)

type claudeIndex struct {
	dir string

	refresh sync.Mutex // one reader at a time
	mu      sync.Mutex
	files   map[string]*transcript
}

type transcript struct {
	size, offset int64
	s            ClaudeSession
}

func claudeProjectsDir() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, "projects")
}

func newClaudeIndex(dir string) *claudeIndex {
	return &claudeIndex{dir: dir, files: map[string]*transcript{}}
}

// Refresh reads whatever was appended since last time and reports whether
// anything changed. If another refresh is running (the first one reads every
// transcript) it returns at once; that one will report the changes.
func (ci *claudeIndex) Refresh() bool {
	if !ci.refresh.TryLock() {
		return false
	}
	defer ci.refresh.Unlock()
	paths, _ := filepath.Glob(filepath.Join(ci.dir, "*", "*.jsonl"))
	seen := make(map[string]bool, len(paths))
	changed := false
	for _, path := range paths {
		seen[path] = true
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		ci.mu.Lock()
		t := ci.files[path]
		ci.mu.Unlock()
		if t != nil && t.size == info.Size() {
			continue
		}
		var next transcript
		if t != nil && info.Size() > t.offset {
			next = *t
		} else {
			// New, or shrank (rewritten): read from the start.
			next = transcript{s: ClaudeSession{ID: strings.TrimSuffix(filepath.Base(path), ".jsonl")}}
		}
		if err := next.read(path); err != nil {
			continue
		}
		next.size = info.Size()
		ci.mu.Lock()
		ci.files[path] = &next
		ci.mu.Unlock()
		changed = true
	}
	ci.mu.Lock()
	for path := range ci.files {
		if !seen[path] {
			delete(ci.files, path)
			changed = true
		}
	}
	ci.mu.Unlock()
	return changed
}

func (t *transcript) read(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReaderSize(f, 1<<16)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			// A partial last line is still being written: read it next time.
			return nil
		}
		t.offset += int64(len(line))
		t.s.add(line)
	}
}

type record struct {
	Type        string    `json:"type"`
	Timestamp   time.Time `json:"timestamp"`
	Cwd         string    `json:"cwd"`
	GitBranch   string    `json:"gitBranch"`
	IsSidechain bool      `json:"isSidechain"`
	IsMeta      bool      `json:"isMeta"`
	AiTitle     string    `json:"aiTitle"`
	Message     *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func (s *ClaudeSession) add(line []byte) {
	// Skip the big tool-result lines' message bodies unless they are user lines.
	var rec record
	if !bytes.Contains(line, []byte(`"type":"user"`)) {
		var light struct {
			Type      string    `json:"type"`
			Timestamp time.Time `json:"timestamp"`
			Cwd       string    `json:"cwd"`
			GitBranch string    `json:"gitBranch"`
			AiTitle   string    `json:"aiTitle"`
		}
		if json.Unmarshal(line, &light) != nil {
			return
		}
		rec = record{Type: light.Type, Timestamp: light.Timestamp, Cwd: light.Cwd, GitBranch: light.GitBranch, AiTitle: light.AiTitle}
	} else if json.Unmarshal(line, &rec) != nil {
		return
	}

	if rec.AiTitle != "" {
		s.Title = rec.AiTitle
	}
	if rec.Cwd != "" && s.Cwd == "" {
		s.Cwd = rec.Cwd
	}
	if rec.GitBranch != "" && rec.GitBranch != "HEAD" {
		s.Branch = rec.GitBranch
	}
	if !rec.Timestamp.IsZero() {
		if s.Start.IsZero() || rec.Timestamp.Before(s.Start) {
			s.Start = rec.Timestamp
		}
		if rec.Timestamp.After(s.End) {
			s.End = rec.Timestamp
		}
		if !s.lastAt.IsZero() {
			if gap := rec.Timestamp.Sub(s.lastAt); gap > 0 && gap < idleGap {
				s.activeNanos += gap
				s.ActiveMins = int(s.activeNanos / time.Minute)
			}
		}
		if rec.Timestamp.After(s.lastAt) {
			s.lastAt = rec.Timestamp
		}
	}
	if rec.Type == "user" && !rec.IsSidechain && !rec.IsMeta && rec.Message != nil {
		if text, ok := promptText(rec.Message.Content); ok {
			s.Prompts++
			if s.firstPrompt == "" {
				s.firstPrompt = text
			}
		}
	}
}

// promptText returns what the user typed, or false for tool results and
// injected context. The VS Code extension puts tags like <ide_opened_file>
// before the prompt; slash commands arrive as <command-name> tags.
func promptText(raw json.RawMessage) (string, bool) {
	var texts []string
	var one string
	if json.Unmarshal(raw, &one) == nil {
		texts = []string{one}
	} else {
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &parts) != nil {
			return "", false
		}
		for _, p := range parts {
			if p.Type == "tool_result" {
				return "", false
			}
			if p.Type == "text" {
				texts = append(texts, p.Text)
			}
		}
	}
	for _, t := range texts {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if !strings.HasPrefix(t, "<") {
			return t, true
		}
		if name := between(t, "<command-name>", "</command-name>"); name != "" {
			if builtinCommands[strings.TrimPrefix(name, "/")] {
				return "", false
			}
			return strings.TrimSpace(name + " " + between(t, "<command-args>", "</command-args>")), true
		}
	}
	return "", false
}

// Slash commands that manage Claude Code itself rather than ask for work.
var builtinCommands = map[string]bool{
	"compact": true, "clear": true, "model": true, "resume": true, "exit": true, "cost": true,
	"context": true, "config": true, "mcp": true, "help": true, "status": true, "login": true,
	"logout": true, "memory": true, "permissions": true, "caveman": true, "fast": true, "usage": true,
}

func between(s, open, close string) string {
	_, rest, ok := strings.Cut(s, open)
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(rest, close)
	return strings.TrimSpace(v)
}

func (s ClaudeSession) title() string {
	if s.Title != "" {
		return s.Title
	}
	t := strings.Join(strings.Fields(s.firstPrompt), " ")
	if r := []rune(t); len(r) > 90 {
		t = string(r[:90]) + "…"
	}
	return t
}

// Sessions returns the sessions whose working directory is inside one of
// the given project paths, keyed by project rel, newest first.
func (ci *claudeIndex) Sessions(items []*Item) map[string][]ClaudeSession {
	ci.mu.Lock()
	all := make([]ClaudeSession, 0, len(ci.files))
	for _, t := range ci.files {
		if t.s.Prompts > 0 && t.s.Cwd != "" {
			s := t.s
			s.Title = s.title()
			all = append(all, s)
		}
	}
	ci.mu.Unlock()

	out := map[string][]ClaudeSession{}
	for _, s := range all {
		if it := sessionProject(items, s.Cwd); it != nil {
			out[it.Rel] = append(out[it.Rel], s)
		}
	}
	for _, list := range out {
		sort.Slice(list, func(i, j int) bool { return list[i].End.After(list[j].End) })
	}
	return out
}

func summarize(list []ClaudeSession, now time.Time) ClaudeSummary {
	var cs ClaudeSummary
	if len(list) == 0 {
		return cs
	}
	cs.LastAt, cs.LastTitle = list[0].End, list[0].Title
	cs.Live = now.Sub(list[0].End) < liveSpan
	for _, s := range list {
		if now.Sub(s.End) < 7*24*time.Hour {
			cs.Sessions7d++
			cs.Mins7d += s.ActiveMins
		}
	}
	return cs
}

// projectFor finds the project a directory belongs to: the deepest project
// path that contains it (worktrees under <repo>/.claude/worktrees count).
func projectFor(items []*Item, dir string) *Item {
	dir = filepath.Clean(dir)
	var best *Item
	for _, it := range items {
		if dir == it.Path || strings.HasPrefix(dir, it.Path+string(filepath.Separator)) {
			if best == nil || len(it.Path) > len(best.Path) {
				best = it
			}
		}
	}
	return best
}

// sessionProject is projectFor plus a fallback for projects that were moved
// since the session (~/Projects/barsys → ~/Projects/blauweschuit/barsys): if
// the old directory is gone, match a project by folder name, deepest first.
func sessionProject(items []*Item, dir string) *Item {
	if it := projectFor(items, dir); it != nil {
		return it
	}
	if _, err := os.Stat(dir); err == nil {
		return nil // still exists, just not inside a project (a workspace folder)
	}
	parts := strings.Split(filepath.Clean(dir), string(filepath.Separator))
	for i := len(parts) - 1; i > 0; i-- {
		var match *Item
		for _, it := range items {
			if it.Name == parts[i] {
				if match != nil {
					return nil // ambiguous
				}
				match = it
			}
		}
		if match != nil {
			return match
		}
	}
	return nil
}
