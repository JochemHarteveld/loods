// Package hygiene is the second look at a repository: not what it is, but what
// is wrong with it. Secrets in git, work that has sat uncommitted for a week,
// branches that can go — each as a Warning the board can show and, where there
// is something to do about it, name the action that fixes it.
//
// It works from a gitx.Status and a directory, never from a board item, so the
// checks stay testable on a bare repository.
package hygiene

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"loods/internal/fsx"
	"loods/internal/gitx"
	"loods/internal/textfmt"
)

const (
	staleDirty    = 7 * 24 * time.Hour
	staleUnpushed = 3 * 24 * time.Hour
)

// Warning is one finding on a project. Fix names an action the board can run
// for it (with a confirmation); empty means a hint only.
type Warning struct {
	Level string `json:"level"` // danger | warn | info
	Kind  string `json:"kind"`
	Text  string `json:"text"`
	Fix   string `json:"fix,omitempty"` // cleanup-branches | prune-worktrees | ignore-env
}

// Report is what one hygiene pass over a checkout found.
type Report struct {
	DirtySince    time.Time // oldest modification among uncommitted files
	UnpushedSince time.Time // oldest commit that is on no remote
	EnvTracked    []string  // secret files committed to git
	EnvUnignored  []string  // secret files on disk that git would pick up
	Merged        []string  // local branches fully in the default branch
	Gone          []string  // branches whose upstream was deleted, not merged
	Prunable      int       // worktrees whose folder is gone
}

// Counts are the numbers a warning quotes back at the user. They come from the
// board item, but the checks only need these, so that is all they take.
type Counts struct {
	Dirty     int
	Unpushed  int
	Behind    int
	HasRemote bool
	Upstream  string
}

// Read looks for everything worth warning about in one checkout.
func Read(dir string, st gitx.Status) Report {
	var r Report
	r.DirtySince = oldestDirty(dir, st.Porcelain)
	if st.HasRemote && st.Unpushed > 0 {
		if s, err := gitx.Run(dir, "log", "--branches", "--not", "--remotes", "--format=%ct"); err == nil {
			lines := strings.Split(s, "\n")
			if n, err := strconv.ParseInt(lines[len(lines)-1], 10, 64); err == nil {
				r.UnpushedSince = time.Unix(n, 0)
			}
		}
	}

	if s, err := gitx.Run(dir, "ls-files", "--", ":(glob)**/.env", ":(glob)**/.env.*"); err == nil && s != "" {
		for _, f := range strings.Split(s, "\n") {
			if isEnvFile(filepath.Base(f)) {
				r.EnvTracked = append(r.EnvTracked, f)
			}
		}
	}
	for _, f := range Unignored(dir, EnvCandidates(dir)) {
		if !slices.Contains(r.EnvTracked, f) {
			r.EnvUnignored = append(r.EnvUnignored, f)
		}
	}

	r.Merged, r.Gone = gitx.CleanupCandidates(dir, st.Branch)
	r.Prunable = gitx.PrunableWorktrees(dir)
	return r
}

// Warnings turns a report into what the board shows, worst first.
func (r Report) Warnings(c Counts, now time.Time) []Warning {
	var w []Warning
	add := func(level, kind, text, fix string) {
		w = append(w, Warning{Level: level, Kind: kind, Text: text, Fix: fix})
	}
	for _, f := range r.EnvTracked {
		add("danger", "env-tracked", f+" is committed: secrets are in the git history", "")
	}
	if len(r.EnvUnignored) > 0 {
		add("warn", "env-unignored", strings.Join(r.EnvUnignored, ", ")+" not in .gitignore", "ignore-env")
	}
	if c.Dirty > 0 && !r.DirtySince.IsZero() && now.Sub(r.DirtySince) > staleDirty {
		add("warn", "stale-dirty", fmt.Sprintf("%d uncommitted %s, oldest change %s ago",
			c.Dirty, textfmt.Plural(c.Dirty, "file", "files"), textfmt.Span(now.Sub(r.DirtySince))), "")
	}
	if !c.HasRemote {
		add("warn", "no-remote", "no remote: this repo exists only on this disk", "")
	} else if !r.UnpushedSince.IsZero() && now.Sub(r.UnpushedSince) > staleUnpushed {
		add("warn", "stale-unpushed", fmt.Sprintf("%d %s not pushed for %s",
			c.Unpushed, textfmt.Plural(c.Unpushed, "commit", "commits"), textfmt.Span(now.Sub(r.UnpushedSince))), "")
	}
	if c.Behind > 0 {
		add("info", "behind", fmt.Sprintf("%d %s behind %s (as of the last fetch)",
			c.Behind, textfmt.Plural(c.Behind, "commit", "commits"), c.Upstream), "")
	}
	if n := len(r.Merged); n > 0 {
		add("info", "merged-branches", fmt.Sprintf("%d merged %s", n, textfmt.Plural(n, "branch", "branches")), "cleanup-branches")
	}
	if n := len(r.Gone); n > 0 {
		add("info", "gone-branches", fmt.Sprintf("%d %s whose remote branch was deleted", n, textfmt.Plural(n, "branch", "branches")), "cleanup-branches")
	}
	if r.Prunable > 0 {
		add("info", "prunable-worktrees", fmt.Sprintf("%d %s whose folder is gone",
			r.Prunable, textfmt.Plural(r.Prunable, "worktree", "worktrees")), "prune-worktrees")
	}
	return w
}

// isEnvFile matches .env and .env.production, but not the committed templates.
func isEnvFile(name string) bool {
	if name != ".env" && !strings.HasPrefix(name, ".env.") {
		return false
	}
	switch strings.TrimPrefix(name, ".env.") {
	case "example", "sample", "template", "dist", "defaults", "schema":
		return false
	}
	return true
}

// EnvCandidates finds secret files in the repo root and two levels below
// (monorepos: app/.env, apps/web/.env), skipping dependency folders.
func EnvCandidates(dir string) []string {
	var out []string
	var walk func(rel string, depth int)
	walk = func(rel string, depth int) {
		entries, err := os.ReadDir(filepath.Join(dir, rel))
		if err != nil {
			return
		}
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() {
				if isEnvFile(name) {
					out = append(out, filepath.Join(rel, name))
				}
				continue
			}
			if depth < 2 && !strings.HasPrefix(name, ".") && !fsx.Noisy(name) {
				walk(filepath.Join(rel, name), depth+1)
			}
		}
	}
	walk("", 0)
	return out
}

// Unignored keeps the paths git would pick up: not matched by .gitignore.
func Unignored(dir string, paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	// check-ignore exits 1 when nothing is ignored; its output is still right.
	s, _ := gitx.Run(dir, append([]string{"check-ignore", "--"}, paths...)...)
	ignored := map[string]bool{}
	for _, f := range strings.Split(s, "\n") {
		ignored[f] = true
	}
	var out []string
	for _, f := range paths {
		if !ignored[f] {
			out = append(out, f)
		}
	}
	return out
}

// oldestDirty stats the paths from `git status --porcelain`.
func oldestDirty(dir, status string) time.Time {
	var oldest time.Time
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 4 {
			continue
		}
		p := line[3:]
		if _, to, ok := strings.Cut(p, " -> "); ok {
			p = to
		}
		info, err := os.Stat(filepath.Join(dir, strings.Trim(p, `"`)))
		if err != nil {
			continue // deleted
		}
		if oldest.IsZero() || info.ModTime().Before(oldest) {
			oldest = info.ModTime()
		}
	}
	return oldest
}
