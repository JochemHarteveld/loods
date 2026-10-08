package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestAgentSpec(t *testing.T) {
	it := &Item{Path: "/home/x/Projects/loods", Rel: "loods", Name: "loods"}
	spec := agentSpec(it, Task{ID: 4, Text: "Fix the board's `fetch` of plans"})
	if spec.ID != "loods#agent:4" || spec.Kind != "agent" || spec.Task != 4 {
		t.Fatalf("spec identity: %+v", spec)
	}
	if spec.Dir != it.Path {
		t.Errorf("dir = %q, want the project itself", spec.Dir)
	}
	if !strings.HasPrefix(spec.Run, agentCommand()+" '") {
		t.Errorf("run = %q, want one quoted prompt for %s", spec.Run, agentCommand())
	}
	// The task text goes through bash: a backtick in it must stay text.
	out, err := exec.Command("bash", "-lc", "printf %s "+strings.TrimPrefix(spec.Run, agentCommand()+" ")).Output()
	if err != nil {
		t.Fatalf("the prompt is not one safe argument: %v", err)
	}
	if got := string(out); got != agentPrompt("loods", 4, "Fix the board's `fetch` of plans") {
		t.Errorf("prompt survived the shell as %q", got)
	}
	if !strings.Contains(spec.Run, "loods todo claim loods#4") {
		t.Errorf("the prompt does not tell the agent to claim the task: %q", spec.Run)
	}
}

func TestShQuote(t *testing.T) {
	for _, in := range []string{"plain", "it's", `$HOME and "quotes"`, "back`tick`"} {
		out, err := exec.Command("bash", "-lc", "printf %s "+shQuote(in)).Output()
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if string(out) != in {
			t.Errorf("%q came back as %q", in, out)
		}
	}
}
