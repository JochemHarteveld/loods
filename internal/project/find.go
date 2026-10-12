package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// containing finds the project a directory belongs to: the deepest project
// path that contains it (worktrees under <repo>/.claude/worktrees count).
func containing(items []*Item, dir string) *Item {
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

// ContainingWorktree is containing plus a step out of a linked git worktree
// that lives outside the project folder, back to its main repository.
func ContainingWorktree(items []*Item, dir string) *Item {
	if it := containing(items, dir); it != nil {
		return it
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return nil
	}
	return containing(items, filepath.Dir(strings.TrimSpace(string(out))))
}

// ContainingOrRenamed is containing plus a fallback for projects that were
// moved since (~/Projects/barsys → ~/Projects/blauweschuit/barsys): if the old
// directory is gone, match a project by folder name, deepest first.
func ContainingOrRenamed(items []*Item, dir string) *Item {
	if it := containing(items, dir); it != nil {
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

// Rels is the list of project ids on the board, which is what the stores use
// to drop state for projects that are gone.
func Rels(items []*Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Rel)
	}
	return out
}
