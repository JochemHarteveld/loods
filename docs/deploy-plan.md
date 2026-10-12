# Deploys on loods.site

A project on the board gets a URL: `<project>.loods.site`, live within seconds of
`loods deploy`, with no DNS record, no certificate and no tunnel change per project.
One wildcard record exists for the whole domain; everything after that is routing
inside loods.

## Subdomain, not path

`projectnaam.loods.site`, not `loods.site/projectnaam`.

| | subdomain | path prefix |
|---|---|---|
| DNS work per project | none (`*.loods.site` covers all) | none |
| App config per project | none | `base` / `paths.base` / router basename / absolute asset URLs, per framework |
| Origin isolation | separate origins: cookies, localStorage, service workers can't collide | one origin: every app shares cookie jar and SW scope |
| Access policy per project | per hostname, trivial | needs path rules, awkward |
| Proxy work | match `Host` | strip prefix, rewrite redirects and `Location` headers |

The path variant looks cheaper and is not: every app has to be rebuilt knowing where it
is mounted, and the first app that ships an absolute `/assets/...` URL breaks. Subdomains
cost one wildcard record, once.

`loods.site` itself stays free for a landing page that lists the live deploys, generated
from board state.

Caveat: Cloudflare's Universal SSL covers the apex and one wildcard level. `app.loods.site`
is covered, `api.app.loods.site` is not (that needs an advanced certificate). Keep deploy
hostnames one level deep.

## The chain

```
browser
  → Cloudflare edge         *.loods.site — TLS, Access, caching
  → Cloudflare Tunnel       outbound-only from the origin; no port is opened
  → loods edge   :8080      reverse proxy, routes on Host
  → the project             static dir, a supervised process, or a compose service
```

Four pieces, and only the last two know about individual projects:

- **DNS**: one proxied record, `*.loods.site CNAME <tunnel-uuid>.cfargotunnel.com`, plus the
  same for the apex. Written once.
- **Tunnel**: one ingress rule, `hostname: "*.loods.site"` → `http://127.0.0.1:8080`, plus a
  catch-all that returns 404. Written once. (`cloudflared tunnel ingress rule
  https://foo.loods.site` checks it.)
- **loods edge**: the routing table. One entry per deploy, reloaded on change — this is the
  only thing a `loods deploy` touches.
- **the project**: built artifacts, or a process under `internal/proc`.

### Why a proxy of our own

`cloudflared` could map hostnames to local ports directly, but then every deploy is an edit
to the tunnel config and a reload of the connector. A single wildcard rule into one proxy
keeps the tunnel static, and puts the per-project routing in the process that already holds
the state.

`internal/edge` is a `httputil.ReverseProxy` plus a `http.FileServer`, maybe 200 lines: look
up the `Host`, serve a directory (with SPA fallback) or proxy to a port, 502 with a readable
page when the backend is down. It binds `127.0.0.1` only, so cloudflared is the one way in.
Caddy is a drop-in alternative if we ever want on-demand TLS without Cloudflare in front;
behind the tunnel TLS is already terminated at the edge, so there is nothing to gain yet.

Carving it out as its own subcommand (`loods edge --routes …`) is what makes the VPS step
later a copy of one binary instead of a rewrite.

## What a deploy is

Three kinds, covering what is on the board today:

| kind | build | serve |
|---|---|---|
| `static` | `npm run build` (or detected) | `internal/edge` serves the output dir, SPA fallback on 404 |
| `proc` | optional | a long-running command on an allocated port, supervised by `internal/proc` |
| `compose` | `docker compose build` | `docker compose up -d`; route to the published port of a named service |

`proc` is nearly free: `internal/proc` already gives a real terminal, a 512 KB output
backlog, Ctrl+C → SIGTERM → SIGKILL stopping, and orphan recovery after a loods crash. A
deploy is a proc with a stable port, a route, and `restart: always` semantics.

