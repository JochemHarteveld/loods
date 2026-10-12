// Package scaffold brings a new project into existence: the folder, a git
// repository and a plan, within a second.
//
// Creating the folder up front rather than letting the first agent do it is
// what makes a run visible: a run belongs to a project, a project is a folder
// under the root, and the card has to be on the board before anything is built
// — so whatever happens to the agent, the project exists with its goal as the
// next step.
package scaffold

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"loods/internal/plan"
	"loods/internal/run"
)

// Plans is the part of the plan store this needs: write the goal down.
type Plans interface {
	Update(rel string, fn func(*plan.Plan) error) (plan.Plan, error)
}

// New creates <root>/[group/]<name> as a git repository with a plan, and
// returns the project's id on the board and its path.
func New(root, group, name, goal string, plans Plans) (rel, path string, err error) {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return "", "", errors.New("say what to build: that is what the orchestrator gets")
	}
	rel, path, err = Dir(root, group, name)
	if err != nil {
		return "", "", err
	}
	if err := gitInit(path); err != nil {
		os.Remove(path) // only succeeds while it is still empty
		return "", "", err
	}
	if _, err := plans.Update(rel, func(p *plan.Plan) error {
		p.Status = "idea"
		p.Next = goal
		p.AddLog("you", "created by loods: "+goal)
		return nil
	}); err != nil {
		return "", "", err
	}
	return rel, path, nil
}

// Dir checks the name and makes the folder. The name is the project's id on
// the board and part of every ref you type ("huizenzoeker#4"), so it is held to
// the same characters as a job id.
func Dir(root, group, name string) (rel, path string, err error) {
	name = strings.Trim(strings.TrimSpace(name), "/")
	group = strings.Trim(strings.TrimSpace(group), "/")
	for _, part := range []string{name, group} {
		if strings.Contains(part, "/") || strings.Contains(part, "..") {
			return "", "", fmt.Errorf("%q is not a name: use one folder name, and -g for the group", part)
		}
	}
	if err := run.ValidID(strings.ToLower(name)); err != nil {
		return "", "", fmt.Errorf("project name: %w", err)
	}
	if name != strings.ToLower(name) {
		return "", "", fmt.Errorf("project name %q: use lowercase, so refs stay easy to type", name)
	}
	rel = name
	if group != "" {
		rel = group + "/" + name
	}
	path = filepath.Join(root, filepath.FromSlash(rel))
	if _, err := os.Stat(path); err == nil {
		return "", "", fmt.Errorf("%s already exists", path)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", "", err
	}
	return rel, path, nil
}

// gitInit makes it a repository straight away, so the board shows it as one and
// the first agent has somewhere to commit. The remote is the repo job's work.
func gitInit(path string) error {
	cmd := exec.Command("git", "init", "-b", "main")
	cmd.Dir = path
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
