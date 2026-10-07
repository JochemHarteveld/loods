package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Warning is a hygiene finding on a project. Fix names an action the board
// can run for it (with a confirmation); empty means a hint only.
type Warning struct {
	Level string `json:"level"` // danger | warn | info
	Kind  string `json:"kind"`
	Text  string `json:"text"`
	Fix   string `json:"fix,omitempty"` // cleanup-branches | prune-worktrees | ignore-env
}

const (
	staleDirty    = 7 * 24 * time.Hour
	staleUnpushed = 3 * 24 * time.Hour
)

// hygiene is the part of the git scan that feeds warnings.
type hygiene struct {
	dirtySince    time.Time // oldest modification among uncommitted files
	unpushedSince time.Time // oldest commit that is on no remote
	envTracked    []string  // secret files committed to git
	envUnignored  []string  // secret files on disk that git would pick up
	merged        []string  // local branches fully in the default branch
	gone          []string  // branches whose upstream was deleted, not merged (often squash-merged PRs)
	prunable      int       // worktrees whose folder is gone
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

func readHygiene(dir, status string, g *gitInfo) hygiene {
	var h hygiene
	h.dirtySince = oldestDirty(dir, status)
	if g.hasRemote && g.unpushed > 0 {
		if s, err := git(dir, "log", "--branches", "--not", "--remotes", "--format=%ct"); err == nil {
			lines := strings.Split(s, "\n")
			if n, err := strconv.ParseInt(lines[len(lines)-1], 10, 64); err == nil {
				h.unpushedSince = time.Unix(n, 0)
			}
		}
	}

	if s, err := git(dir, "ls-files", "--", ":(glob)**/.env", ":(glob)**/.env.*"); err == nil && s != "" {
		for _, f := range strings.Split(s, "\n") {
			if isEnvFile(filepath.Base(f)) {
				h.envTracked = append(h.envTracked, f)
			}
		}
	}
	if cands := envCandidates(dir); len(cands) > 0 {
		ignored := map[string]bool{}
		// check-ignore exits 1 when nothing is ignored; its output is still right.
		s, _ := git(dir, append([]string{"check-ignore", "--"}, cands...)...)
		for _, f := range strings.Split(s, "\n") {
			ignored[f] = true
		}
		for _, f := range cands {
			if !ignored[f] && !slices.Contains(h.envTracked, f) {
				h.envUnignored = append(h.envUnignored, f)
			}
		}
	}

	h.merged, h.gone = cleanupCandidates(dir, g.branch)
	if s, err := git(dir, "worktree", "list", "--porcelain"); err == nil {
		h.prunable = strings.Count("\n"+s, "\nprunable")
	}
	return h
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

// envCandidates finds secret files in the repo root and two levels below
// (monorepos: app/.env, apps/web/.env), skipping dependency folders.
func envCandidates(dir string) []string {
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
			if depth < 2 && !strings.HasPrefix(name, ".") && !noisyDirs[name] {
				walk(filepath.Join(rel, name), depth+1)
			}
		}
	}
	walk("", 0)
	return out
}

// cleanupCandidates lists branches that can go: merged into the default
// branch, or with a deleted upstream. Never the current or default branch.
func cleanupCandidates(dir, current string) (merged, gone []string) {
	def := defaultBranch(dir)
	if def == "" {
		return nil, nil
	}
	keep := map[string]bool{current: true, strings.TrimPrefix(def, "origin/"): true}
	isMerged := map[string]bool{}
	if s, err := git(dir, "branch", "--merged", def, "--format=%(refname:short)"); err == nil {
		for _, n := range strings.Split(s, "\n") {
			if n != "" && !keep[n] {
				isMerged[n] = true
				merged = append(merged, n)
			}
		}
	}
	if s, err := git(dir, "for-each-ref", "--format=%(refname:short)\x1f%(upstream:track)", "refs/heads"); err == nil {
		for _, line := range strings.Split(s, "\n") {
			name, track, _ := strings.Cut(line, "\x1f")
			if track == "[gone]" && !keep[name] && !isMerged[name] {
				gone = append(gone, name)
			}
		}
	}
	return merged, gone
}

func (it *Item) warnings(h hygiene, now time.Time) []Warning {
	var w []Warning
	add := func(level, kind, text, fix string) {
		w = append(w, Warning{Level: level, Kind: kind, Text: text, Fix: fix})
	}
	for _, f := range h.envTracked {
		add("danger", "env-tracked", f+" is committed: secrets are in the git history", "")
	}
	if len(h.envUnignored) > 0 {
		add("warn", "env-unignored", strings.Join(h.envUnignored, ", ")+" not in .gitignore", "ignore-env")
	}
	if it.Dirty > 0 && !h.dirtySince.IsZero() && now.Sub(h.dirtySince) > staleDirty {
		add("warn", "stale-dirty", fmt.Sprintf("%d uncommitted %s, oldest change %s ago", it.Dirty, plural(it.Dirty, "file", "files"), span(now.Sub(h.dirtySince))), "")
	}
	if !it.HasRemote {
		add("warn", "no-remote", "no remote: this repo exists only on this disk", "")
	} else if !h.unpushedSince.IsZero() && now.Sub(h.unpushedSince) > staleUnpushed {
		add("warn", "stale-unpushed", fmt.Sprintf("%d %s not pushed for %s", it.Unpushed, plural(it.Unpushed, "commit", "commits"), span(now.Sub(h.unpushedSince))), "")
	}
	if it.Behind > 0 {
		add("info", "behind", fmt.Sprintf("%d %s behind %s (as of the last fetch)", it.Behind, plural(it.Behind, "commit", "commits"), it.Upstream), "")
	}
	if n := len(h.merged); n > 0 {
		add("info", "merged-branches", fmt.Sprintf("%d merged %s", n, plural(n, "branch", "branches")), "cleanup-branches")
	}
	if n := len(h.gone); n > 0 {
		add("info", "gone-branches", fmt.Sprintf("%d %s whose remote branch was deleted", n, plural(n, "branch", "branches")), "cleanup-branches")
	}
	if h.prunable > 0 {
		add("info", "prunable-worktrees", fmt.Sprintf("%d %s whose folder is gone", h.prunable, plural(h.prunable, "worktree", "worktrees")), "prune-worktrees")
	}
	return w
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// span formats a duration like the UI does: 5d, 3w, 2mo.
func span(d time.Duration) string {
	days := int(d.Hours() / 24)
	switch {
	case days < 14:
		return fmt.Sprintf("%dd", days)
	case days < 60:
		return fmt.Sprintf("%dw", days/7)
	default:
		return fmt.Sprintf("%dmo", days/30)
	}
}
