package github

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"loods/internal/project"
)

func TestCheckStateAndRepo(t *testing.T) {
	for _, c := range []struct{ status, conclusion, state, want string }{
		{"COMPLETED", "SUCCESS", "", "pass"},
		{"COMPLETED", "FAILURE", "", "fail"},
		{"IN_PROGRESS", "", "", "pending"},
		{"", "", "PENDING", "pending"},
		{"", "", "SUCCESS", "pass"},
		{"", "", "ERROR", "fail"},
	} {
		if got := CheckState(c.status, c.conclusion, c.state); got != c.want {
			t.Errorf("CheckState(%q,%q,%q) = %s, want %s", c.status, c.conclusion, c.state, got, c.want)
		}
	}
	if r := Repo("https://github.com/TranspaClean/transpakit"); r != "TranspaClean/transpakit" {
		t.Error(r)
	}
	if r := Repo("https://gitlab.com/a/b"); r != "" {
		t.Error(r)
	}
}

func TestRealGitHub(t *testing.T) {
	if os.Getenv("LOODS_REAL") == "" {
		t.Skip("set LOODS_REAL=1")
	}
	home, _ := os.UserHomeDir()
	items := project.Board(filepath.Join(home, "Projects"), 3, filepath.Join(home, "Archive"))
	project.ScanAll(items, false)
	g := NewPoller()
	start := time.Now()
	g.Refresh(items, Every)
	info, status := g.Snapshot()
	t.Logf("%v, status %q", time.Since(start), status)
	for rel, i := range info {
		ci := "-"
		if i.CI != nil {
			ci = i.CI.Workflow + ":" + i.CI.Status + "/" + i.CI.Result
		}
		t.Logf("%-40s %-32s prs=%d issues=%d ci=%s err=%s", rel, i.Repo, len(i.PRs), i.Issues, ci, i.Error)
		for _, p := range i.PRs {
			t.Logf("    #%d %s [%s] checks=%s review=%s draft=%v", p.Number, p.Branch, p.Title, p.Checks, p.Review, p.Draft)
		}
	}
}
