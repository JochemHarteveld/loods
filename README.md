# loods

One board for every project under `~/Projects`: activity, git state, branches and
shortcuts to open them. Pick a project and you land on its own page: a planboard of its
tasks, a garage that runs its dev servers, and its git state. Tasks are numbered, so you
can tell Claude "do todo #4 of loods" and watch it work on the board. A whole project can be
handed to agents at once: one orchestrator plans the work, loods runs the jobs it hands out —
several at a time — and the Agents view shows what every session is doing. Hygiene and
Graveyard views clean up across all projects. Runs as a local server with the UI in a
chromeless app window.
Absorbs [graveyard](../graveyard) (archive / trash / undo).

```
make install        # builds UI + binary into ~/.local/bin/loods
loods               # start the server on 127.0.0.1:7777 and open the window
loods               # again while running: just opens another window
loods --no-open     # server only
loods --dev         # also accept terminal websockets from the vite dev server (:5173)
loods --json        # one-off scan as JSON (includes sizes), for scripts or Claude
loods new <name> "<what to build>"   # new project, planned and built by agents (see Runs)
loods run           # the runs of a project: submit a graph of jobs, follow it, report back
loods undo          # restore the last archived batch (shares ~/Archive/graveyard.log)
loods plan          # show / edit the plan of the project you are in (see Plans)
loods todo          # list, claim and finish numbered tasks, by you or an agent (see Tasks)
loods claude install  # /wrapup, /todo and /orchestrate skills + SessionStart hook for Claude Code
```

## Keys

Everywhere: `/` or `ctrl+k` filter · `R` rescan · `?` help · `y`/`n` answer a confirmation

Board, Hygiene, Graveyard, Agents: `1` Board · `2` Hygiene · `3` Graveyard · `4` Agents ·
`+` new project (planned and built by agents)

Board: `hjkl`/arrows move · `enter` open the project page · `space` details drawer ·
`L` its garage · `c` VS Code · `t` terminal · `o` folder · `g` remote · `r` run · `x` stop ·
`w` open web URL · `s` sort (activity / priority / name)

Project page: `esc` back to the board · `1` Planboard · `2` Garage · `3` Git

Board and project page: `n` next step · `N` notes · `a` add task · `m` status · `p` priority ·
`c`/`t`/`o`/`g` open · `r` run · `x` stop · `w` web URL

Planboard: `hjkl` move · `H`/`L` move the task a column left / right (or drag it) ·
`space` done ⇄ to do · `enter` rename · `del` delete · click a task for its menu ·
`A` assign it to an agent · `i` type into that agent's terminal · `X` free a task from an agent

Agents: click a session to show its terminal in the dock · `+` new project

Anywhere: `` ` `` show / hide the agent dock

Garage: `j`/`k` select · `i`/`enter` type into the terminal (`esc` leaves) · `r` restart ·
`x` stop (again: kill) · `u`/`U` flutter hot reload / restart · `w` open URL · `del` remove

Hygiene: `j`/`k` select · `enter` fix (asks first) · `space` details

Graveyard: `space` mark · `a` archive · `x` trash · `u` undo the last archive · `d` duplicates only ·
`s` sort by age / size / name · `R` measure again

## Plans

Every project gets a status (`idea` · `active` · `paused` · `shipped` · `dead`; none = inbox),
a priority (P1–P3), a next step, tasks, notes and a short log. Cards on the Board show the
status, priority, next step and task progress; the project page's **Planboard** tab is a kanban
of that project's tasks in three columns — to do, in progress, done — with the next step pinned
above the first column.

Every task carries a number (`#8`) that stays with it while it moves, is never reused, and
is what you point at from a conversation. `last_task_id` is the counter behind it.

They live in `~/.config/loods/plans.yaml`, separate from the hand-written config because
loods writes this one. Edits from the board, the CLI, Claude or your editor all go through
a file lock and show up everywhere within two seconds. Hand edits work; comments are not kept.

```yaml
projects:
  bierreel:
    status: active
    priority: 1
    next: Fix Play Store rejection: privacy policy URL
    last_task_id: 3
    tasks:
      - '#1 Update privacy policy page'
      - '#2 [~] Shoot new store screenshots'
      - '#3 [x] Resubmit build'
```

```
loods plan                       # show the plan of the project containing $PWD
loods plan next "…"              # set the next step
loods plan status paused         # idea | active | paused | shipped | dead | none
loods plan priority 1            # 0 = none
loods plan task "…"              # add a task (to do)
loods plan doing 2               # move task #2 to in progress; done / undo / drop <n|text>
loods plan note "…"              # add a log entry
loods plan -p group/project …    # another project
```

