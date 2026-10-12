// Package gitops is the cleanup loods is allowed to do to a repository:
// delete merged branches, remove the worktrees that hang off them, prune the
// ones whose folder is gone, and add forgotten .env files to .gitignore.
//
// Every deletion is logged with the commit it pointed at, so Undo can recreate
// branches and worktrees exactly. Nothing is forced: a dirty worktree is
// skipped, not removed.
package gitops

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"loods/internal/gitx"
	"loods/internal/hygiene"
	"loods/internal/ops"
)

// Op is one logged change to a repository, enough to undo it.
type Op struct {
	Time     time.Time `json:"time"`
	Batch    string    `json:"batch"`
	Repo     string    `json:"repo"`             // absolute path of the main checkout
	Action   string    `json:"action"`           // delete-branch | remove-worktree | restore
	Branch   string    `json:"branch,omitempty"` //
	SHA      string    `json:"sha,omitempty"`
	Worktree string    `json:"worktree,omitempty"`
	Undoes   string    `json:"undoes,omitempty"` // batch a restore entry belongs to
}

// Log is the append-only record of what was deleted, so it can be put back.
type Log struct {
	path string
	mu   sync.Mutex
}

func NewLog(stateDir string) *Log {
	return &Log{path: filepath.Join(stateDir, "git-cleanup.log")}
}

func (l *Log) append(entries ...Op) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, op := range entries {
		b, _ := json.Marshal(op)
		if _, err := f.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}

func (l *Log) read() []Op {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(l.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Op
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var op Op
		if json.Unmarshal(sc.Bytes(), &op) == nil {
			out = append(out, op)
		}
	}
	return out
}

// UndoInfo describes the newest cleanup of a repo that can still be undone.
type UndoInfo struct {
	Batch     string    `json:"batch"`
	Time      time.Time `json:"time"`
	Branches  []string  `json:"branches"`
	Worktrees int       `json:"worktrees"`
}

// LastBatch is the newest cleanup of a repo that can still be undone, and the
// entries that make it up.
func (l *Log) LastBatch(repo string) (*UndoInfo, []Op) {
	ops := l.read()
	undone := map[string]bool{}
	for _, op := range ops {
		if op.Action == "restore" {
			undone[op.Undoes] = true
		}
	}
	for i := len(ops) - 1; i >= 0; i-- {
		op := ops[i]
		if op.Repo != repo || op.Action == "restore" || undone[op.Batch] {
			continue
		}
		var batch []Op
		info := &UndoInfo{Batch: op.Batch, Time: op.Time}
		for _, o := range ops {
			if o.Batch == op.Batch && o.Action != "restore" {
				batch = append(batch, o)
				switch o.Action {
				case "delete-branch":
					info.Branches = append(info.Branches, o.Branch)
				case "remove-worktree":
					info.Worktrees++
				}
			}
		}
		return info, batch
	}
	return nil, nil
}

