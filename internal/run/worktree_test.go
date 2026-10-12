package run

import (
	"strings"
	"testing"
)

// The point of isolation: jobs that all commit, which would queue up behind one
// `exclusive: repo` lock, run at the same time when each has a tree of its own.
func TestTickRunsIsolatedCommittingJobsTogether(t *testing.T) {
	rs := newStore(t)
	var jobs []Job
	for _, id := range []string{"docs", "e2e", "deploy"} {
		j := job(id)
		j.Exclusive, j.Isolation = "repo", IsolationWorktree
		jobs = append(jobs, j)
	}
	submitted(t, rs, 3, jobs...)

	var s started
	rs.Tick(s.start)
	if len(s.ids) != 3 {
		t.Fatalf("started %v, want all three: an isolated job waits for nobody's tree", s.ids)
	}
}

// Isolation is per job, so an isolated job and a shared one that both say
// `exclusive: repo` do not wait for each other either — only the shared ones do.
func TestIsolationDoesNotTakeTheProjectLock(t *testing.T) {
	rs := newStore(t)
	shared, alsoShared, isolated := job("scaffold"), job("course"), job("docs")
	shared.Exclusive, alsoShared.Exclusive, isolated.Exclusive = "repo", "repo", "repo"
	isolated.Isolation = IsolationWorktree
	submitted(t, rs, 4, shared, alsoShared, isolated)

	var s started
	rs.Tick(s.start)
	if len(s.ids) != 2 || s.ids[0] != "scaffold" || s.ids[1] != "docs" {
		t.Fatalf("started %v, want scaffold and docs (course waits for the project tree)", s.ids)
	}
}

// Two isolated jobs of one run get different trees, and so different locks.
func TestIsolatedJobsGetTheirOwnLockAndBranch(t *testing.T) {
	rs := newStore(t)
	a, b := job("docs"), job("e2e")
	a.Exclusive, b.Exclusive = "repo", "repo"
	a.Isolation, b.Isolation = IsolationWorktree, IsolationWorktree
	r := submitted(t, rs, 2, a, b)

	if ka, kb := r.exclusiveKey(r.Jobs[0]), r.exclusiveKey(r.Jobs[1]); ka == kb {
		t.Errorf("both isolated jobs hold %q; they have separate trees", ka)
	}
	if got := WorktreeBranch(r.ID, "docs"); got != "loods/"+r.ID+"/docs" {
		t.Errorf("branch is %q", got)
	}
	// Nothing holds a lock against an isolated job, so status says "ready"
	// rather than naming a holder.
	rs.Tick((&started{}).start)
	r, _ = rs.Get(r.ID)
	if holder := r.LockHolder(r.Jobs[1]); holder != "" {
		t.Errorf("isolated job waits for %s", holder)
	}
}

// A shared job still waits, and still says who has the tree. This is the
// regression that matters: LockHolder compares scoped keys now, not raw names.
func TestLockHolderStillNamesTheSharedHolder(t *testing.T) {
	rs := newStore(t)
	first, second, isolated := job("scaffold"), job("course"), job("docs")
	first.Exclusive, second.Exclusive, isolated.Exclusive = "repo", "repo", "repo"
	isolated.Isolation = IsolationWorktree
	r := submitted(t, rs, 3, first, second, isolated)

	rs.Tick((&started{}).start)
	r, _ = rs.Get(r.ID)
	i, _ := r.JobIndex("course")
	if holder := r.LockHolder(r.Jobs[i]); holder != "scaffold" {
		t.Errorf("course waits for %q, want scaffold", holder)
	}
}

func TestValidateRejectsUnknownIsolation(t *testing.T) {
	rs := newStore(t)
	r, err := rs.New("g/p", "goal", 3)
	if err != nil {
		t.Fatal(err)
	}
	j := job("docs")
	j.Isolation = "container"
	if _, err := rs.Submit(r.ID, []Job{j}); err == nil {
		t.Fatal("isolation: container was accepted")
	} else if !strings.Contains(err.Error(), "worktree") {
		t.Errorf("error does not say what to use instead: %v", err)
	}
}

func TestValidateNormalisesIsolation(t *testing.T) {
	rs := newStore(t)
	r, err := rs.New("g/p", "goal", 3)
	if err != nil {
		t.Fatal(err)
	}
	j := job("docs")
	j.Isolation = "  Worktree "
	r, err = rs.Submit(r.ID, []Job{j})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Jobs[0].Isolated() {
		t.Errorf("isolation is %q, want it normalised to %q", r.Jobs[0].Isolation, IsolationWorktree)
	}
}

func TestParseJobsReadsIsolation(t *testing.T) {
	jobs, err := ParseJobs([]byte(`
jobs:
  - id: docs
    title: Write the docs
    exclusive: repo
    isolation: worktree
    prompt: |
      Write docs/physics.md
  - id: land
    title: Merge it
    needs: [docs]
    exclusive: repo
    prompt: |
      Merge the branch
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("parsed %d jobs", len(jobs))
	}
	if !jobs[0].Isolated() {
		t.Errorf("docs isolation is %q", jobs[0].Isolation)
	}
	if jobs[1].Isolated() {
		t.Errorf("land was given isolation %q it did not ask for", jobs[1].Isolation)
	}
}

// Worktrees live outside the project, flattened to one folder per project, so
// the scanner cannot mistake one for a project and two runs cannot collide.
func TestWorktreePathIsOutsideTheProject(t *testing.T) {
	p := WorktreePath("/state/worktrees", "group/overstag", "74ce78", "docs")
	if want := "/state/worktrees/group-overstag/74ce78-docs"; p != want {
		t.Errorf("path is %q, want %q", p, want)
	}
	if other := WorktreePath("/state/worktrees", "group/overstag", "aa11bb", "docs"); other == p {
		t.Error("two runs share one worktree folder")
	}
}
