# loods

One board for every project under `~/Projects`: activity, git state, branches and
shortcuts to open them. Pick a project and you land on its own page: a planboard of its
tasks, a garage that runs its dev servers, and its git state. Tasks are numbered, so you
can tell Claude "do todo #4 of loods" and watch it work on the board. Hygiene and
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
loods undo          # restore the last archived batch (shares ~/Archive/graveyard.log)
loods plan          # show / edit the plan of the project you are in (see Plans)
loods todo          # list, claim and finish numbered tasks, by you or an agent (see Tasks)
loods claude install  # /wrapup and /todo skills + SessionStart hook for Claude Code
```

## Keys

Everywhere: `/` or `ctrl+k` filter · `R` rescan · `?` help · `y`/`n` answer a confirmation

Board, Hygiene, Graveyard: `1` Board · `2` Hygiene · `3` Graveyard

Board: `hjkl`/arrows move · `enter` open the project page · `space` details drawer ·
`L` its garage · `c` VS Code · `t` terminal · `o` folder · `g` remote · `r` run · `x` stop ·
`w` open web URL · `s` sort (activity / priority / name)

Project page: `esc` back to the board · `1` Planboard · `2` Garage · `3` Git

Board and project page: `n` next step · `N` notes · `a` add task · `m` status · `p` priority ·
`c`/`t`/`o`/`g` open · `r` run · `x` stop · `w` web URL

Planboard: `hjkl` move · `H`/`L` move the task a column left / right (or drag it) ·
`space` done ⇄ to do · `enter` rename · `del` delete · `X` free a task from an agent

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
- **`loods claude install`** adds two things to `~/.claude` (undo with `loods claude uninstall`):
  - a `/wrapup` skill: Claude logs what the session did, sets the next step and moves or adds
    tasks via `loods plan`;
  - a `/todo` skill: Claude picks up a numbered task, claims it, works, and reports back
    (see [Tasks and agents](#tasks-and-agents));
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

### Claude

`loods claude install` adds a `/todo` skill that tells Claude the whole loop: list, claim
before starting, heartbeat while working, finish with a note or hand it back. The SessionStart
hook prints the project's open tasks with their numbers and any claim, so a fresh session
already knows what `#4` means.

No MCP server is needed for Claude Code: it runs `loods todo` over Bash. An MCP server would
only add a transport for clients that cannot run commands (Claude Desktop, claude.ai).

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
6. Extras: activity feed, MCP server for clients without a shell, weekly digest, adb / serial devices