Databases are not per deploy. The compose stacks already running (mysql, postgres, valkey)
are the origin's databases; a deploy gets its own schema in them, not its own server.

## Config and state

`~/.config/loods/deploys.yaml` — hand-written, read on every scan, like `config.yaml`:

```yaml
site:
  domain: loods.site
  tunnel: loods          # cloudflared tunnel name
  edge: 127.0.0.1:8080
deploys:
  bierreel:
    kind: static
    build: npm run build
    dir: build
    public: true         # default is private, behind Access
  blauweschuit/de-website-en-backend:
    sub: blauweschuit    # default: the project folder name, slugified
    kind: compose
    service: web
  transpaclean/TranspaBridge:
    sub: bridge
    kind: proc
    build: bun run build
    run: bun run start   # $PORT is set
    env:
      NODE_ENV: production
```

`~/.local/state/loods/deploys.json` — written by loods, next to `procs.json`, `claims.json`
and `runs.json`: allocated port, pid, commit deployed, last deploy time and outcome, health.
Same `internal/store` lock and the same two-second pass that picks up plan edits, so the
board, the CLI and a hand edit all agree within two seconds.

Ports come from a dedicated range (8900–8999), assigned on first deploy and kept in
`deploys.json`, so a deploy never collides with a garage dev server on 5173.

Secrets stay out of the repo: `env` in `deploys.yaml` (mode 0600), or a path to an env file
outside the working tree. Hygiene already flags a committed `.env`.

## CLI

```
loods deploy                       # build and publish the project containing $PWD
loods deploy -p group/project
loods deploy list [--all] [--json]
loods deploy log [--follow]        # the build and the process output
loods deploy stop | rm
loods deploy public on|off         # Access bypass for this hostname
loods deploy open                  # https://<sub>.loods.site
loods edge --routes ~/.local/state/loods/deploys.json   # the proxy on its own
```

On the board: a card shows its deploy URL and state (live · stale · failed · stopped),
`D` deploys, `w` opens the public URL instead of the dev URL when there is one. The project
page gets a **Deploy** tab: the hostname, what is published (kind, commit, when), the build
log, and public/private.

Agents reach it the same way they reach everything else, over Bash: `loods deploy` is a
sensible last job in a run, and `loods deploy list --json` tells an orchestrator what is live.

## Access: private by default, public per project

One Access application for `*.loods.site` with an Allow policy for the owner's email; one
more specific application per public hostname with a Bypass policy for everyone. The more
specific application wins, so `public: true` means loods creates (or deletes) a one-hostname
bypass app over the API — no manual work, no DNS change.

```
POST /accounts/$ACCOUNT_ID/access/apps      # self_hosted, domain: demo.loods.site
POST /accounts/$ACCOUNT_ID/access/policies  # decision: bypass, include: [{everyone:{}}]
```

Two things to check in the dashboard before leaning on this:

- that a wildcard application domain is allowed on the current plan. If it is not, the
  fallback is one Access app per deploy, created by the same code path — still zero manual
  work, just one API call more per project.
- wildcard applications do not get preemptive auth cookies, so a deploy that fetches from
  another deploy's hostname authenticates per hostname. Irrelevant for single-origin apps.

A scoped API token (`Access: Apps and Policies Write`, `DNS:Edit` for the zone) lives in
`~/.config/loods/credentials.yaml`, 0600, never in the repo.

## Phases