### Claude

- **Sessions** are read from Claude Code's transcripts (`~/.claude/projects`), read-only and
  incrementally. Cards show when Claude last worked on a project and on what (its session
  title); the details drawer lists recent sessions with prompts and active time. Sessions in a
  folder that has since moved are matched by folder name; sessions in a workspace folder
  above several repos are not attributed.
- **`loods claude install`** adds four things to `~/.claude` (undo with `loods claude uninstall`):
  - a `/wrapup` skill: Claude logs what the session did, sets the next step and moves or adds
    tasks via `loods plan`;
  - a `/todo` skill: Claude picks up a numbered task, claims it, works, and reports back
    (see [Tasks and agents](#tasks-and-agents));
  - an `/orchestrate` skill: Claude splits a goal into a graph of jobs and lets loods run them
    as parallel agents (see [Runs](#runs-a-whole-project-by-several-agents));
  - a `SessionStart` hook (`loods hook`): a new session in a project starts with its plan in
    context, so Claude knows where you left off.

  `settings.json` keeps its other keys and order; a backup is written next to it.

## Tasks and agents

Handing work to Claude is the same loop you would do by hand, with the board as the shared
state. Numbers are the handle: "do todo #4 of loods" is unambiguous because `#4` never
moves to another task.

```
loods todo                                 # open tasks of the project you are in
loods todo list --all                      # every project, highest priority first
loods todo list --all --json               # the same, for an agent to read
loods todo show loods#4                    # one task with its project's plan
loods todo claim loods#4                   # take it: in progress, and marked as yours
loods todo heartbeat loods#4               # still working on it
loods todo done loods#4 --note "…"         # finished; the note goes in the project log
loods todo release loods#4 --note "…"      # handed back: back to to do, claim dropped
loods todo add "…"                         # new task, prints its number
```

A ref is `group/project#4`, `#4` or `4`; a bare number means the project you are in (or
`-p group/project`). `--state todo|doing|done|any` widens the default (everything not done),
`--mine` narrows it to this agent.

**Claims** say who is working on what. They live in `~/.local/state/loods/claims.json`, not
in `plans.yaml`: a plan is durable, a claim is not. Each claim carries the agent, its session
id, the branch, and a heartbeat. The planboard shows a claimed task with `✻ claude working ·
2m`; after ten minutes without a heartbeat it reads `stalled`, which is how you spot an agent
that died. `X` (or *free* on the card) drops the claim — it asks first, and warns you when the
agent is in fact still reporting in. Claiming a task another agent is actively on is refused
unless you pass `--steal`; a stale claim can be taken over without asking. A project that
leaves the board takes its claims with it.

Two agents can work two tasks of one project at once: claims are per task, and every write
goes through the same file lock as the board.

### Handing a task over from the board

Clicking a task on the planboard opens its menu: **assign to agent** (`A`), move it to another
column, rename, delete, and free it from an agent. Assigning starts a Claude Code session in a
real terminal in the project itself — `claude "Pick up todo loods#4 …"`, with the loop of the
`/todo` skill spelled out in the prompt — and moves the task to in progress with a line in the
project log. loods does not claim the task for the agent: a claim is the session's own statement,
so the card reads `✻ agent starting` until the agent claims it, and `✻ claude working · 2m` after.

Those terminals live in the **agent dock** along the bottom of the window, with one tab per
agent, so they keep running (and stay one click away) while you walk the board. `` ` `` shows and
hides it, a tab switches to that agent, `i` types into the selected one and `esc` leaves it, and
the dock's *task* button jumps to the planboard of the task an agent is on. Several agents run at
once — one per task, over any number of projects. An agent is an ordinary loods process, so it
has the same output backlog, stop (Ctrl+C, then SIGTERM, then SIGKILL) and *run again* as a dev
server, and quitting loods stops them with everything else.

### Claude

`loods claude install` adds a `/todo` skill that tells Claude the whole loop: list, claim
before starting, heartbeat while working, finish with a note or hand it back. The SessionStart
hook prints the project's open tasks with their numbers and any claim, so a fresh session
already knows what `#4` means.

No MCP server is needed for Claude Code: it runs `loods todo` over Bash. An MCP server would
only add a transport for clients that cannot run commands (Claude Desktop, claude.ai).

## Runs: a whole project by several agents

One task is one agent. A **run** is one goal split into **jobs**, each with its own agent, and
loods decides what may run at the same time. `loods new` is the short way in:

```
loods new huizenzoeker "a house-hunting site with svelte and express"
loods new -g blauweschuit barsys2 "…"      # in a group folder
loods new --no-agent …                     # only the project and its plan
```

The folder, a git repository and a plan exist within a second, so the project is on the board
before anything is built. Then loods starts an **orchestrator**: a Claude session in that
project whose job is to plan, not to build. It writes a graph of jobs and hands it to loods:

```yaml
jobs:
  - id: repo
    role: git
    title: Create the repository and push it to GitHub
    exclusive: repo
    prompt: |
      .gitignore, a README, commit, then `gh repo create --private` and push.
  - id: stories
    role: stories
    title: Write the user stories
    prompt: |
      Write docs/userstories.md …
  - id: tickets
    role: tickets
    needs: [stories]
    task: 2
    title: Turn the user stories into tasks on the planboard
    prompt: |
      One `loods todo add` per story …
```

loods starts a job as soon as everything it `needs` is done, at most `--max-parallel` sessions
at a time (3), and never two jobs holding the same `exclusive` lock — `repo` for anything that
commits, because two sessions in one working tree race over the index. Locks are per project,
so two runs on one repo take turns while runs on different projects never wait for each other.
Here `repo` and `stories` start together, `tickets` follows `stories`, and a job with
`task: 2` claims that planboard task so the card shows it as live work.

Each job is an ordinary loods process, so it has the same terminal, output backlog and stop as
a dev server, and it reports back itself:

```
loods run done <run>:<job> --note "what you did"
loods run fail <run>:<job> --note "why"
```

Nothing that depends on a job starts until it reports. A job whose session ends without
reporting is a failure — the run has no way to know what it built — and everything that needed
it reads `blocked` instead of waiting forever. The orchestrator decides what to do with that:
repair it with `loods run extend`, fix it itself, or tell you it is stuck. `extend` is also how
it adds work it only discovers once a job reports back; a graph with a cycle, a missing `needs`
or a job without a prompt is refused as a whole, so nothing starts half-planned.

The **Agents** view (`4`) is one card per run: the orchestrator on top, then the jobs in waves —
everything in one column may run at the same time, and each column waits for the one before it.
Every job line says what it is doing, or why it is not: `waits for stories`,
`waits for a free repo (repo has it)`, or the note its agent left. Clicking a line brings that
session's terminal up in the dock, where the tabs of a run sit together with the orchestrator
first. *cancel run* stops handing out work and stops the sessions it started.

```
loods run                          # the runs of this project (--all for every project)
loods run new "<goal>"             # a run with no jobs yet; prints its id
loods run submit <run> --file -    # give it its graph (YAML on stdin or a file)
loods run extend <run> --file -    # add jobs to a run that is already going
loods run status <run> --watch     # print it whenever something moves
loods run done|fail <run>:<job> --note "…"
loods run cancel <run>
```

Runs live in `~/.local/state/loods/runs.json`, next to procs.json and claims.json: a plan is
durable, a run is as alive as the sessions in it. Finished runs stay a week as history. The CLI
only writes state — the server starts the sessions, so without a running loods the commands say
so instead of silently doing nothing. A job that was running when loods died is failed on the
next start, so a run never waits for a session that is gone.

`loods claude install` adds an `/orchestrate` skill that spells this loop out, and the sessions
loods starts carry it in their prompt, so they report back with or without the skill installed.

## Hygiene

Findings per project, worst first. Fixes always show what they will do and ask first.

| Finding | Fix |
|---|---|
| `.env` (or `.env.production`, …) committed | hint: `git rm --cached`, ignore it, rotate the secrets |
| `.env` on disk and not ignored | append its exact path to `.gitignore` |
| uncommitted changes older than 7 days, commits unpushed for 3+ days, no remote, behind upstream | hints |
| branches merged into the default branch, or whose remote branch was deleted | delete them (clean worktrees go too) |
| worktrees whose folder is gone | `git worktree prune` |
| failing CI on the checked-out branch, PRs with failing checks | link |

Branch cleanup is undoable: every deleted branch is logged with its commit in
`~/.local/state/loods/git-cleanup.log`, and "Undo cleanup" in the details drawer recreates the
branches and their worktrees. Nothing is forced: worktrees with uncommitted changes are kept.
Single branches can be deleted from the drawer too (✕ on hover).

### GitHub

With `gh` installed and logged in, loods asks GitHub every 5 minutes (and on `R`) for open PRs
with their checks and reviews, the open issue count and the latest CI run on the checked-out
branch. Cards show CI (✓ ✗ ●) and PR count; the drawer lists the PRs and tags branches that
have one. Read-only; turn it off with `github: false` in `config.yaml`.

## Graveyard

Everything under the root, including plain folders and archive files, with real sizes
(`node_modules` included, so measuring takes a few seconds; cached for 2 minutes). Flags show
possible duplicates, uncommitted or unpushed work and plan status (`dead` projects are struck
through). Archive moves into `~/Archive/<date>/<path>` (same disk only, never copies) and is
undoable; trash uses `gio trash`. Both share `~/Archive/graveyard.log` with `loods undo` and the
old graveyard tool. Projects with a running process are refused.

## Garage

Every project page has a garage: the commands loods found for that project, its running
processes with their terminals, and the stacks of its group. Commands are detected per project
root and its direct subfolders (`app/`, `web/`, …):

| Found | Command |
|---|---|
| executable `dev-stack.sh` / `dev.sh` | `./dev-stack.sh` |
| `package.json` with `dev` (else `start`) | `npm`/`pnpm`/`yarn`/`bun run dev`, by lockfile |
| `pubspec.yaml` + `lib/main.dart` | `flutter run` (`u`/`U` send `r`/`R`) |
| `go.mod` + `main.go` | `go run .` |
| compose file | `docker compose up` |
| `platformio.ini` | `pio run -t upload` |
| `manage.py` | `python3 manage.py runserver` |
| `Makefile` with `dev:`/`run:`/`serve:` | `make dev` |

An executable `dev-stack.sh` in a group folder (like `~/Projects/transpaclean`) becomes a stack.

- Each process runs in a real terminal (pty) under `bash -lc`, so PATH, colours, prompts and
  flutter's single-key commands work like in a terminal. The UI shows it with xterm.js; output
  is kept (512 KB) so you can attach any time.
- Stop sends Ctrl+C (docker compose stops its containers), then SIGTERM after 10 s, then
  SIGKILL. Stopping twice kills immediately.
- Ports come from `/proc` (listening sockets of the process tree), URLs from the output.
- Quitting loods (Ctrl+C, logout) stops everything it started. If loods dies instead, the next
  start finds the survivors (`~/.local/state/loods/procs.json`) and lists them as orphans you
  can stop or restart.

### Config

`~/.config/loods/config.yaml` is created with examples on first run and read on every scan
(save, then `R`):

```yaml
projects:
  transpaclean/transpaclean-flutterapp:
    default: app flutter                 # what r starts without asking
    commands:
      app flutter: { run: flutter run -d linux }    # override a detected command
      dashboard dev: { hide: true }                 # hide one
      web: { run: npm run dev, dir: web, url: http://localhost:5173 }   # add one
stacks:
  transpaclean:                          # one script …
    dir: transpaclean
    run: ./dev-stack.sh --no-fw
  site:                                  # … or several project commands
    commands:
      - blauweschuit/de-website-en-backend:compose
      - blauweschuit/de-website-en-backend:dev
```

## How it works

- Runs are scheduled in the same two-second pass that picks up plan edits: read what the agents
  wrote, turn ended sessions into finished jobs, then start whatever may run now.
- Rescans every 30 s (or `R`) and pushes a snapshot over SSE; the UI only re-renders when something changed.
  Plan edits, task claims and new Claude transcript lines are picked up between scans, so a
  `loods todo claim` in a terminal shows up on the board within two seconds.
- Board scans skip `node_modules`, `build`, … entirely, so a full scan takes about a second.
- Ahead/behind counts come from the last `git fetch`; loods never fetches. The only network use is
  the read-only `gh` queries above.
- Branch details compare every local branch with the remote's default branch (`origin/HEAD`).
- The server binds to 127.0.0.1, rejects requests for any other `Host` (DNS rebinding), and
  requires an `X-Loods` header on POSTs, so other websites cannot make it launch things.
  The client only ever sends project, command and stack names, never paths or command lines;
  terminal websockets must come from the same origin.

## Development

```
go run . --no-open --dev   # API on :7777
make dev             # UI with hot reload on :5173, proxies /api
make test
```

## Roadmap

1. ~~Board: cards, git state, branches, open in VS Code / terminal, live refresh~~
2. ~~Garage: detected dev commands, process manager with terminals, ports, RAM, stacks~~
3. ~~Plans: status / next step / priority / tasks, kanban, Claude sessions, /wrapup + SessionStart hook~~
4. ~~Hygiene: branch & worktree cleanup with undo, `gh` PRs and CI, warnings, graveyard view~~
5. ~~Project pages: per-project planboard, garage and git; numbered tasks an agent can claim~~
6. ~~Runs: `loods new`, an orchestrator that plans a graph of jobs, a scheduler that runs them
   in parallel, and the Agents view~~
7. Extras: activity feed, MCP server for clients without a shell, weekly digest, adb / serial devices
