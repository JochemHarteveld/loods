package run

import (
	"strings"
	"testing"
)

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
	jobs, err := ParseJobs([]byte(wrapped))
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
	if jobs, err := ParseJobs([]byte(bare)); err != nil || len(jobs) != 1 {
		t.Fatalf("a bare list read as %v (%v)", jobs, err)
	}
}

// A graph file says what to build, not what already happened: loods fills in the
// statuses, so a file cannot hand in a job that claims to be done.
func TestReadJobsIgnoresStatusInTheFile(t *testing.T) {
	jobs, err := ParseJobs([]byte(`
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
		if _, err := ParseJobs([]byte(body)); err == nil {
			t.Errorf("ParseJobs(%q) was accepted", body)
		}
	}
}
