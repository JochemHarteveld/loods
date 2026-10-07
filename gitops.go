package main

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
)

// Branch and worktree cleanup. Every deletion is logged with the commit it
// pointed at, so `undo` can recreate branches and worktrees exactly. Nothing
// is forced: dirty worktrees are skipped, not removed.

type GitOp struct {
	Time     time.Time `json:"time"`
	Batch    string    `json:"batch"`
	Repo     string    `json:"repo"`             // absolute path of the main checkout
	Action   string    `json:"action"`           // delete-branch | remove-worktree | restore
	Branch   string    `json:"branch,omitempty"` //
	SHA      string    `json:"sha,omitempty"`
	Worktree string    `json:"worktree,omitempty"`
	Undoes   string    `json:"undoes,omitempty"` // batch a restore entry belongs to
}

type OpResult struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Note  string `json:"note,omitempty"`
	Error string `json:"error,omitempty"`
}

type gitLog struct {
	path string
	mu   sync.Mutex
}

func gitLogPath(stateDir string) string { return filepath.Join(stateDir, "git-cleanup.log") }

func (l *gitLog) append(ops ...GitOp) error {
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
	for _, op := range ops {
		b, _ := json.Marshal(op)
		if _, err := f.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}

func (l *gitLog) read() []GitOp {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(l.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []GitOp
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var op GitOp
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

func (l *gitLog) lastBatch(repo string) (*UndoInfo, []GitOp) {
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
		var batch []GitOp
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

// gitw runs a git command that changes the repo and returns its error output.
func gitw(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

// deleteBranches removes the named branches. A branch checked out in a linked
// worktree takes the worktree with it, but only if that worktree is clean.
func (l *gitLog) deleteBranches(repo string, names []string) []OpResult {
	current, _ := git(repo, "rev-parse", "--abbrev-ref", "HEAD")
	def := strings.TrimPrefix(defaultBranch(repo), "origin/")
	trees := worktreeBranches(repo)
	batch := time.Now().Format("20060102-150405.000")
	var results []OpResult
	for _, name := range names {
		r := OpResult{Name: name}
		results = append(results, r)
		res := &results[len(results)-1]
		if name == current || name == def {
			res.Error = "current or default branch: kept"
			continue
		}
		sha, err := git(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
		if err != nil || sha == "" {
			res.Error = "no such branch"
			continue
		}
		var ops []GitOp
		if wt := trees[name]; wt != "" {
			if _, err := os.Stat(wt); err == nil {
				if dirty, _ := git(wt, "status", "--porcelain"); dirty != "" {
					res.Error = "its worktree " + wt + " has uncommitted changes: kept"
					continue
				}
				if err := gitw(repo, "worktree", "remove", wt); err != nil {
					res.Error = err.Error()
					continue
				}
				ops = append(ops, GitOp{Time: time.Now(), Batch: batch, Repo: repo, Action: "remove-worktree", Branch: name, SHA: sha, Worktree: wt})
				res.Note = "worktree removed"
			} else if err := gitw(repo, "worktree", "prune"); err != nil {
				// The folder is gone; git still holds the branch for it until pruned.
				res.Error = err.Error()
				continue
			}
		}
		if err := gitw(repo, "branch", "-D", name); err != nil {
			res.Error = err.Error()
		} else {
			res.OK = true
			ops = append(ops, GitOp{Time: time.Now(), Batch: batch, Repo: repo, Action: "delete-branch", Branch: name, SHA: sha})
		}
		if err := l.append(ops...); err != nil && res.Error == "" {
			res.Error = "done, but the undo log failed: " + err.Error()
		}
	}
	return results
}

// undo restores the newest cleanup batch of a repo: branches first, then
// their worktrees.
func (l *gitLog) undo(repo string) ([]OpResult, error) {
	info, ops := l.lastBatch(repo)
	if info == nil {
		return nil, errors.New("nothing to undo")
	}
	var results []OpResult
	for _, op := range ops {
		if op.Action != "delete-branch" {
			continue
		}
		r := OpResult{Name: op.Branch}
		if _, err := git(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+op.Branch); err == nil {
			r.Error = "a branch with that name exists again"
		} else if err := gitw(repo, "branch", op.Branch, op.SHA); err != nil {
			r.Error = err.Error()
		} else {
			r.OK = true
		}
		results = append(results, r)
	}
	for _, op := range ops {
		if op.Action != "remove-worktree" {
			continue
		}
		r := OpResult{Name: op.Worktree}
		if _, err := os.Stat(op.Worktree); err == nil {
			r.Error = "folder exists again"
		} else if err := gitw(repo, "worktree", "add", op.Worktree, op.Branch); err != nil {
			r.Error = err.Error()
		} else {
			r.OK = true
			r.Note = "worktree restored"
		}
		results = append(results, r)
	}
	err := l.append(GitOp{Time: time.Now(), Batch: info.Batch, Repo: repo, Action: "restore", Undoes: info.Batch})
	return results, err
}

func pruneWorktrees(repo string) ([]OpResult, error) {
	cmd := exec.Command("git", "-C", repo, "worktree", "prune", "-v")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git worktree prune: %s", strings.TrimSpace(string(out)))
	}
	var results []OpResult
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			results = append(results, OpResult{Name: line, OK: true})
		}
	}
	return results, nil
}

// ignoreEnv appends the unignored secret files to the repo's .gitignore,
// anchored to their exact paths. It never touches files already committed.
func ignoreEnv(repo string) ([]OpResult, error) {
	var unignored []string
	cands := envCandidates(repo)
	if len(cands) == 0 {
		return nil, errors.New("no .env files found")
	}
	ignored := map[string]bool{}
	s, _ := git(repo, append([]string{"check-ignore", "--"}, cands...)...)
	for _, f := range strings.Split(s, "\n") {
		ignored[f] = true
	}
	tracked, _ := git(repo, append([]string{"ls-files", "--"}, cands...)...)
	for _, f := range cands {
		if !ignored[f] && !slices.Contains(strings.Split(tracked, "\n"), f) {
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
	var results []OpResult
	for _, f := range unignored {
		b.WriteString("/" + filepath.ToSlash(f) + "\n")
		results = append(results, OpResult{Name: f, OK: true})
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	_, err = f.WriteString(b.String())
	return results, err
}
