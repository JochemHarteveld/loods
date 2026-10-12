#!/usr/bin/env bash
# Phase 0 of docs/deploy-plan.md: one tunnel, one wildcard record, and nothing
# per project ever again.
#
# Prerequisite (in a browser, once): loods.site added to the Cloudflare account.
# The nameserver switch at Namecheap may still be propagating; everything below
# works before it finishes.
#
# Idempotent: re-running it skips what already exists.
set -euo pipefail

DOMAIN=loods.site
TUNNEL=loods
CF_DIR="$HOME/.cloudflared"
CONFIG="$CF_DIR/config.yml"
CLOUDFLARED="$HOME/.local/bin/cloudflared"

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }

# 1. A certificate for the account, so cloudflared may act on the zone.
if [[ ! -f "$CF_DIR/cert.pem" ]]; then
  say "Logging in to Cloudflare (a browser opens; pick $DOMAIN)"
  "$CLOUDFLARED" tunnel login
else
  say "cert.pem present, skipping login"
fi

# 2. The tunnel. Its UUID is the only identifier the DNS records need.
if "$CLOUDFLARED" tunnel list --output json | grep -q "\"name\":\"$TUNNEL\""; then
  say "Tunnel '$TUNNEL' exists"
else
  say "Creating tunnel '$TUNNEL'"
  "$CLOUDFLARED" tunnel create "$TUNNEL"
fi

UUID=$("$CLOUDFLARED" tunnel list --output json \
  | python3 -c "import json,sys;print(next(t['id'] for t in json.load(sys.stdin) if t['name']=='$TUNNEL'))")
say "Tunnel UUID: $UUID"

# 3. Point the config at it.
sed -i "s/__TUNNEL_UUID__/$UUID/g" "$CONFIG"
grep -q "$UUID" "$CONFIG" || { echo "config.yml does not reference the tunnel" >&2; exit 1; }
"$CLOUDFLARED" --config "$CONFIG" tunnel ingress validate

# 4. The two DNS records. Proxied CNAMEs to <uuid>.cfargotunnel.com; wildcard
#    records can be proxied on every plan. If the CLI refuses the wildcard,
#    add it by hand once (see the message below) — it is the only record this
#    setup will ever need.
for host in "*.$DOMAIN" "$DOMAIN"; do
  say "Routing $host to the tunnel"
  if "$CLOUDFLARED" tunnel route dns --overwrite-dns "$TUNNEL" "$host"; then
    :
  else
    cat <<MSG

  The CLI would not create "$host". Add it in the dashboard (DNS > Records):

      Type: CNAME   Name: ${host%%.$DOMAIN}   Target: $UUID.cfargotunnel.com
      Proxy status: Proxied (orange cloud)

MSG
  fi
done

# 5. Run it, and keep running it after a reboot or a logout.
say "Starting the connector"
systemctl --user daemon-reload
systemctl --user enable --now cloudflared-loods.service
loginctl enable-linger "$USER" >/dev/null 2>&1 || true
sleep 3
systemctl --user --no-pager --lines=10 status cloudflared-loods.service || true

# 6. Prove the chain, once DNS resolves.
say "Checking https://phase0.$DOMAIN"
if curl -fsS --max-time 15 "https://phase0.$DOMAIN/" | grep -q "loods edge is up"; then
  printf '\n\033[32mPhase 0 done: the edge, the tunnel and the origin are one chain.\033[0m\n'
else
  cat <<MSG

Not through yet. Usually the nameservers: check what the world sees with

    dig +short NS $DOMAIN @1.1.1.1

If that still shows registrar-servers.com, Namecheap has not handed the zone
over yet (minutes to a few hours). The tunnel itself is fine as long as

    systemctl --user is-active cloudflared-loods.service

says active, and a 1033/1016 page means DNS arrived but the connector did not.
MSG
fi
