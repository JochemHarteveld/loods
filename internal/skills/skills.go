// Package skills is what `loods claude install` puts in Claude Code's config:
// the /wrapup, /todo and /orchestrate skills, and a SessionStart hook that
// shows Claude where a project stands before the first prompt.
//
// It only ever touches what it wrote itself: a skill file without the marker
// below is left alone, and settings.json keeps every other key, its order and
// its formatting.
package skills

import (
	"maps"
	"slices"
)

// Marker says a skill file was written by loods, so uninstall only removes
// its own and install never overwrites one you wrote.
const Marker = "<!-- written by `loods claude install` -->"

const wrapupSkill = `---
name: wrapup
description: Record where this project stands on the loods board — what this session did, the next step, open tasks and status. Use when the user wraps up a session or asks to update the project plan.
allowed-tools: Bash(loods plan:*)
---
` + Marker + `

Update this project's plan on the loods board, so the next session (and the
user, looking at the board) knows where things stand.

1. Run ` + "`loods plan`" + ` to see the current plan.
2. Log what this session did in one or two concrete sentences: what changed,
   what is verified, what is not: ` + "`loods plan note \"…\"`" + `
3. Set the single most useful next step, specific enough to start on without
   rereading this session: ` + "`loods plan next \"…\"`" + `
4. Move tasks to the column they are in now: ` + "`loods plan done <n>`" + ` for
   finished ones, ` + "`loods plan doing <n>`" + ` for what is half-built, and add
   real, actionable follow-ups (` + "`loods plan task \"…\"`" + `). Skip if none.
5. Change the status only if it clearly changed
   (` + "`loods plan status idea|active|paused|shipped|dead`" + `). Leave priority
   to the user.

Then tell the user in two or three lines what you recorded.

Extra instructions from the user, if any: $ARGUMENTS
`

const todoSkill = `---
name: todo
description: Pick up, work on and finish numbered tasks from the loods planboard. Use when the user says "do todo #4 of <project>", asks which tasks are open or what to pick up, or hands you work from the board.
allowed-tools: Bash(loods todo:*), Bash(loods plan:*)
---
` + Marker + `

Work the user hands you from the loods planboard. Every task has a number that
stays with it, so "#4 of loods" always means the same task.

1. **Find the work.** ` + "`loods todo list`" + ` for this project,
   ` + "`loods todo list --all`" + ` for every project (highest priority first,
   with each project's status and next step). Add ` + "`--json`" + ` when you want
   to pick from it programmatically. The user often names one outright: "do todo
   #4 of loods".
2. **Claim it before you touch anything:** ` + "`loods todo claim <project>#<n>`" + `.
   That moves the task to in progress and shows on the board that you are on it,
   so the user can see what is being worked on and does not start it themselves.
   If another agent is actively on it the claim is refused: say so and stop. Only
   pass ` + "`--steal`" + ` when the user explicitly tells you to take it over.
3. **Stay visible.** Run ` + "`loods todo heartbeat <project>#<n>`" + ` as you go:
   after finishing a step, and before anything long. Without a heartbeat for ten
   minutes the board marks the task as stalled, which is how the user spots an
   agent that died.
4. **Finish it:** ` + "`loods todo done <project>#<n> --note \"what you did\"`" + `.
   The note lands in the project's log, so write what changed and what is
   verified, not "done".
5. **Or hand it back:** ` + "`loods todo release <project>#<n> --note \"why\"`" + `
   when you stop, get blocked, or the user changes direction. Never walk away from
   a claim: a task that looks claimed but is not being worked on is worse than an
   untouched one.
6. New work you discover while doing this belongs on the board too:
   ` + "`loods todo add \"…\"`" + ` (it prints the number). Do not quietly widen the
   task you claimed.

Tell the user which task you claimed, and at the end what you recorded.

Extra instructions from the user, if any: $ARGUMENTS
`

