package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// An agent is a Claude Code session loods starts for one task of a planboard.
// It is an ordinary Manager process — a real terminal under `bash -lc` — so it
// gets the same output backlog, websocket terminal and stop / restart as a dev
// server, and several can run at once. What makes it an agent is its Spec: kind
// "agent" plus the task number it was handed, which is how the board knows
// which card to mark and which terminal belongs to which task.
//
// loods never claims the task for the agent: a claim says who is really working
// on it, and that is the session's own statement (`loods todo claim`, step 2 of
// the /todo skill). Until it claims, the card shows the agent as starting.

// agentCommand is the Claude Code CLI. LOODS_AGENT_COMMAND replaces it, which
// is how the tests run a whole run without starting real sessions.
func agentCommand() string {
	if c := os.Getenv("LOODS_AGENT_COMMAND"); c != "" {
		return c
	}
	return "claude"
}

func agentID(rel string, task int) string { return fmt.Sprintf("%s#agent:%d", rel, task) }

// agentPrompt is what the session starts with. It names the task by number
// (numbers are stable, so "#4" keeps meaning this task) and spells out the loop
// itself, so the agent still claims and reports back when the /todo skill is
// not installed.
func agentPrompt(rel string, task int, text string) string {
	ref := fmt.Sprintf("%s#%d", rel, task)
	return fmt.Sprintf("Pick up todo %s from the loods planboard: %s\n\n"+
		"Follow the /todo skill: run `loods todo claim %s` before you touch anything, "+
		"`loods todo heartbeat %s` as you go, and finish with "+
		"`loods todo done %s --note \"what you did\"` (or `loods todo release %s --note \"why\"` "+
		"if you stop or get blocked).", ref, text, ref, ref, ref, ref)
}

func agentSpec(it *Item, task Task) Spec {
	return Spec{
		ID:      agentID(it.Rel, task.ID),
		Project: it.Rel,
		Kind:    "agent",
		Task:    task.ID,
		Name:    fmt.Sprintf("agent #%d", task.ID),
		Run:     agentCommand() + " " + shQuote(agentPrompt(it.Rel, task.ID, task.Text)),
		Dir:     it.Path,
	}
}

// shQuote wraps a string for `bash -lc`, so a task text with quotes, backticks
// or a $ in it is still one argument and nothing in it is expanded.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func agentAvailable() error {
	cmd := agentCommand()
	if _, err := exec.LookPath(strings.Fields(cmd)[0]); err != nil {
		return fmt.Errorf("%s is not on PATH: install Claude Code to hand tasks to an agent", cmd)
	}
	return nil
}

// A run adds two more kinds of agent on top of the single-task one above: the
// orchestrator that owns a run, and one session per job it hands out. Both are
// ordinary Manager processes again, so the dock shows them next to the dev
// servers and `stop` works the same; Spec.RunID / Job / Role are what lets the
// Agents view draw the jobs under their orchestrator.
//
// The prompts spell out the whole loop, like agentPrompt does: the skills from
// `loods claude install` say the same thing in more words, but a session started
// without them still has to know how to report back, or the run would wait for
// an agent that never says anything.

func orchestratorID(rel, run string) string { return fmt.Sprintf("%s#run:%s", rel, run) }

func jobProcID(rel, run, job string) string { return fmt.Sprintf("%s#job:%s:%s", rel, run, job) }

func orchestratorSpec(it *Item, r AgentRun) Spec {
	return Spec{
		ID:      orchestratorID(it.Rel, r.ID),
		Project: it.Rel,
		Kind:    "orchestrator",
		RunID:   r.ID,
		Role:    "orchestrator",
		Name:    "orchestrator " + r.ID,
		Run:     agentCommand() + " " + shQuote(orchestratorPrompt(it, r)),
		Dir:     it.Path,
	}
}

func jobSpec(it *Item, r AgentRun, j Job) Spec {
	return Spec{
		ID:      jobProcID(it.Rel, r.ID, j.ID),
		Project: it.Rel,
		Kind:    "job",
		RunID:   r.ID,
		Job:     j.ID,
		Role:    j.Role,
		Task:    j.Task,
		Name:    j.Role + " " + j.ID,
		Run:     agentCommand() + " " + shQuote(jobPrompt(it, r, j)),
		Dir:     it.Path,
	}
}

