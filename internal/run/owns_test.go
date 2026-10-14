package run

import "testing"

func TestGlobsOverlap(t *testing.T) {
	yes := [][2]string{
		{"src/lib/ui/Hud.svelte", "src/lib/ui/Hud.svelte"},
		{"src/lib/ui", "src/lib/ui/Hud.svelte"}, // the folder above it
		{"src/lib/ui/*.svelte", "src/lib/ui/Hud.svelte"},
		{"src/**/*.ts", "src/lib/ui/inputs.ts"},
		{"e2e", "e2e/mobile-layout.spec.ts"},
		{"src/lib/ui/Hud*", "src/lib/ui/*.svelte"}, // two patterns that can meet
		{"docs/**", "docs/responsive/measure.mjs"},
	}
	for _, c := range yes {
		if !globsOverlap(c[0], c[1]) || !globsOverlap(c[1], c[0]) {
			t.Errorf("%q and %q should overlap", c[0], c[1])
		}
	}
	no := [][2]string{
		{"src/lib/ui/Hud.svelte", "src/lib/ui/Helm.svelte"},
		{"src/lib/ui", "src/lib/render"},
		{"src/lib/ui/*.ts", "src/lib/ui/*.svelte"},
		{"e2e/mobile-*.spec.ts", "e2e/desktop-*.spec.ts"},
		{"src/lib/**/*.css", "src/lib/ui/Hud.svelte"},
	}
	for _, c := range no {
		if globsOverlap(c[0], c[1]) || globsOverlap(c[1], c[0]) {
			t.Errorf("%q and %q should not overlap", c[0], c[1])
		}
	}
}

// A graph whose parallel lanes claim the same files is refused as a whole, and
// the error names both jobs so the orchestrator knows what to decide.
func TestSubmitRefusesOverlappingOwners(t *testing.T) {
	r := &Run{Jobs: []Job{
		{ID: "dock", Title: "Dock", Prompt: "x", Owns: []string{"src/lib/ui/Helm.svelte", "src/lib/ui/theme.css"}},
		{ID: "topbar", Title: "Top bar", Prompt: "x", Owns: []string{"src/lib/ui/Hud.svelte", "src/lib/ui/*.css"}},
	}}
	err := r.validate()
	if err == nil {
		t.Fatal("two lanes owning the same css were accepted")
	}
	for _, want := range []string{"dock", "topbar", "theme.css"} {
		if !contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// Jobs that take turns may share files: that is what a sequence is.
func TestSequentialJobsMayShareFiles(t *testing.T) {
	r := &Run{Jobs: []Job{
		{ID: "lane", Title: "Lane", Prompt: "x", Owns: []string{"src/lib/ui"}},
		{ID: "merge", Title: "Merge", Prompt: "x", Needs: []string{"lane"}, Owns: []string{"src/lib/ui"}},
	}}
	if err := r.validate(); err != nil {
		t.Fatalf("sequential jobs refused: %v", err)
	}
}

// Ownership is opt-in: a graph that writes none behaves exactly as before.
func TestNoOwnersNoCheck(t *testing.T) {
	r := &Run{Jobs: []Job{
		{ID: "a", Title: "A", Prompt: "x"},
		{ID: "b", Title: "B", Prompt: "x"},
	}}
	if err := r.validate(); err != nil {
		t.Fatalf("graph without owners refused: %v", err)
	}
}

func TestValidateOwnsCleansAndRefuses(t *testing.T) {
	j := &Job{ID: "a", Owns: []string{" src/lib/ui ", "./docs/x.md", "src/lib/ui", ""}}
	if err := j.validateOwns(); err != nil {
		t.Fatal(err)
	}
	if len(j.Owns) != 2 || j.Owns[0] != "src/lib/ui" || j.Owns[1] != "docs/x.md" {
		t.Errorf("owns = %q", j.Owns)
	}
	for _, bad := range []string{"/etc/passwd", "../other-project/src", ".."} {
		j := &Job{ID: "a", Owns: []string{bad}}
		if err := j.validateOwns(); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// An indirect dependency counts: a lane and the job that lands two lanes later
// are never in the water together.
func TestTransitiveDependencyAllowsSharing(t *testing.T) {
	r := &Run{Jobs: []Job{
		{ID: "lane", Title: "L", Prompt: "x", Owns: []string{"src/app.ts"}},
		{ID: "mid", Title: "M", Prompt: "x", Needs: []string{"lane"}},
		{ID: "land", Title: "D", Prompt: "x", Needs: []string{"mid"}, Owns: []string{"src/app.ts"}},
	}}
	if err := r.validate(); err != nil {
		t.Fatalf("transitively ordered jobs refused: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// A folder owns what is under it; a file does not, so a styles lane and a
// component lane are allowed to run side by side.
func TestFolderAndFileSemantics(t *testing.T) {
	if !globsOverlap("src/lib/ui", "src/lib/ui/sub/deep.svelte") {
		t.Error("a folder should own its whole subtree")
	}
	if globsOverlap("src/**/*.css", "src/lib/ui/Hud.svelte") {
		t.Error("nothing lives under a file")
	}
	if !globsOverlap("src/lib/ui/Hud.svelte/**", "src/lib/ui/Hud.svelte") {
		t.Error("an explicit /** should still mean a subtree")
	}
	if !globsOverlap(".github", ".github/workflows/ci.yml") {
		t.Error("a leading dot is a folder, not an extension")
	}
}

// The field has to survive the graph file: an orchestrator writes owns: in
// YAML and never touches the struct.
func TestParseJobsReadsOwns(t *testing.T) {
	jobs, err := ParseJobs([]byte(`
jobs:
  - id: dock
    title: The dock
    prompt: build the dock
    isolation: worktree
    owns:
      - src/lib/ui/Helm.svelte
      - src/lib/ui/SailTrim.svelte
    steps:
      - do it
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || len(jobs[0].Owns) != 2 || jobs[0].Owns[1] != "src/lib/ui/SailTrim.svelte" {
		t.Fatalf("owns = %#v", jobs)
	}
	r := &Run{Jobs: jobs}
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	if got := r.Owners()["dock"]; len(got) != 2 {
		t.Errorf("Owners() = %v", r.Owners())
	}
}
