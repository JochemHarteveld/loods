package agent

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"loods/internal/plan"
	"loods/internal/project"
	"loods/internal/run"
)

func TestAgentSpec(t *testing.T) {
	it := &project.Item{Path: "/home/x/Projects/loods", Rel: "loods", Name: "loods"}
	spec := New().TaskSpec(it, plan.Task{ID: 4, Text: "Fix the board's `fetch` of plans"})
	if spec.ID != "loods#agent:4" || spec.Kind != "agent" || spec.Task != 4 {
		t.Fatalf("spec identity: %+v", spec)
	}
	if spec.Dir != it.Path {
		t.Errorf("dir = %q, want the project itself", spec.Dir)
	}
	if !strings.HasPrefix(spec.Run, Command()+" '") {
		t.Errorf("run = %q, want one quoted prompt for %s", spec.Run, Command())
	}
	// The task text goes through bash: a backtick in it must stay text.
	out, err := exec.Command("bash", "-lc", "printf %s "+strings.TrimPrefix(spec.Run, Command()+" ")).Output()
	if err != nil {
		t.Fatalf("the prompt is not one safe argument: %v", err)
	}
	if got := string(out); got != taskPrompt("loods", 4, "Fix the board's `fetch` of plans") {
		t.Errorf("prompt survived the shell as %q", got)
	}
	if !strings.Contains(spec.Run, "loods todo claim loods#4") {
		t.Errorf("the prompt does not tell the agent to claim the task: %q", spec.Run)
	}
}

func TestShQuote(t *testing.T) {
	for _, in := range []string{"plain", "it's", `$HOME and "quotes"`, "back`tick`"} {
		out, err := exec.Command("bash", "-lc", "printf %s "+ShQuote(in)).Output()
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if string(out) != in {
			t.Errorf("%q came back as %q", in, out)
		}
	}
}

// What the agents actually read. The prompts have to name the run, the job and
// the exact commands to report back, because a session without the skills
// installed has nothing else to go on.
func TestPromptsSayHowToReportBack(t *testing.T) {
	it := &project.Item{Rel: "g/huizen", Path: "/home/u/Projects/g/huizen", Name: "huizen"}
	r := run.Run{ID: "4f2a1c", Project: it.Rel, Goal: "a house-hunting site", MaxPar: 3, Status: run.Running}
	j := run.Job{ID: "stories", Role: "stories", Title: "Write the user stories", Prompt: "Write docs/userstories.md", Task: 7}

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
	if plain := jobPrompt(it, r, run.Job{ID: "repo", Role: "git", Title: "t", Prompt: "p"}); strings.Contains(plain, "todo claim") {
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
	it := &project.Item{Rel: "g/huizen", Path: "/home/u/Projects/g/huizen", Name: "huizen"}
	r := run.Run{ID: "4f2a1c", Project: it.Rel, Goal: "goal", MaxPar: 3}
	o := New().OrchestratorSpec(it, r)
	if o.ID != "g/huizen#run:4f2a1c" || o.Kind != "orchestrator" || o.RunID != "4f2a1c" || o.Dir != it.Path {
		t.Fatalf("orchestrator spec: %+v", o)
	}
	j := New().JobSpec(it, r, run.Job{ID: "stories", Role: "stories", Title: "t", Prompt: "p", Task: 7})
	if j.ID != "g/huizen#job:4f2a1c:stories" || j.Kind != "job" || j.Job != "stories" || j.Role != "stories" || j.Task != 7 {
		t.Fatalf("job spec: %+v", j)
	}
	// Prompts go through one shell argument, whatever is in them.
	tricky := New().JobSpec(it, r, run.Job{ID: "x", Role: "r", Title: "t", Prompt: "it's `rm -rf /` and $HOME"})
	if !strings.HasPrefix(tricky.Run, "claude '") || strings.Contains(tricky.Run, "`rm -rf /`\"") {
		t.Fatalf("prompt not quoted for bash: %s", tricky.Run)
	}
}

// The template in the orchestrator's prompt has to be submittable as it stands:
// it is the first thing a run does, and a typo in it would block every new
// project.
func TestDefaultGraphSubmits(t *testing.T) {
	jobs, err := run.ParseJobs([]byte(DefaultGraph))
	if err != nil {
		t.Fatalf("the default graph does not parse: %v", err)
	}
	rs := run.NewStore(filepath.Join(t.TempDir(), "runs.json"))
	r, err := rs.New("g/p", "a house-hunting site", 3)
	if err != nil {
		t.Fatal(err)
	}
	if r, err = rs.Submit(r.ID, jobs); err != nil {
		t.Fatal(err)
	}
	if len(r.Jobs) != 4 {
		t.Fatalf("the default graph has %d jobs", len(r.Jobs))
	}
	var started []string
	rs.Tick(func(r run.Run, j run.Job) (string, error) {
		started = append(started, j.ID)
		return JobID(r.Project, r.ID, j.ID), nil
	})
	// repo and stories have nothing to do with each other; scaffold holds the
	// same lock as repo, tickets waits for stories.
	if len(started) != 2 || started[0] != "repo" || started[1] != "stories" {
		t.Fatalf("the default graph starts %v, want repo and stories", started)
	}
}