| # | What | Done when |
|---|---|---|
| 0 | Nameservers at Namecheap → Cloudflare. Create tunnel `loods`, wildcard CNAME, ingress rule, `cloudflared` as a user service | `curl https://anything.loods.site` reaches a local hello-world |
| 1 | `internal/edge`: Host routing, static dir + proxy + 502 page, reload on routes change. `loods edge` subcommand | two hardcoded hostnames serve two different things |
| 2 | ~~`deploys.yaml`, `deploys.json`, `internal/deploy`: detect kind, build, assign a port, start, route, record. `loods deploy` + `list`/`log`/`stop`/`rm`~~ | **done** — static and proc proved live, failure and stop paths too |
| 3 | Access: wildcard allow app, per-hostname bypass, `loods deploy public` | a private deploy asks for login, a public one does not |
| 4 | Board and project page: Deploy tab, card state, `D`, build log in the dock | a deploy can be done and watched without the CLI |
| 5 | Apex landing page generated from board state | `loods.site` lists what is live |
| 6 | VPS: `loods edge` + `cloudflared` on a small box, `loods deploy --remote` ships the artifact or image over ssh and flips the route | laptop closed, deploys still up |
| 7 | Extras: per-branch previews (`pr-12.loods.site`), deploy on push via a git hook, health checks that mark a deploy `failed` on the board | — |

Phases 0–2 are the useful minimum; 3 and 4 are what make it pleasant; 6 is the one that
changes the answer to "is it still up".

## Known costs

- **The origin is a laptop.** Until phase 6, a closed lid means every deploy is down, and a
  shared public link is the thing that breaks. The architecture is chosen so that moving the
  origin is one config change and a copy of one binary, not a redesign.
- **A deploy is not a build server.** `loods deploy` builds on the machine it runs on, with
  whatever toolchain is installed. Reproducibility is the `compose` kind's job.
- **No rollback in the first version.** `deploys.json` records the commit deployed, so the
  manual rollback is `git checkout <sha> && loods deploy`. Keeping the previous static output
  dir around would make it one command; worth doing once there is something to roll back.

## Phase 0, as built

Everything that does not need the zone to exist is in place:

| File | What it is |
|---|---|
| `~/.local/share/loods/edge/hello.py` | the placeholder origin on `127.0.0.1:8080`: echoes the `Host` it was asked for. Phase 1 replaces it with `loods edge` |
| `~/.config/systemd/user/loods-edge.service` | runs it, enabled, active now |
| `~/.cloudflared/config.yml` | the tunnel: `*.loods.site` and the apex to `127.0.0.1:8080`, a 404 catch-all, and nothing per project. `__TUNNEL_UUID__` is filled in by the script |
| `~/.config/systemd/user/cloudflared-loods.service` | the connector, after `loods-edge.service` |
| `scripts/phase0-cloudflare.sh` | login, create the tunnel, fill in the config, create the two DNS records, start the connector, verify `https://phase0.loods.site`. Idempotent |

The existing `cloudflared-transpaclean.service` is a separate, remotely-managed
tunnel (its config lives in the dashboard) and is left alone — two connectors
can run side by side.

Two steps needed a browser and were the only manual work in the whole design:
adding `loods.site` to the Cloudflare account, and pointing Namecheap's
nameservers at the two Cloudflare gave (`maya` / `robert`). Both are done; no
later phase touches DNS again.

### Done, 2026-10-09

Tunnel `loods` is `87cce5e4-43bd-496d-a950-182444a2c761`, four connections into
ams. `*.loods.site` and `loods.site` are proxied CNAMEs to it, and Universal SSL
issued a certificate with `DNS:*.loods.site, DNS:loods.site`. A hostname that was
never configured anywhere answers:

```
$ curl https://zomaar-iets-nieuws.loods.site/
loods edge is up
Host: zomaar-iets-nieuws.loods.site
```

That is the whole point of the design, working: a new project needs no DNS
record, no certificate and no tunnel change — only a route inside loods.

Two things worth remembering:

- **The metrics port.** Another `cloudflared` on this machine (pid 160552, its
  own token, unrelated to `cloudflared-transpaclean.service`) holds the default
  `127.0.0.1:20241`, so this tunnel uses `20242`. A third connector will need a
  third port.
- **IPv6 is broken on this network**, not in this setup: `curl -6 https://cloudflare.com`
  fails too. Since the records answer AAAA as well as A, a local test picks the
  v6 address about half the time and fails after 30 ms. `curl -4` always works,
  and visitors elsewhere are unaffected. The local fix, if it gets annoying, is
  to prefer IPv4 in `/etc/gai.conf`:
  `precedence ::ffff:0:0/96  100`.

