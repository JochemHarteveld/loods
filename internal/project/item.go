// Package project is the board's model of a folder under the root: what kind of
// thing it is, how fresh it is, what git says about it, and what you can run in
// it. Discovery and scanning are split on purpose — discovery is cheap and
// names everything, scanning is slow and fills it in.
package project

import (
	"fmt"
	"time"

	"loods/internal/hygiene"
)

type Kind string

const (
	KindGit    Kind = "git"  // has .git
	KindProj   Kind = "proj" // has a project manifest but no git
	KindFolder Kind = "dir"  // plain folder
	KindArc    Kind = "zip"  // archive file
)

type Item struct {
	Path       string    `json:"path"`
	Rel        string    `json:"rel"`
	Name       string    `json:"name"`
	Group      string    `json:"group"` // first folder of Rel for nested projects, "" for top level
	Kind       Kind      `json:"kind"`
	Stack      []string  `json:"stack,omitempty"`
	SizeBytes  int64     `json:"size_bytes"`
	Activity   time.Time `json:"last_activity"`
	LastFile   time.Time `json:"last_file_change"`
	LastCommit time.Time `json:"last_commit,omitzero"`
	Branch     string    `json:"branch,omitempty"`
	Dirty      int       `json:"dirty_files,omitempty"`
	Unpushed   int       `json:"unpushed_commits,omitempty"`
	HasRemote  bool      `json:"has_remote,omitempty"`
	Stashes    int       `json:"stashes,omitempty"`
	Upstream   string    `json:"upstream,omitempty"` // of the current branch
	Ahead      int       `json:"ahead,omitempty"`    // current branch vs its upstream (as of last fetch)
	Behind     int       `json:"behind,omitempty"`
	Branches   int       `json:"branches,omitempty"`
	Worktrees  int       `json:"worktrees,omitempty"` // linked worktrees, not counting the main checkout
	WebURL     string    `json:"web_url,omitempty"`
	Commands   []Command `json:"commands,omitempty"`
	// DefaultCommand is what r starts without asking (config default, else the first).
	DefaultCommand string            `json:"default_command,omitempty"`
	Risks          []string          `json:"risks,omitempty"`
	Warnings       []hygiene.Warning `json:"warnings,omitempty"`
	Dupes          []string          `json:"possible_duplicates,omitempty"`

	Scanned bool `json:"-"`
	Gone    bool `json:"-"`
}

// IsProject reports whether this is something you work in, rather than a plain
// folder or an archive file. Only these go on the board.
func (it *Item) IsProject() bool { return it.Kind == KindGit || it.Kind == KindProj }

// Counts is what the hygiene checks quote back at the user.
func (it *Item) Counts() hygiene.Counts {
	return hygiene.Counts{
		Dirty: it.Dirty, Unpushed: it.Unpushed, Behind: it.Behind,
		HasRemote: it.HasRemote, Upstream: it.Upstream,
	}
}

// finalize derives Activity and Risks from the raw scan fields.
func (it *Item) finalize() {
	it.Activity = it.LastFile
	if it.Kind == KindGit && !it.LastCommit.IsZero() {
		it.Activity = it.LastCommit
		if it.Dirty > 0 && it.LastFile.After(it.Activity) {
			it.Activity = it.LastFile
		}
	}
	it.Risks = nil
	switch it.Kind {
	case KindGit:
		if it.Dirty > 0 {
			it.Risks = append(it.Risks, fmt.Sprintf("%d uncommitted", it.Dirty))
		}
		if !it.HasRemote {
			it.Risks = append(it.Risks, "no remote")
		} else if it.Unpushed > 0 {
			it.Risks = append(it.Risks, fmt.Sprintf("%d unpushed", it.Unpushed))
		}
		if it.Stashes > 0 {
			it.Risks = append(it.Risks, fmt.Sprintf("%d stashed", it.Stashes))
		}
	case KindProj, KindFolder:
		it.Risks = append(it.Risks, "not in git")
	}
}
