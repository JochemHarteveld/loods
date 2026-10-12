package gitx

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

type Branch struct {
	Name       string    `json:"name"`
	Current    bool      `json:"current,omitempty"`
	LastCommit time.Time `json:"last_commit"`
	Subject    string    `json:"subject"`
	Upstream   string    `json:"upstream,omitempty"`
	Ahead      int       `json:"ahead,omitempty"` // vs upstream
	Behind     int       `json:"behind,omitempty"`
	Gone       bool      `json:"upstream_gone,omitempty"`
	BaseAhead  int       `json:"base_ahead"` // vs the default branch
	BaseBehind int       `json:"base_behind"`
	Merged     bool      `json:"merged,omitempty"` // fully contained in the default branch
	Worktree   string    `json:"worktree,omitempty"`
}

type BranchList struct {
	Default  string   `json:"default"` // ref the base counts are measured against, e.g. origin/main
	Branches []Branch `json:"branches"`
}

// Branches reads all local branches of a repo, newest first.
// Remote state is as of the last fetch; this never touches the network.
func Branches(dir string) (BranchList, error) {
	const sep = "\x1f"
	out, err := Run(dir, "for-each-ref", "--sort=-committerdate", "refs/heads",
		"--format=%(HEAD)"+sep+"%(refname:short)"+sep+"%(committerdate:unix)"+sep+
			"%(upstream:short)"+sep+"%(upstream:track,nobracket)"+sep+"%(subject)")
	if err != nil {
		return BranchList{}, err
	}
	bl := BranchList{Default: DefaultBranch(dir), Branches: []Branch{}}
	trees := WorktreeBranches(dir)
	merged := map[string]bool{}
	if bl.Default != "" {
		if s, err := Run(dir, "branch", "--merged", bl.Default, "--format=%(refname:short)"); err == nil {
			for _, n := range strings.Split(s, "\n") {
				merged[n] = true
			}
		}
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, sep)
		if len(f) != 6 {
			continue
		}
		b := Branch{Current: f[0] == "*", Name: f[1], Upstream: f[3], Subject: f[5], Merged: merged[f[1]], Worktree: trees[f[1]]}
		if n, err := strconv.ParseInt(f[2], 10, 64); err == nil {
			b.LastCommit = time.Unix(n, 0)
		}
		b.Ahead, b.Behind, b.Gone = ParseTrack(f[4])
		if bl.Default != "" {
			if s, err := Run(dir, "rev-list", "--left-right", "--count", bl.Default+"..."+b.Name); err == nil {
				b.BaseBehind, b.BaseAhead = parseCounts(s)
			}
		}
		bl.Branches = append(bl.Branches, b)
	}
	sort.SliceStable(bl.Branches, func(i, j int) bool { return bl.Branches[i].Current && !bl.Branches[j].Current })
	return bl, nil
}

// ParseTrack reads %(upstream:track,nobracket): "ahead 1, behind 2", "gone" or "".
func ParseTrack(s string) (ahead, behind int, gone bool) {
	if s == "gone" {
		return 0, 0, true
	}
	for _, part := range strings.Split(s, ", ") {
		k, v, ok := strings.Cut(part, " ")
		if !ok {
			continue
		}
		n, _ := strconv.Atoi(v)
		switch k {
		case "ahead":
			ahead = n
		case "behind":
			behind = n
		}
	}
	return ahead, behind, false
}

// DefaultBranch prefers the remote's HEAD (origin/main), then a local main or master.
func DefaultBranch(dir string) string {
	if s, err := Run(dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil && s != "" {
		return s
	}
	for _, n := range []string{"main", "master"} {
		if _, err := Run(dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+n); err == nil {
			return n
		}
	}
	return ""
}

// WorktreeBranches maps branch name → path of every linked worktree.
func WorktreeBranches(dir string) map[string]string {
	out := map[string]string{}
	s, err := Run(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return out
	}
	var path string
	first := true
	for _, line := range strings.Split(s, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch ") && !first:
			out[strings.TrimPrefix(line, "branch refs/heads/")] = path
		case line == "":
			first = false
		}
	}
	return out
}

// CleanupCandidates lists branches that can go: merged into the default
// branch, or with a deleted upstream (often a squash-merged PR). Never the
// current or the default branch.
func CleanupCandidates(dir, current string) (merged, gone []string) {
	def := DefaultBranch(dir)
	if def == "" {
		return nil, nil
	}
	keep := map[string]bool{current: true, strings.TrimPrefix(def, "origin/"): true}
	isMerged := map[string]bool{}
	if s, err := Run(dir, "branch", "--merged", def, "--format=%(refname:short)"); err == nil {
		for _, n := range strings.Split(s, "\n") {
			if n != "" && !keep[n] {
				isMerged[n] = true
				merged = append(merged, n)
			}
		}
	}
	if s, err := Run(dir, "for-each-ref", "--format=%(refname:short)\x1f%(upstream:track)", "refs/heads"); err == nil {
		for _, line := range strings.Split(s, "\n") {
			name, track, _ := strings.Cut(line, "\x1f")
			if track == "[gone]" && !keep[name] && !isMerged[name] {
				gone = append(gone, name)
			}
		}
	}
	return merged, gone
}

// PrunableWorktrees counts worktrees whose folder is gone.
func PrunableWorktrees(dir string) int {
	s, err := Run(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return 0
	}
	return strings.Count("\n"+s, "\nprunable")
}