const orchestrateSkill = `---
name: orchestrate
description: Plan a goal as a graph of jobs and let loods run them as parallel agents. Use when the user asks for a whole project or feature to be built by several agents at once, when loods started you as the orchestrator of a run, or when they say "orchestrate", "spin up agents" or "maak hier een project van".
allowed-tools: Bash(loods run:*), Bash(loods new:*), Bash(loods plan:*), Bash(loods todo:*)
---
` + Marker + `

You split a goal into jobs, hand the graph to loods, and watch. loods starts
each job as its own Claude session in a terminal on the board, so the user sees
which agents are running and what each of them is doing.

**You do not do the jobs yourself**, and you do not decide what runs at the same
time: loods starts a job as soon as everything it ` + "`needs`" + ` is done, at most
--max-parallel sessions at once, and never two jobs that hold the same
` + "`exclusive`" + ` lock.

1. **Know what you are orchestrating.** ` + "`loods run list`" + ` for this project;
   loods names your run in the prompt it started you with. No project yet?
   ` + "`loods new <name> \"<what to build>\"`" + ` creates one and starts an
   orchestrator in it (that may be you, in the new project).
2. **Write the graph.** One job per piece of work, each with an ` + "`id`" + `, a
   ` + "`title`" + `, a ` + "`prompt`" + ` that is enough on its own (the agent
   reading it has the project in front of it but not your reasoning) and
   ` + "`steps`" + `, its checklist. Add ` + "`needs: [other ids]`" + ` only for
   real dependencies — anything you leave out runs in parallel, which is the
   point. Add ` + "`exclusive: repo`" + ` to every job that commits, because two
   sessions in one working tree fight over the index. Add ` + "`task: <n>`" + `
   when the job is a numbered task from the planboard, so the card shows it as
   live work.
3. **Cut every job into steps.** ` + "`steps:`" + ` is a list of lines: the small
   pieces the job is made of, decided by you before the job starts. The agent
   ticks one off with ` + "`loods run step <run>:<job> <n>`" + ` as it finishes
   it, and that is the only signal anyone has from outside the session: the
   Agents view turns the ticks into a percentage, a forecast of the time left
   and a checklist with a green mark per finished step, and ` + "`loods run status`" + `
   shows the same.
   - **3 to 8 steps per job.** Fewer and the bar is useless; more and the job
     should have been two jobs.
   - **Of roughly the same size.** The forecast is the pace of the steps a job
     already finished applied to the ones it has left, so one step that takes
     twenty minutes between four that take two makes it lie.
   - **Each either done or not.** "Write the login form" is a step; "think about
     auth" is not. Name the artefact — a file, a command that runs, a pushed
     commit — so the agent knows when to tick.
   - A job with no steps still runs, but it shows as a session that has been
     busy for eleven minutes and nothing more. Write them.
4. **Cut the seams by coupling, not by file.** A lane is a thing with one
   answer in it: a region of the screen, a module behind an interface, one
   layer of the stack, a data contract. "These files are yours" is not a seam —
   if two lanes have to agree on an answer to finish, they are one lane, and
   the agreement will be made twice and reconciled by whoever merges them.
   - **Write the boundary into the graph, not into a doc.** ` + "`owns: [paths]`" + `
     on every parallel lane: globs relative to the project
     (` + "`src/lib/ui`" + `, ` + "`e2e/mobile-*.spec.ts`" + `, ` + "`docs/**`" + `).
     A graph where two jobs that can run at the same time claim the same files
     is refused when you submit it, which is the cheap moment to learn it.
     Jobs that wait for each other may share files — taking turns is what a
     sequence is for.
   - **One owner per acceptance number.** Every number in the contract a lane
     works against has to be passable by one lane, with only the files that
     lane owns. A number that needs two lanes to move is not an acceptance
     criterion yet: move the boundary until it is, or give both halves to one
     job. Otherwise every lane reports green and the number is still red.
   - **Build the shared harness first.** The job after the audit writes the
     measuring script, the test fixture or the seed data that every lane
     verifies against, and commits it. Skip it and each lane writes its own
     throwaway probe, throws it away, and reports numbers nobody can compare.
   - **Carve the seams first.** If two jobs only clash because the code has no
     extension point, a short serialised job that adds one (a registry, a slot,
     a shared type) turns them into jobs that genuinely do not overlap. That
     trade — one small job to unlock several — is usually the best one on the
     board.
5. **Let the jobs that commit run at the same time anyway.** ` + "`exclusive: repo`" + `
   is what serialises a graph: in a project where nearly every job commits, the
   run becomes a queue of one. Add ` + "`isolation: worktree`" + ` and the job
   gets a git worktree and a branch (` + "`loods/<run>/<job>`" + `) of its own,
   so its lock is scoped to its own tree and it waits for nobody. The cost is a
   branch and a merge, so spend it where it buys something:
   - **Worth it** for lanes whose ` + "`owns:`" + ` do not touch — docs, a test
     suite, a deploy config, a self-contained module, one region of a UI. Three
     of those finish in the time one of them used to take.
   - **Not worth it** for jobs that edit the same hot files (the game loop, the
     root page, a shared type module). They conflict on merge instead of
     waiting for a lock, and you pay for it in the merge job.
   - Worktrees start without installed dependencies, so an isolated job that
     builds or tests pays an install. Say so in its prompt.
6. **End with an integrate job that merges and does not design.** A worktree
   branch is nobody's until a job lands it: one ` + "`exclusive: repo`" + ` job
   that needs them all, merges each branch, re-runs the build and the tests,
   removes the worktrees with ` + "`git worktree remove`" + ` and pushes.
   - **It may not decide what a lane should have decided.** Tell it so in its
     prompt: a conflict it cannot resolve mechanically, or a question the lanes
     answered two ways, goes back to you (` + "`loods run extend`" + `) or onto
     the board — it does not get redesigned here. An integrate job that quietly
     becomes a fourth implementation job is the bill for a bad seam, and it is
     serial, so it is the most expensive hour in the run.
   - **Give the judging to someone else.** Add a read-only verify job that runs
     the shared harness against the contract and reports a number per
     criterion, needing integrate and running next to the test job. A merge
     job that is the only judge of its own merge is how a run closes green with
     the contract still unmet.
7. **Submit it:** ` + "`loods run submit <run> --file -`" + ` with the YAML on
   stdin. The graph is checked as a whole: a cycle, a missing ` + "`needs`" + ` or
   a job without a prompt is refused and nothing is started, so fix it and
   submit again.
8. **Watch:** ` + "`loods run status <run> --watch`" + ` prints the run whenever
   something moves and returns when it is over. Jobs report themselves; you only
   step in when one fails.
9. **React.** A failed job blocks everything that needed it. Decide: add a
   repaired job with ` + "`loods run extend <run> --file -`" + ` (it may depend on
   jobs that are already done), do the small fix yourself, or tell the user it
   is blocked and why. Use ` + "`extend`" + ` for work you only discover once a
   job reports back — that is the part of the plan you were not able to write up
   front.
10. **Close it.** When the run is over: ` + "`loods plan next \"…\"`" + ` and
   ` + "`loods plan note \"…\"`" + `, then tell the user in a few lines what was
   built, what failed, and what you left on the board.

A graph for a new project looks like this. It is a starting point, not a script:
rewrite the prompts for the actual goal, drop what does not apply, add what does.

` + "```yaml" + `
jobs:
  - id: repo
    role: git
    title: Create the repository and push it to GitHub
    exclusive: repo
    prompt: |
      Make this folder a git repository with a .gitignore and a README that
      names the project and its stack, commit it, then create a private GitHub
      repository with gh and push. Report the repository URL.
    steps:
      - git init with a .gitignore for this stack
      - Write the README with what it is and which stack
      - Commit everything
      - Create the private GitHub repository with gh
      - Push and report the URL
  - id: stories
    role: stories
    title: Write the user stories
    prompt: |
      Write docs/userstories.md: the user roles, and per role the user stories
      that cover the goal, each with acceptance criteria. Keep them small
      enough to become one ticket each.
    steps:
      - List the user roles
      - Write the stories of the first role
      - Write the stories of the other roles
      - Add acceptance criteria to every story
  - id: tickets
    role: tickets
    needs: [stories]
    title: Turn the user stories into tasks on the planboard
    prompt: |
      Read docs/userstories.md and add one task per story with
      'loods todo add "…"'. Report the numbers you added.
    steps:
      - Read docs/userstories.md
      - Add a task per story of the first role
      - Add a task per story of the other roles
      - Report the task numbers
  - id: scaffold
    role: scaffold
    needs: [repo]
    exclusive: repo
    title: Scaffold the application
    prompt: |
      Set up the project skeleton for the stack in the goal, with a working dev
      command and a README that says how to run it. Commit and push.
    steps:
      - Create the project skeleton for the stack
      - Get the dev command running
      - Write the run instructions in the README
      - Commit and push
` + "```" + `

Here repo and stories have nothing to do with each other and run together,
tickets comes out of the stories, and scaffold waits for repo because both
commit.

Extra instructions from the user, if any: $ARGUMENTS
`

// files are everything `loods claude install` writes into ~/.claude/skills.
var files = map[string]string{"wrapup": wrapupSkill, "todo": todoSkill, "orchestrate": orchestrateSkill}

// Names are the skills loods installs, in a stable order.
func Names() []string { return slices.Sorted(maps.Keys(files)) }
