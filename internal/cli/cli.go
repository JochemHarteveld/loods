// Package cli is everything loods does without its window: show and edit a
// plan, hand tasks to agents and get them back, run graphs of jobs, create a
// project, install Claude's skills, undo an archive.
//
// The commands only ever write state. Starting a session is the server's job,
// because a session has to outlive the command that asked for it and show up
// in the dock; without a server the state is still correct, so a command says
// so instead of silently doing nothing.
package cli

import (
	"fmt"
	"io"
)

// Env is what every command needs to know about this machine: where the
// projects are and where to reach a running board.
type Env struct {
	Root    string
	Archive string
	Depth   int
	Port    int
}

// Command is one subcommand of loods.
type Command struct {
	Name string
	// Help is its lines in `loods -h`, ready to print.
	Help string
	Run  func(env Env, args []string) error
}

// commands is the whole list, in the order `loods -h` shows them. A new
// subcommand is one entry here plus its own file.
var commands = []Command{
	{
		Name: "new",
		Help: "  loods new <name> \"<what to build>\"\n" +
			"                            create a project and hand the goal to an orchestrator agent (loods new -h)",
		Run: runNew,
	},
	{
		Name: "run",
		Help: "  loods run [command]       the runs of a project: submit a graph of jobs, follow it, report back (loods run -h)",
		Run:  runRun,
	},
	{
		Name: "plan",
		Help: "  loods plan [command]      show or edit the plan of the project you are in (loods plan -h)",
		Run:  runPlan,
	},
	{
		Name: "todo",
		Help: "  loods todo [command]      list, claim and finish numbered tasks, for you or an agent (loods todo -h)",
		Run:  runTodo,
	},
	{
		Name: "deploy",
		Help: "  loods deploy [command]    publish this project at <project>.loods.site (loods deploy -h)",
		Run:  runDeploy,
	},
	{
		Name: "edge",
		Help: "  loods edge                the origin behind the tunnel: route *.<domain> to what is deployed (loods edge -h)",
		Run:  runEdge,
	},
	{
		Name: "claude",
		Help: "  loods claude install      add the /wrapup, /todo and /orchestrate skills and a SessionStart hook\n" +
			"  loods claude uninstall    remove them again",
		Run: runClaude,
	},
	{
		Name: "hook",
		Help: "  loods hook                the SessionStart hook itself (reads Claude's JSON on stdin)",
		Run:  runHook,
	},
	{
		Name: "undo",
		Help: "  loods undo                restore the last archived batch",
		Run:  runUndo,
	},
}

// Lookup finds the subcommand a word names.
func Lookup(name string) (Command, bool) {
	for _, c := range commands {
		if c.Name == name {
			return c, true
		}
	}
	return Command{}, false
}

// WriteHelp prints the subcommand lines of `loods -h`.
func WriteHelp(w io.Writer) {
	for _, c := range commands {
		fmt.Fprintln(w, c.Help)
	}
}