// DeleteBranches removes the named branches. A branch checked out in a linked
// worktree takes the worktree with it, but only if that worktree is clean.
func (l *Log) DeleteBranches(repo string, names []string) []ops.Result {
	current, _ := gitx.Run(repo, "rev-parse", "--abbrev-ref", "HEAD")
	def := strings.TrimPrefix(gitx.DefaultBranch(repo), "origin/")
	trees := gitx.WorktreeBranches(repo)
	batch := time.Now().Format("20060102-150405.000")
	var results []ops.Result
	for _, name := range names {
		r := ops.Result{Name: name}
		results = append(results, r)
		res := &results[len(results)-1]
		if name == current || name == def {
			res.Error = "current or default branch: kept"
			continue
		}
		sha, err := gitx.Run(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
		if err != nil || sha == "" {
			res.Error = "no such branch"
			continue
		}
		var entries []Op
		if wt := trees[name]; wt != "" {
			if _, err := os.Stat(wt); err == nil {
				if dirty, _ := gitx.Run(wt, "status", "--porcelain"); dirty != "" {
					res.Error = "its worktree " + wt + " has uncommitted changes: kept"
					continue
				}
				if err := gitx.Write(repo, "worktree", "remove", wt); err != nil {
					res.Error = err.Error()
					continue
				}
				entries = append(entries, Op{Time: time.Now(), Batch: batch, Repo: repo, Action: "remove-worktree", Branch: name, SHA: sha, Worktree: wt})
				res.Note = "worktree removed"
			} else if err := gitx.Write(repo, "worktree", "prune"); err != nil {
				// The folder is gone; git still holds the branch for it until pruned.
				res.Error = err.Error()
				continue
			}
		}
		if err := gitx.Write(repo, "branch", "-D", name); err != nil {
			res.Error = err.Error()
		} else {
			res.OK = true
			entries = append(entries, Op{Time: time.Now(), Batch: batch, Repo: repo, Action: "delete-branch", Branch: name, SHA: sha})
		}
		if err := l.append(entries...); err != nil && res.Error == "" {
			res.Error = "done, but the undo log failed: " + err.Error()
		}
	}
	return results
}

// Undo restores the newest cleanup batch of a repo: branches first, then
// their worktrees.
func (l *Log) Undo(repo string) ([]ops.Result, error) {
	info, entries := l.LastBatch(repo)
	if info == nil {
		return nil, errors.New("nothing to undo")
	}
	var results []ops.Result
	for _, op := range entries {
		if op.Action != "delete-branch" {
			continue
		}
		r := ops.Result{Name: op.Branch}
		if _, err := gitx.Run(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+op.Branch); err == nil {
			r.Error = "a branch with that name exists again"
		} else if err := gitx.Write(repo, "branch", op.Branch, op.SHA); err != nil {
			r.Error = err.Error()
		} else {
			r.OK = true
		}
		results = append(results, r)
	}
	for _, op := range entries {
		if op.Action != "remove-worktree" {
			continue
		}
		r := ops.Result{Name: op.Worktree}
		if _, err := os.Stat(op.Worktree); err == nil {
			r.Error = "folder exists again"
		} else if err := gitx.Write(repo, "worktree", "add", op.Worktree, op.Branch); err != nil {
			r.Error = err.Error()
		} else {
			r.OK = true
			r.Note = "worktree restored"
		}
		results = append(results, r)
	}
	err := l.append(Op{Time: time.Now(), Batch: info.Batch, Repo: repo, Action: "restore", Undoes: info.Batch})
	return results, err
}

// PruneWorktrees drops the worktrees whose folder is gone.
func PruneWorktrees(repo string) ([]ops.Result, error) {
	cmd := exec.Command("git", "-C", repo, "worktree", "prune", "-v")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git worktree prune: %s", strings.TrimSpace(string(out)))
	}
	var results []ops.Result
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			results = append(results, ops.Result{Name: line, OK: true})
		}
	}
	return results, nil
}

// IgnoreEnv appends the unignored secret files to the repo's .gitignore,
// anchored to their exact paths. It never touches files already committed:
// those are in the history, which .gitignore cannot fix.
func IgnoreEnv(repo string) ([]ops.Result, error) {
	cands := hygiene.EnvCandidates(repo)
	if len(cands) == 0 {
		return nil, errors.New("no .env files found")
	}
	tracked, _ := gitx.Run(repo, append([]string{"ls-files", "--"}, cands...)...)
	var unignored []string
	for _, f := range hygiene.Unignored(repo, cands) {
		if !slices.Contains(strings.Split(tracked, "\n"), f) {
			unignored = append(unignored, f)
		}
	}
	if len(unignored) == 0 {
		return nil, errors.New("every .env file is already ignored")
	}
	path := filepath.Join(repo, ".gitignore")
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var b strings.Builder
	if len(old) > 0 && !strings.HasSuffix(string(old), "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\n# secrets (added by loods)\n")
	var results []ops.Result
	for _, f := range unignored {
		b.WriteString("/" + filepath.ToSlash(f) + "\n")
		results = append(results, ops.Result{Name: f, OK: true})
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	_, err = f.WriteString(b.String())
	return results, err
}
