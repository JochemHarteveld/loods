// Package github is the state of a repository on GitHub, through the gh CLI:
// open PRs with their checks, open issues and the latest CI run on the
// checked-out branch. Read-only queries, every few minutes, only for repos
// whose remote is on github.com. Turn it off with `github: false` in
// config.yaml.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"loods/internal/project"
)

// Info is what loods knows about one project's GitHub repository.
type Info struct {
	Repo      string    `json:"repo"`   // owner/name
	Branch    string    `json:"branch"` // the branch CI was looked up for
	PRs       []PR      `json:"prs"`
	Issues    int       `json:"issues"`
	CI        *CIRun    `json:"ci,omitempty"` // latest run on the current branch
	Error     string    `json:"error,omitempty"`
	FetchedAt time.Time `json:"fetched_at"`
}

type PR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Branch string `json:"branch"`
	Draft  bool   `json:"draft,omitempty"`
	URL    string `json:"url"`
	Review string `json:"review,omitempty"` // APPROVED | CHANGES_REQUESTED | REVIEW_REQUIRED
	Checks string `json:"checks,omitempty"` // pass | fail | pending, empty without checks
}

// CIRun is one workflow run on GitHub Actions.
type CIRun struct {
	Workflow  string    `json:"workflow"`
	Branch    string    `json:"branch"`
	Status    string    `json:"status"`     // queued | in_progress | completed
	Result    string    `json:"conclusion"` // success | failure | cancelled | …
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
}

// Every is how often a repo is asked again, unless something asks sooner.
const Every = 5 * time.Minute

// Poller keeps the last answer per project and refreshes it in the background,
// never more than three repos at a time.
type Poller struct {
	mu     sync.Mutex
	info   map[string]Info // by project rel
	status string          // why GitHub is off, empty when on
	busy   bool
}

func NewPoller() *Poller {
	return &Poller{info: map[string]Info{}}
}

// Snapshot is the last answer for every project, plus why it is empty.
func (g *Poller) Snapshot() (map[string]Info, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]Info, len(g.info))
	for k, v := range g.info {
		out[k] = v
	}
	return out, g.status
}

// Repo returns owner/name for a github.com web URL, or "" when the remote is
// somewhere else.
func Repo(web string) string {
	u, err := url.Parse(web)
	if err != nil || u.Host != "github.com" {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

func gh(ctx context.Context, out any, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "NO_COLOR=1", "GH_NO_UPDATE_NOTIFIER=1")
	b, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			msg := strings.TrimSpace(string(ee.Stderr))
			if i := strings.IndexByte(msg, '\n'); i > 0 {
				msg = msg[:i]
			}
			return errors.New(msg)
		}
		return err
	}
	return json.Unmarshal(b, out)
}

// Refresh queries every GitHub repo among items and reports whether anything
// changed. It skips repos whose answer is younger than maxAge, and does
// nothing at all while another refresh is running.
func (g *Poller) Refresh(items []*project.Item, maxAge time.Duration) bool {
	g.mu.Lock()
	if g.busy {
		g.mu.Unlock()
		return false
	}
	g.busy = true
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.busy = false
		g.mu.Unlock()
	}()

	status := ""
	if _, err := exec.LookPath("gh"); err != nil {
		status = "gh is not installed"
	} else if err := exec.Command("gh", "auth", "status", "--hostname", "github.com").Run(); err != nil {
		status = "gh is not logged in (gh auth login)"
	}
	g.mu.Lock()
	changed := status != g.status
	g.status = status
	g.mu.Unlock()
	if status != "" {
		return changed
	}

	type job struct {
		rel, repo, branch string
	}
	var jobs []job
	now := time.Now()
	g.mu.Lock()
	for _, it := range items {
		repo := Repo(it.WebURL)
		if repo == "" {
			continue
		}
		if old, ok := g.info[it.Rel]; ok && old.Repo == repo && old.Branch == it.Branch && now.Sub(old.FetchedAt) < maxAge {
			continue
		}
		jobs = append(jobs, job{it.Rel, repo, it.Branch})
	}
	g.mu.Unlock()

	var wg sync.WaitGroup
	sem := make(chan struct{}, 3)
	results := make([]Info, len(jobs))
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = fetch(context.Background(), j.repo, j.branch)
		}()
	}
	wg.Wait()

	g.mu.Lock()
	defer g.mu.Unlock()
	for i, j := range jobs {
		old, had := g.info[j.rel]
		g.info[j.rel] = results[i]
		changed = changed || !had || !same(old, results[i])
	}
	// Projects that left the board or lost their GitHub remote.
	keep := map[string]bool{}
	for _, it := range items {
		if Repo(it.WebURL) != "" {
			keep[it.Rel] = true
		}
	}
	for rel := range g.info {
		if !keep[rel] {
			delete(g.info, rel)
			changed = true
		}
	}
	return changed
}

