package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditHooksKeepsOtherSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	orig := `{
  "model": "opus",
  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "notify"}]}]},
  "permissions": {"allow": ["Bash(ls:*)"]}
}
`
	os.WriteFile(path, []byte(orig), 0o640)
	if changed, err := EditHooks(path, true); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if changed, _ := EditHooks(path, true); changed {
		t.Error("second install changed the file again")
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	if !(strings.Index(s, `"model"`) < strings.Index(s, `"hooks"`) && strings.Index(s, `"hooks"`) < strings.Index(s, `"permissions"`)) {
		t.Errorf("key order changed:\n%s", s)
	}
	if !strings.Contains(s, `"notify"`) || !strings.Contains(s, `loods hook"`) || !strings.Contains(s, "SessionStart") {
		t.Errorf("hooks wrong:\n%s", s)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
	if changed, err := EditHooks(path, false); err != nil || !changed {
		t.Fatal(changed, err)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "SessionStart") || !strings.Contains(string(b), `"notify"`) {
		t.Errorf("uninstall left:\n%s", b)
	}
}

// The orchestrate skill is the only place the lessons of a finished run are
// written down, so the rules that cost a run an hour to learn are asserted
// here: a graph is cut by coupling, the boundary is declared in the graph, the
// harness is shared, and the job that merges neither designs nor judges.
func TestOrchestrateSkillCarriesTheSeamRules(t *testing.T) {
	for _, want := range []string{
		"Cut the seams by coupling, not by file",
		"`owns: [paths]`",
		"One owner per acceptance number",
		"Build the shared harness first",
		"merges and does not design",
		"Give the judging to someone else",
	} {
		if !strings.Contains(orchestrateSkill, want) {
			t.Errorf("the orchestrate skill does not mention %q", want)
		}
	}
}

// Numbered steps are what the skill is read by, so a renumbering that drops or
// repeats one is caught here rather than by a confused orchestrator.
func TestOrchestrateSkillStepsAreInOrder(t *testing.T) {
	n := 0
	for _, line := range strings.Split(orchestrateSkill, "\n") {
		var got int
		if _, err := fmt.Sscanf(line, "%d. ", &got); err != nil {
			continue
		}
		n++
		if got != n {
			t.Fatalf("step %d is numbered %d:\n%s", n, got, line)
		}
	}
	if n < 8 {
		t.Errorf("found %d numbered steps, expected the whole walkthrough", n)
	}
}
