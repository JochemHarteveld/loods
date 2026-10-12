// Package gitx is every git command loods runs, and the parsing of what comes
// back. Reads never take the index lock and never prompt, so loods can scan a
// repo you are working in; writes are only the ones a cleanup action needs.
package gitx

import (
	"errors"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Run is a read-only git command.
func Run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	// Read-only: never take the index lock, never prompt for credentials.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// Write is a git command that changes the repo; the error carries git's own
// message, which is what the board shows.
func Write(dir string, args ...string) error {
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

// Status is the state of one checkout, as a board card shows it.
type Status struct {
	LastCommit time.Time
	Branch     string
	Dirty      int
	Unpushed   int
	HasRemote  bool
	Stashes    int
	Upstream   string
	Ahead      int
	Behind     int
	Branches   int
	Worktrees  int
	WebURL     string
	// Porcelain is the raw `status --porcelain` output, kept so a second pass
	// (hygiene) does not have to ask git again.
	Porcelain string
}

// Read collects the state of a checkout in one pass.
func Read(dir string) Status {
	var s Status
	if out, err := Run(dir, "log", "-1", "--format=%ct"); err == nil && out != "" {
		if n, err := strconv.ParseInt(out, 10, 64); err == nil {
			s.LastCommit = time.Unix(n, 0)
		}
	}
	s.Branch, _ = Run(dir, "rev-parse", "--abbrev-ref", "HEAD")
	status, err := Run(dir, "status", "--porcelain")
	if err == nil {
		s.Porcelain = status
		s.Dirty = countLines(status)
	}
	remotes, _ := Run(dir, "remote")
	s.HasRemote = remotes != ""
	// Commits reachable from any local branch but from no remote branch.
	if out, err := Run(dir, "rev-list", "--count", "--branches", "--not", "--remotes"); err == nil {
		s.Unpushed, _ = strconv.Atoi(out)
	}
	if out, err := Run(dir, "stash", "list"); err == nil {
		s.Stashes = countLines(out)
	}
	if out, err := Run(dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); err == nil {
		s.Upstream = out
		if out, err := Run(dir, "rev-list", "--left-right", "--count", "HEAD...@{u}"); err == nil {
			s.Ahead, s.Behind = parseCounts(out)
		}
	}
	if out, err := Run(dir, "for-each-ref", "--format=%(refname:short)", "refs/heads"); err == nil {
		s.Branches = countLines(out)
	}
	if out, err := Run(dir, "worktree", "list", "--porcelain"); err == nil {
		s.Worktrees = max(0, strings.Count("\n"+out, "\nworktree ")-1)
	}
	if s.HasRemote {
		remote, err := Run(dir, "remote", "get-url", "origin")
		if err != nil {
			remote, _ = Run(dir, "remote", "get-url", strings.Fields(remotes)[0])
		}
		s.WebURL = WebURL(remote)
	}
	return s
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// parseCounts reads `rev-list --left-right --count` output: "<left>\t<right>".
func parseCounts(s string) (left, right int) {
	f := strings.Fields(s)
	if len(f) == 2 {
		left, _ = strconv.Atoi(f[0])
		right, _ = strconv.Atoi(f[1])
	}
	return left, right
}

// WebURL turns a git remote into a browsable https URL:
// git@github.com:User/repo.git → https://github.com/User/repo.
func WebURL(remote string) string {
	u := strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	switch {
	case strings.HasPrefix(u, "https://"), strings.HasPrefix(u, "http://"):
		p, err := url.Parse(u)
		if err != nil {
			return ""
		}
		p.User = nil // never show tokens embedded in the remote
		return p.String()
	case strings.HasPrefix(u, "ssh://"):
		u = strings.TrimPrefix(u, "ssh://")
		if at := strings.Index(u, "@"); at >= 0 {
			u = u[at+1:]
		}
		host, path, _ := strings.Cut(u, "/")
		host, _, _ = strings.Cut(host, ":") // drop port
		return "https://" + host + "/" + path
	case strings.Contains(u, ":"):
		if at := strings.Index(u, "@"); at >= 0 {
			u = u[at+1:]
		}
		host, path, _ := strings.Cut(u, ":")
		return "https://" + host + "/" + path
	}
	return ""
}