func same(a, b Info) bool {
	a.FetchedAt, b.FetchedAt = time.Time{}, time.Time{}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// CheckState folds one check run or status context into pass/pending/fail.
func CheckState(status, conclusion, state string) string {
	switch strings.ToUpper(conclusion + state) {
	case "FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE":
		return "fail"
	case "SUCCESS", "NEUTRAL", "SKIPPED":
		return "pass"
	}
	if status == "" || strings.EqualFold(status, "COMPLETED") {
		if strings.EqualFold(state, "PENDING") || strings.EqualFold(state, "EXPECTED") {
			return "pending"
		}
		return "pass"
	}
	return "pending"
}

func fetch(ctx context.Context, repo, branch string) Info {
	info := Info{Repo: repo, Branch: branch, PRs: []PR{}, FetchedAt: time.Now()}
	var prs []struct {
		Number      int    `json:"number"`
		Title       string `json:"title"`
		HeadRefName string `json:"headRefName"`
		IsDraft     bool   `json:"isDraft"`
		URL         string `json:"url"`
		Review      string `json:"reviewDecision"`
		Checks      []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			State      string `json:"state"` // status contexts (not check runs) use state
		} `json:"statusCheckRollup"`
	}
	if err := gh(ctx, &prs, "pr", "list", "-R", repo, "--state", "open", "--limit", "30",
		"--json", "number,title,headRefName,isDraft,url,reviewDecision,statusCheckRollup"); err != nil {
		info.Error = err.Error()
		return info
	}
	for _, p := range prs {
		pr := PR{Number: p.Number, Title: p.Title, Branch: p.HeadRefName, Draft: p.IsDraft, URL: p.URL, Review: p.Review}
		rank := map[string]int{"": 0, "pass": 1, "pending": 2, "fail": 3}
		for _, c := range p.Checks {
			if v := CheckState(c.Status, c.Conclusion, c.State); rank[v] > rank[pr.Checks] {
				pr.Checks = v
			}
		}
		info.PRs = append(info.PRs, pr)
	}

	var issues []struct{}
	if err := gh(ctx, &issues, "issue", "list", "-R", repo, "--state", "open", "--limit", "500", "--json", "number"); err == nil {
		info.Issues = len(issues)
	}

	if branch != "" && branch != "HEAD" {
		var runs []struct {
			Workflow   string    `json:"workflowName"`
			Branch     string    `json:"headBranch"`
			Status     string    `json:"status"`
			Conclusion string    `json:"conclusion"`
			URL        string    `json:"url"`
			CreatedAt  time.Time `json:"createdAt"`
		}
		if err := gh(ctx, &runs, "run", "list", "-R", repo, "--branch", branch, "--limit", "1",
			"--json", "workflowName,headBranch,status,conclusion,url,createdAt"); err == nil && len(runs) > 0 {
			r := runs[0]
			info.CI = &CIRun{Workflow: r.Workflow, Branch: r.Branch, Status: r.Status, Result: r.Conclusion, URL: r.URL, CreatedAt: r.CreatedAt}
		}
	}
	return info
}
