package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jobs.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadJobsBothShapes(t *testing.T) {
	wrapped := `
jobs:
  - id: stories
    role: stories
    title: Write the user stories
    prompt: |
      Write docs/userstories.md
  - id: tickets
    needs: [stories]
    title: Turn them into tasks
    prompt: one task per story
    exclusive: repo
    task: 7
`
	bare := `
- id: stories
  title: Write the user stories
  prompt: Write docs/userstories.md
`
	jobs, err := readJobs(writeTemp(t, wrapped))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("read %d jobs, want 2", len(jobs))
	}
	if jobs[0].ID != "stories" || !strings.HasPrefix(jobs[0].Prompt, "Write docs") {
		t.Errorf("first job wrong: %+v", jobs[0])
	}
	if j := jobs[1]; j.Needs[0] != "stories" || j.Exclusive != "repo" || j.Task != 7 {
		t.Errorf("second job lost its fields: %+v", j)
	}
	if jobs, err := readJobs(writeTemp(t, bare)); err != nil || len(jobs) != 1 {
		t.Fatalf("a bare list read as %v (%v)", jobs, err)
	}
}

// A graph file says what to build, not what already happened: loods fills in the
// statuses, so a file cannot hand in a job that claims to be done.
func TestReadJobsIgnoresStatusInTheFile(t *testing.T) {
	jobs, err := readJobs(writeTemp(t, `
jobs:
  - id: repo
    title: t
    prompt: p
    status: done
    proc: someone/elses#job
    note: I promise
`))
	if err != nil {
		t.Fatal(err)
	}
	if jobs[0].Status != JobPending || jobs[0].ProcID != "" || jobs[0].Note != "" {
		t.Fatalf("the file set its own state: %+v", jobs[0])
	}
}

func TestReadJobsRejectsRubbish(t *testing.T) {
	for _, body := range []string{"", "   \n", "jobs:\n", "nothing: here\n", "[}\n"} {
		if _, err := readJobs(writeTemp(t, body)); err == nil {
			t.Errorf("readJobs(%q) was accepted", body)
		}
	}
	if _, err := readJobs(filepath.Join(t.TempDir(), "gone.yaml")); err == nil {
		t.Error("a missing file was accepted")
	}
}

// The template in the orchestrator's prompt has to be submittable as it stands:
// it is the first thing a run does, and a typo in it would block every new
// project.
func TestDefaultGraphSubmits(t *testing.T) {
	jobs, err := readJobs(writeTemp(t, defaultGraph))
	if err != nil {
		t.Fatalf("the default graph does not parse: %v", err)
	}
	rs := runStore(t)
	r := submitted(t, rs, 3, jobs...)
	if len(r.Jobs) != 4 {
		t.Fatalf("the default graph has %d jobs", len(r.Jobs))
	}
	var s started
	rs.Tick(s.start)
	// repo and stories have nothing to do with each other; scaffold holds the
	// same lock as repo, tickets waits for stories.
	if len(s.ids) != 2 || s.ids[0] != "repo" || s.ids[1] != "stories" {
		t.Fatalf("the default graph starts %v, want repo and stories", s.ids)
	}
}