## Phase 1, as built

`internal/edge` is the origin: a routes file, a `Host` lookup, and three kinds
of answer — a built directory, a reverse proxy, or a page explaining why there
is neither. `loods edge` runs it on its own, which is what phase 6 needs.

The routes file is `~/.local/state/loods/deploys.json`, re-read every two
seconds the same way the board picks up plan edits, so publishing a project is
a file write and never a restart. A file that cannot be parsed keeps the last
good table: half a deploy should not take the other deploys down.

`loods-edge.service` now runs `loods edge` instead of the phase-0 placeholder.

Proved end to end over `*.loods.site`, with four routes at once:

| Hostname | Route | Answer |
|---|---|---|
| `static-demo` | a built directory, `spa: true` | the page, its assets, and `/diepe/client/route` falling back to `index.html` |
| `board` | proxy to `127.0.0.1:7777` | `forbidden host` — the board's own DNS-rebinding guard, which is the proof the visitor's `Host` was forwarded |
| `dood` | proxy to a dead port | the "is not answering" page, naming the upstream |
| `kapot` | `status: failed` | the "is not running" page, with the build error |
| `typo` | no route | the "no deploy here" page, listing what does answer |

Two things learned, both now in the code:

- **Cloudflare replaces an origin 502 or 504 with a generic page** (passing them
  through is an Enterprise feature), so a deploy whose upstream is down answers
  **503**. The detail the page exists to show survives; a 502 would have been
  swapped for 16 bytes of plain text.
- **The SPA fallback only applies to navigations**, read from `Sec-Fetch-Mode`
  (or `Accept`). A missing `/assets/app.js` must 404; handing it `index.html`
  turns a clear error into a browser parse failure three steps away from the
  cause.

## Phase 2, as built

`internal/deploy` is the two halves of publishing: `deploys.yaml` (yours — what a
project publishes, how it is built) and `deploys.json` (the board's — hostname,
kind, port, commit, status). The second file is the routes file the edge already
reads, so there is no third thing to keep in step.

The server does the publishing, for the same reason it schedules runs: the build
and the process it may become have to outlive the command and belong to the dock.
`loods deploy` posts to `/api/deploys/start`; `tickDeploys` runs in the same
two-second pass as the runs scheduler and turns what the process did into what the
edge serves.

A deploy is one `proc.Spec`, which is where most of the work went away: terminal,
512 KB backlog, Ctrl+C → TERM → KILL, orphan recovery after a crash, and a dock tab
all came for free. A `proc` deploy is `build && exec run` in one process, so a build
that fails never becomes a server.

Proved over the real domain, with a project created for it:

```
$ loods deploy
fase2.loods.site  building
  building (static). Follow it: loods deploy log -p fase2

$ curl https://fase2.loods.site/
deployed by loods                 # and /app.js, and /client/route via the fallback

$ # break the build, deploy again
fase2.loods.site  static  failed (serving the last build)  the build exited 3
$ curl https://fase2.loods.site/   → 200, still the last good build

$ loods deploy stop  → 503 "Deploy is not running"
$ loods deploy rm    → 404 "No deploy here"

$ # kind: proc
fase2.loods.site  proc  live  -> 127.0.0.1:8900
$ curl https://fase2.loods.site/
proc deploy on port 8900 · Host: fase2.loods.site
```

The hostname the app sees is the visitor's, which is what makes its own redirects
and absolute URLs come out right.

One thing to know: `loods <subcommand>` reads the root from `$LOODS_ROOT` or
`~/Projects` and ignores a positional root, which the serving form accepts. It cost
a confused minute during testing. Worth tidying when something else touches the flag
parsing.

Still open before this is pleasant: Access (phase 3) decides who may look, and the
board (phase 4) is where a deploy should be started and watched. Until phase 6 the
origin is a laptop, so a shared link is only up while it is.