// orchestratorPrompt hands over the goal and the rules of the graph. It does
// not describe the project: the SessionStart hook already puts the plan in
// context, and the goal is the part the session cannot look up.
func orchestratorPrompt(it *Item, r AgentRun) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are the orchestrator of loods run %s, in the project %s (%s).\n\n", r.ID, it.Rel, it.Path)
	fmt.Fprintf(&b, "Goal: %s\n\n", r.Goal)
	fmt.Fprintf(&b, "Follow the /orchestrate skill. In short: plan the work as a graph of jobs, "+
		"hand the graph to loods and let it run them. You do not do the jobs yourself, "+
		"and you do not decide what may run at the same time: loods starts a job once "+
		"everything it needs is done, at most %d sessions at a time, and never two jobs "+
		"that hold the same lock (`exclusive: repo` for anything that commits, because "+
		"two sessions in one working tree race over the index).\n\n", r.MaxPar)
	b.WriteString("1. Write the graph and submit it (one job per piece of work, each with its own prompt):\n\n")
	fmt.Fprintf(&b, "   loods run submit %s --file - <<'JOBS'\n", r.ID)
	b.WriteString(defaultGraph)
	b.WriteString("   JOBS\n\n")
	fmt.Fprintf(&b, "2. Watch it: `loods run status %s --watch`. Jobs report back themselves; "+
		"you only have to react when one fails or when what you learn changes the plan.\n", r.ID)
	fmt.Fprintf(&b, "3. Add work you did not foresee: `loods run extend %s --file -` with the same "+
		"format. New jobs may depend on jobs that are already done.\n", r.ID)
	fmt.Fprintf(&b, "4. When the run is over, record where the project stands: `loods plan next \"…\"` "+
		"and `loods plan note \"…\"`, then tell the user in a few lines what was built and what failed.\n")
	return b.String()
}

// defaultGraph is the starting point for a new project: the repository and the
// user stories have nothing to do with each other and run together, tickets
// come out of the stories, and the scaffold waits for the repository because
// both commit. The orchestrator is expected to change the prompts to the actual
// goal and to add jobs; it is a template, not a script.
const defaultGraph = `   jobs:
     - id: repo
       role: git
       title: Create the repository and push it to GitHub
       exclusive: repo
       prompt: |
         Make this folder a git repository with a .gitignore and a README that
         names the project and its stack, commit it, then create a private
         GitHub repository with gh and push. Report the repository URL.
     - id: stories
       role: stories
       title: Write the user stories
       prompt: |
         Write docs/userstories.md: the user roles, and per role the user
         stories that cover the goal, each with acceptance criteria. Keep them
         small enough to become one ticket each.
     - id: tickets
       role: tickets
       needs: [stories]
       title: Turn the user stories into tasks on the planboard
       prompt: |
         Read docs/userstories.md and add one task per story with
         'loods todo add "…"'. Each task has to be actionable on its own.
         Report the numbers you added.
     - id: scaffold
       role: scaffold
       needs: [repo]
       exclusive: repo
       title: Scaffold the application
       prompt: |
         Set up the project skeleton for the stack in the goal, with a working
         dev command and a README that says how to run it. Commit and push.
`

// jobPrompt is what one job's session starts with: what to build, and how to
// report back. Nothing that depends on this job starts until it does, which is
// why that sentence is in the prompt.
func jobPrompt(it *Item, r AgentRun, j Job) string {
	ref := r.ID + ":" + j.ID
	var b strings.Builder
	fmt.Fprintf(&b, "You are the %s agent of loods run %s in %s. The run's goal: %s\n\n", j.Role, r.ID, it.Rel, r.Goal)
	fmt.Fprintf(&b, "Your job (%s): %s\n\n%s\n\n", ref, j.Title, j.Prompt)
	if j.Task > 0 {
		fmt.Fprintf(&b, "This job is task #%d on the planboard: run `loods todo claim %s#%d` before "+
			"you start and `loods todo heartbeat %s#%d` as you go, so the board shows it as live work.\n\n",
			j.Task, it.Rel, j.Task, it.Rel, j.Task)
	}
	fmt.Fprintf(&b, "Report back when you are done — nothing that depends on your job starts until you do:\n"+
		"  loods run done %s --note \"what you did\"\n"+
		"or, if you cannot finish it:\n"+
		"  loods run fail %s --note \"why, and what you did leave behind\"\n\n", ref, ref)
	b.WriteString("Stay inside your job. Work you discover that belongs to someone else goes on the board " +
		"(`loods todo add \"…\"`) or in your report, not into this session.")
	return b.String()
}
