package cli

import (
	"os"
	"path/filepath"
	"testing"

	"loods/internal/run"
)

// readJobs is the file half of submitting a graph; the parsing itself is
// tested in the run package.
func TestReadJobsReadsAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.yaml")
	os.WriteFile(path, []byte("jobs:\n  - id: stories\n    title: t\n    prompt: p\n"), 0o644)
	jobs, err := readJobs(path)
	if err != nil || len(jobs) != 1 || jobs[0].ID != "stories" {
		t.Fatalf("readJobs = %+v, %v", jobs, err)
	}
	if _, err := readJobs(filepath.Join(t.TempDir(), "gone.yaml")); err == nil {
		t.Error("a missing file was accepted")
	}
}

func TestPickByPrefix(t *testing.T) {
	rs := run.NewStore(filepath.Join(t.TempDir(), "runs.json"))
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
