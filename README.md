# loods

One board for every project under `~/Projects`: activity, git state, branches, and
shortcuts to open them. Runs as a local server with the UI in a chromeless app window.
Absorbs [graveyard](../graveyard) (archive / trash / undo).

```
make install        # builds UI + binary into ~/.local/bin/loods
loods               # start the server on 127.0.0.1:7777 and open the window
loods               # again while running: just opens another window
loods --no-open     # server only
loods --json        # one-off scan as JSON (includes sizes), for scripts or Claude
loods undo          # restore the last archived batch (shares ~/Archive/graveyard.log)
```

## Keys

`hjkl`/arrows move · `enter` details & branches · `c` VS Code · `t` terminal · `o` folder ·
`g` remote · `/` or `ctrl+k` filter · `s` sort · `R` rescan · `?` help

## How it works

- Rescans every 30 s (or `R`) and pushes a snapshot over SSE; the UI only re-renders when something changed.
- Board scans skip `node_modules`, `build`, … entirely, so a full scan takes about a second.
- Ahead/behind counts come from the last `git fetch`; loods never touches the network.
- Branch details compare every local branch with the remote's default branch (`origin/HEAD`).
- The server binds to 127.0.0.1, rejects requests for any other `Host` (DNS rebinding), and
  requires an `X-Loods` header on POSTs, so other websites cannot make it launch things.
  The client only ever sends project ids, never paths or commands.

## Development

```
go run . --no-open   # API on :7777
make dev             # UI with hot reload on :5173, proxies /api
make test
```

## Roadmap

1. ~~Board: cards, git state, branches, open in VS Code / terminal, live refresh~~
2. Garage: detected dev commands, process manager with logs, ports, RAM, stacks (`dev-stack.sh`)
3. Plans: status / next step / priority in `~/.config/loods/projects.yaml`, kanban, Claude wrap-up hook
4. Hygiene: branch & worktree cleanup, `gh` PRs and CI, warnings, graveyard view
5. Extras: activity feed, Claude session time, ask-Claude, weekly digest, adb / serial devices
