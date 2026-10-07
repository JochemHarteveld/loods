# loods

One board for every project under `~/Projects`: activity, git state, branches,
shortcuts to open them, a plan per project (status, next step, tasks) with a kanban,
and a Garage that runs their dev servers. Runs as a local
server with the UI in a chromeless app window.
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
loods claude install  # /wrapup skill + SessionStart hook for Claude Code
```

## Keys

Everywhere: `1` Board · `2` Plans · `3` Garage · `/` or `ctrl+k` filter · `R` rescan · `?` help

Board: `hjkl`/arrows move · `enter` details, plan & branches · `c` VS Code · `t` terminal ·
`o` folder · `g` remote · `r` run · `x` stop · `L` logs · `w` open web URL ·
`s` sort (activity / priority / name)

Board and Plans: `n` next step · `a` add task · `N` notes · `m` status · `p` priority

Plans: `hjkl` move · `H`/`L` move the card a column left / right (or drag it)

Garage: `j`/`k` select · `i`/`enter` type into the terminal (`esc` leaves) · `r` restart ·
`x` stop (again: kill) · `u`/`U` flutter hot reload / restart · `w` open URL · `del` remove

## Plans

Every project gets a status (`idea` · `active` · `paused` · `shipped` · `dead`; none = inbox),
a priority (P1–P3), a next step, tasks, notes and a short log. The Plans tab is a kanban by
status; cards on the Board show the status, priority, next step and task progress.

They live in `~/.config/loods/plans.yaml`, separate from the hand-written config because
loods writes this one. Edits from the board, the CLI, Claude or your editor all go through
a file lock and show up everywhere within two seconds. Hand edits work; comments are not kept.

```yaml
projects:
  bierreel:
    status: active
    priority: 1
    next: Fix Play Store rejection: privacy policy URL
    tasks:
      - Update privacy policy page
      - '[x] Resubmit build'
```

```
loods plan                       # show the plan of the project containing $PWD
loods plan next "…"              # set the next step
loods plan status paused         # idea | active | paused | shipped | dead | none
loods plan priority 1            # 0 = none
loods plan task "…"              # add a task;  done / undo / drop <n|text>
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
  - a `/wrapup` skill: Claude logs what the session did, sets the next step and ticks or adds
    tasks via `loods plan`;
  - a `SessionStart` hook (`loods hook`): a new session in a project starts with its plan in
    context, so Claude knows where you left off.

  `settings.json` keeps its other keys and order; a backup is written next to it.

## Garage

Commands are detected per project root and its direct subfolders (`app/`, `web/`, …):

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
  Plan edits and new Claude transcript lines are picked up between scans.
- Board scans skip `node_modules`, `build`, … entirely, so a full scan takes about a second.
- Ahead/behind counts come from the last `git fetch`; loods never touches the network.
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
4. Hygiene: branch & worktree cleanup, `gh` PRs and CI, warnings, graveyard view
5. Extras: activity feed, ask-Claude, weekly digest, adb / serial devices