func TestPickByPrefix(t *testing.T) {
	rs := runStore(t)
	a, err := rs.New("g/p", "one", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := pick(rs, a.ID); err != nil || got.ID != a.ID {
		t.Fatalf("exact id: %v %v", got.ID, err)
	}
	if got, err := pick(rs, a.ID[:3]); err != nil || got.ID != a.ID {
		t.Fatalf("prefix: %v %v", got.ID, err)
	}
	if _, err := pick(rs, "zzzzzz"); err == nil {
		t.Error("an unknown run was found")
	}
	if _, err := pick(rs, ""); err == nil {
		t.Error("an empty id matched something")
	}
}

func TestNewProjectDir(t *testing.T) {
	root := t.TempDir()
	rel, path, err := newProjectDir(root, "", "huizenzoeker")
	if err != nil {
		t.Fatal(err)
	}
	if rel != "huizenzoeker" || path != filepath.Join(root, "huizenzoeker") {
		t.Fatalf("rel %q path %q", rel, path)
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		t.Fatalf("folder not created: %v", err)
	}
	if _, _, err := newProjectDir(root, "", "huizenzoeker"); err == nil {
		t.Error("an existing folder was taken over")
	}
	rel, _, err = newProjectDir(root, "blauweschuit", "barsys2")
	if err != nil || rel != "blauweschuit/barsys2" {
		t.Fatalf("group: rel %q err %v", rel, err)
	}
	for _, name := range []string{"", "Huizen Zoeker", "../escape", "a/b", strings.Repeat("x", 33)} {
		if _, _, err := newProjectDir(root, "", name); err == nil {
			t.Errorf("name %q was accepted", name)
		}
	}
}

// What the agents actually read. The prompts have to name the run, the job and
// the exact commands to report back, because a session without the skills
// installed has nothing else to go on.
func TestPromptsSayHowToReportBack(t *testing.T) {
	it := &Item{Rel: "g/huizen", Path: "/home/u/Projects/g/huizen", Name: "huizen"}
	r := AgentRun{ID: "4f2a1c", Project: it.Rel, Goal: "a house-hunting site", MaxPar: 3, Status: RunRunning}
	j := Job{ID: "stories", Role: "stories", Title: "Write the user stories", Prompt: "Write docs/userstories.md", Task: 7}

	p := jobPrompt(it, r, j)
	for _, want := range []string{
		"4f2a1c", "stories agent", "Write docs/userstories.md",
		"loods run done 4f2a1c:stories --note", "loods run fail 4f2a1c:stories --note",
		"loods todo claim g/huizen#7", "loods todo heartbeat g/huizen#7",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the job prompt does not mention %q:\n%s", want, p)
		}
	}
	// A job that is not a planboard task should not be told to claim one.
	if plain := jobPrompt(it, r, Job{ID: "repo", Role: "git", Title: "t", Prompt: "p"}); strings.Contains(plain, "todo claim") {
		t.Error("a job without a task was told to claim one")
	}

	o := orchestratorPrompt(it, r)
	for _, want := range []string{
		"orchestrator of loods run 4f2a1c", "a house-hunting site", "/orchestrate skill",
		"loods run submit 4f2a1c --file -", "loods run status 4f2a1c --watch",
		"loods run extend 4f2a1c --file -", "at most 3 sessions",
	} {
		if !strings.Contains(o, want) {
			t.Errorf("the orchestrator prompt does not mention %q:\n%s", want, o)
		}
	}
}

func TestSpecsPlaceSessionsInTheirRun(t *testing.T) {
	it := &Item{Rel: "g/huizen", Path: "/home/u/Projects/g/huizen", Name: "huizen"}
	r := AgentRun{ID: "4f2a1c", Project: it.Rel, Goal: "goal", MaxPar: 3}
	o := orchestratorSpec(it, r)
	if o.ID != "g/huizen#run:4f2a1c" || o.Kind != "orchestrator" || o.RunID != "4f2a1c" || o.Dir != it.Path {
		t.Fatalf("orchestrator spec: %+v", o)
	}
	j := jobSpec(it, r, Job{ID: "stories", Role: "stories", Title: "t", Prompt: "p", Task: 7})
	if j.ID != "g/huizen#job:4f2a1c:stories" || j.Kind != "job" || j.Job != "stories" || j.Role != "stories" || j.Task != 7 {
		t.Fatalf("job spec: %+v", j)
	}
	// Prompts go through one shell argument, whatever is in them.
	tricky := jobSpec(it, r, Job{ID: "x", Role: "r", Title: "t", Prompt: "it's `rm -rf /` and $HOME"})
	if !strings.HasPrefix(tricky.Run, "claude '") || strings.Contains(tricky.Run, "`rm -rf /`\"") {
		t.Fatalf("prompt not quoted for bash: %s", tricky.Run)
	}
}
