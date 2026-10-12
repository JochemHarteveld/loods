package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"loods/internal/plan"
	"loods/internal/run"
	"loods/internal/scaffold"
	"loods/internal/xdg"
)

// `loods new <name> "<what to build>"`: the folder, a git repository and a plan
// exist within a second, so the project is on the board before anything is
// built; then an orchestrator session is started in it to do the rest.
// Everything the orchestrator and its jobs do after that — the GitHub
// repository, the user stories, the tickets, the scaffold — shows up on that
// project's page as it happens.

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

func runNew(env Env, args []string) error {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	group := fs.String("g", "", "group folder to create it in")
	maxPar := fs.Int("max-parallel", 0, "sessions at a time")
	noAgent := fs.Bool("no-agent", false, "only create the project, do not start an agent")
	asJSON := fs.Bool("json", false, "machine-readable output")
	fs.Usage = func() { fmt.Fprintf(os.Stderr, newUsage, run.DefaultMaxPar) }
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
	plans := plan.NewStore(xdg.PlansPath())
	plans.Refresh()
	rel, path, err := scaffold.New(env.Root, *group, name, goal, plans)
	if err != nil {
		return err
	}
	fmt.Printf("%s: %s\n", rel, path)

	if *noAgent {
		fmt.Println("no agent started (--no-agent); the project is on the board with its goal as the next step")
		return nil
	}
	r, err := startRunOnServer(env, rel, goal, *maxPar)
	if err != nil {
		// The project is real either way; say what did not happen and how to
		// pick it up, instead of leaving half a project and an error.
		fmt.Fprintf(os.Stderr, "loods: the project is created, but no agent was started: %v\n", err)
		fmt.Fprintf(os.Stderr, "start loods and run: loods run new %q -p %s\n", goal, rel)
		return nil
	}
	if *asJSON {
		return writeJSON(os.Stdout, r)
	}
	fmt.Printf("run %s: an orchestrator is planning the work\n", r.ID)
	fmt.Printf("follow it with `loods run status %s --watch`, or on the board\n", r.ID)
	return nil
}
