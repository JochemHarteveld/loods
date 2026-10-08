package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// `loods new <name> "<what to build>"`: the folder, a git repository and a plan
// exist within a second, so the project is on the board before anything is
// built; then an orchestrator session is started in it to do the rest (see
// agents.go). Everything the orchestrator and its jobs do after that — the
// GitHub repository, the user stories, the tickets, the scaffold — shows up on
// that project's page as it happens.
//
// Creating the folder here rather than letting the first agent do it is what
// makes the run visible: a run belongs to a project, and a project is a folder
// under the root.

const newUsage = `usage: loods new [-g group] [--max-parallel n] <name> <what to build>

  loods new huizenzoeker "a house-hunting site with svelte and express"

Creates <root>/[group/]<name> as a git repository with a plan, then hands the
goal to an orchestrator agent: it plans the work as a graph of jobs, and loods
runs those jobs (several at a time where they do not depend on each other) and
shows them on the project's page.

  -g <group>          put it in a group folder, like blauweschuit/
  --max-parallel <n>  sessions at a time (default %d)
  --no-agent          only create the project and the plan, no agent
`

func runNew(args []string, root string, port int) error {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	group := fs.String("g", "", "group folder to create it in")
	maxPar := fs.Int("max-parallel", 0, "sessions at a time")
	noAgent := fs.Bool("no-agent", false, "only create the project, do not start an agent")
	asJSON := fs.Bool("json", false, "machine-readable output")
	fs.Usage = func() { fmt.Fprintf(os.Stderr, newUsage, defaultMaxPar) }
	words, err := parseInterleaved(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if len(words) < 2 {
		fs.Usage()
		return errors.New("new needs a name and what to build")
	}
	name, goal := words[0], strings.Join(words[1:], " ")
	rel, path, err := createProject(root, *group, name, goal)
	if err != nil {
		return err
	}
	fmt.Printf("%s: %s\n", rel, path)

	if *noAgent {
		fmt.Println("no agent started (--no-agent); the project is on the board with its goal as the next step")
		return nil
	}
	run, err := startRunOnServer(port, rel, goal, *maxPar)
	if err != nil {
		// The project is real either way; say what did not happen and how to
		// pick it up, instead of leaving half a project and an error.
		fmt.Fprintf(os.Stderr, "loods: the project is created, but no agent was started: %v\n", err)
		fmt.Fprintf(os.Stderr, "start loods and run: loods run new %q -p %s\n", goal, rel)
		return nil
	}
	if *asJSON {
		return writeJSONTo(os.Stdout, run)
	}
	fmt.Printf("run %s: an orchestrator is planning the work\n", run.ID)
	fmt.Printf("follow it with `loods run status %s --watch`, or on the board\n", run.ID)
	return nil
}

// createProject is what both `loods new` and the board's + button do before any
// agent is involved: the folder, a git repository and a plan, so the project is
// on the board with its goal as the next step whatever happens after.
func createProject(root, group, name, goal string) (rel, path string, err error) {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return "", "", errors.New("say what to build: that is what the orchestrator gets")
	}
	rel, path, err = newProjectDir(root, group, name)
	if err != nil {
		return "", "", err
	}
	if err := gitInit(path); err != nil {
		os.Remove(path) // only succeeds while it is still empty
		return "", "", err
	}
	ps := newPlanStore(plansPath())
	ps.Refresh()
	if _, err := ps.Update(rel, func(p *Plan) error {
		p.Status = "idea"
		p.Next = goal
		p.addLog("you", "created by loods: "+goal)
		return nil
	}); err != nil {
		return "", "", err
	}
	return rel, path, nil
}

// newProjectDir checks the name and makes the folder. The name is the project's
// id on the board and part of every ref you type ("huizenzoeker#4"), so it is
// held to the same characters as a job id.
func newProjectDir(root, group, name string) (rel, path string, err error) {
	name = strings.Trim(strings.TrimSpace(name), "/")
	group = strings.Trim(strings.TrimSpace(group), "/")
	for _, part := range []string{name, group} {
		if strings.Contains(part, "/") || strings.Contains(part, "..") {
			return "", "", fmt.Errorf("%q is not a name: use one folder name, and -g for the group", part)
		}
	}
	if err := validJobID(strings.ToLower(name)); err != nil {
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

// startRunOnServer asks the running loods to make the run and start the
// orchestrator. It has to be the server: the session must outlive this command
// and belong to the dock, which is where you watch it.
func startRunOnServer(port int, rel, goal string, maxPar int) (AgentRun, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !alreadyRunning(url) {
		return AgentRun{}, fmt.Errorf("no loods server on %s", url)
	}
	body, _ := json.Marshal(map[string]any{"project": rel, "goal": goal, "max_parallel": maxPar})
	req, err := http.NewRequest(http.MethodPost, url+"/api/runs/start", bytes.NewReader(body))
	if err != nil {
		return AgentRun{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Loods", "1")
	c := http.Client{Timeout: 15 * time.Second} // the server may rescan first
	resp, err := c.Do(req)
	if err != nil {
		return AgentRun{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := readAllLimit(resp.Body, 4<<10)
		return AgentRun{}, errors.New(strings.TrimSpace(msg))
	}
	var run AgentRun
	if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
		return AgentRun{}, err
	}
	return run, nil
}
