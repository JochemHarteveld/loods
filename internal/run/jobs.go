package run

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// jobInput is the job as a graph file may write it: the fields an orchestrator
// gets to set, and nothing else. Statuses and process ids are loods's to fill
// in, so a file cannot claim a job is already done.
type jobInput struct {
	ID        string   `yaml:"id"`
	Role      string   `yaml:"role"`
	Title     string   `yaml:"title"`
	Prompt    string   `yaml:"prompt"`
	Needs     []string `yaml:"needs"`
	Exclusive string   `yaml:"exclusive"`
	Isolation string   `yaml:"isolation"`
	Task      int      `yaml:"task"`
	// Owns is the paths this job is the only one allowed to change. See owns.go.
	Owns []string `yaml:"owns"`
	// Steps is the checklist, as a plain list of lines: the whole job cut into
	// pieces small enough that ticking one off means something.
	Steps []string `yaml:"steps"`
}

// ParseJobs reads a graph. Both `jobs:` with a list under it and a bare list
// are accepted, because both are what people write.
func ParseJobs(b []byte) ([]Job, error) {
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, errors.New("the graph is empty")
	}
	var wrapped struct {
		Jobs []jobInput `yaml:"jobs"`
	}
	var bare []jobInput
	in := []jobInput(nil)
	switch {
	case yaml.Unmarshal(b, &wrapped) == nil && len(wrapped.Jobs) > 0:
		in = wrapped.Jobs
	case yaml.Unmarshal(b, &bare) == nil && len(bare) > 0:
		in = bare
	default:
		if err := yaml.Unmarshal(b, &wrapped); err != nil {
			return nil, fmt.Errorf("the graph is not readable: %w", err)
		}
		return nil, errors.New("the graph has no jobs: a list under `jobs:`, or a bare list")
	}
	out := make([]Job, 0, len(in))
	for _, j := range in {
		steps := make([]Step, 0, len(j.Steps))
		for _, text := range j.Steps {
			steps = append(steps, Step{Text: text})
		}
		out = append(out, Job{
			ID: j.ID, Role: j.Role, Title: j.Title, Prompt: j.Prompt,
			Needs: j.Needs, Exclusive: j.Exclusive, Isolation: j.Isolation, Task: j.Task,
			Owns: j.Owns, Steps: steps, Status: JobPending,
		})
	}
	return out, nil
}
