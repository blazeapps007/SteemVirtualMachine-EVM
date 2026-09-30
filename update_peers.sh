#!/usr/bin/env bash
#
# update_peers.sh — rebuild persistent_peers from a fixed list of known
# validator IPs. Excludes whichever of those IPs turns out to be this host
# itself. Each IP's node ID comes from, in order:
#   1. that IP's own RPC (/status) — only if it serves RPC publicly, which
#      most validators no longer do (docker-compose.yml binds it to 127.0.0.1);
#   2. this node's own /net_info — the peers it is connected to right now;
#   3. the entry config.toml already has for that IP. Node IDs survive resets
#      (node_key.json is kept), so a known entry stays valid.
# An IP found by none of them is left out.
#
# Standalone: patches config.toml, prints the result, and reminds you a
# restart is needed to pick it up. Called from update.sh as one step of a
# full update (which also does the restart).
#
# Usage: ./update_peers.sh
#
# Overridable via env:
#   NODE_IPS    space-separated list of known validator IPs (default: the
#               four below)
#   SELF_IP     skip self-detection and use this IP as "self" instead
#   P2P_PORT    p2p port appended to each peer entry (default: 26656)
#   RPC_PORT    RPC port queried for each IP's node ID (default: 26657)
#   LOCAL_RPC   this node's own RPC, for /net_info (default:
#               http://127.0.0.1:$RPC_PORT)
#   STEEMVM_HOME  node home to patch (default: $HOME/.steemvm, must match
#                 docker-compose.yml's bind mount)

if [ -z "${BASH_VERSION:-}" ]; then
  if command -v bash >/dev/null 2>&1; then
    exec bash "$0" "$@"
  else
    echo "ERROR: this script requires bash." >&2
    exit 1
  fi
fi

set -euo pipefail

NODE_IPS="${NODE_IPS:-95.217.44.178 62.169.19.142 57.131.13.43 167.235.9.31}"
P2P_PORT="${P2P_PORT:-26656}"
RPC_PORT="${RPC_PORT:-26657}"
LOCAL_RPC="${LOCAL_RPC:-http://127.0.0.1:${RPC_PORT}}"
STEEMVM_HOME="${STEEMVM_HOME:-$HOME/.steemvm}"

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m✔\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || die "curl is required."
command -v jq   >/dev/null 2>&1 || die "jq is required."

# detect_self_ip: prefer matching a known IP against this host's own network
# interfaces (works directly on a VPS/bare-metal box with the public IP
# bound to an interface, no external call needed); fall back to asking a
# public IP-echo service if none of the known IPs are locally bound (e.g.
# behind NAT). SELF_IP env always wins over both.
detect_self_ip() {
  SELF_IP="${SELF_IP:-}"
  [ -n "$SELF_IP" ] && return 0

  local local_ips ip
  local_ips="$( (ip -4 addr show 2>/dev/null || ifconfig 2>/dev/null) | grep -oE '([0-9]{1,3}\.){3}[0-9]{1,3}' || true)"
  for ip in $NODE_IPS; do
    if printf '%s\n' "$local_ips" | grep -qx "$ip"; then
      SELF_IP="$ip"
      return 0
    fi
  done

  SELF_IP="$(curl -fsS --max-time 5 https://api.ipify.org 2>/dev/null || true)"
  [ -n "$SELF_IP" ]
}

fetch_node_id() {
  local ip="$1" resp id
  resp="$(curl -fsS --max-time 5 "http://${ip}:${RPC_PORT}/status" 2>/dev/null)" || return 1
  id="$(printf '%s' "$resp" | jq -r '.result.node_info.id // empty' 2>/dev/null)"
  [ -n "$id" ] || return 1
  printf '%s' "$id"
}

LOCAL_NET_INFO="$(curl -fsS --max-time 5 "${LOCAL_RPC%/}/net_info" 2>/dev/null || true)"
id_from_net_info() {
  local id
  [ -n "$LOCAL_NET_INFO" ] || return 1
  id="$(printf '%s' "$LOCAL_NET_INFO" | jq -r --arg ip "$1" '[.result.peers[]? | select(.remote_ip == $ip) | .node_info.id][0] // empty' 2>/dev/null)"
  [ -n "$id" ] || return 1
  printf '%s' "$id"
}

EXISTING_PEERS="$(cat "$STEEMVM_HOME/config/config.toml" Instructions/config.toml 2>/dev/null | sed -n 's/^persistent_peers = "\(.*\)"/\1/p' | tr ',' '\n' | tr -d ' ' || true)"
id_from_config() {
  local id
  id="$(printf '%s\n' "$EXISTING_PEERS" | awk -F@ -v ip="$1" '{ split($2, a, ":") } a[1] == ip { print $1; exit }')"
  [ -n "$id" ] || return 1
  printf '%s' "$id"
}

if detect_self_ip; then
  ok "This host's IP: $SELF_IP (excluded from its own persistent_peers)"
else
  warn "could not determine this host's own IP — will query every IP in NODE_IPS, including possibly itself. Set SELF_IP to override."
  SELF_IP=""
fi

PEER_LIST=""
for ip in $NODE_IPS; do
  if [ -n "$SELF_IP" ] && [ "$ip" = "$SELF_IP" ]; then
    log "skipping $ip (self)"
    continue
  fi
  if id="$(fetch_node_id "$ip")"; then
    ok "$ip -> $id"
  elif id="$(id_from_net_info "$ip")"; then
    ok "$ip -> $id (connected to this node now)"
  elif id="$(id_from_config "$ip")"; then
    ok "$ip -> $id (kept from config.toml — its RPC is private and it isn't connected right now)"
  else
    warn "$ip -> node ID unknown (RPC private, not connected, not in config.toml), skipping"
    continue
  fi
  entry="${id}@${ip}:${P2P_PORT}"
  if [ -z "$PEER_LIST" ]; then PEER_LIST="$entry"; else PEER_LIST="${PEER_LIST},${entry}"; fi
done

[ -n "$PEER_LIST" ] || die "no peer node IDs found for: $NODE_IPS (all unknown, or all matched as self)."

log "persistent_peers = \"$PEER_LIST\""

patch_peers() {
  local f="$1"
  [ -f "$f" ] || return 1
  sed -i.bak "s|^persistent_peers = .*|persistent_peers = \"$PEER_LIST\"|" "$f"
  rm -f "$f.bak"
  return 0
}

PATCHED=0
if patch_peers "$STEEMVM_HOME/config/config.toml"; then
  ok "Patched $STEEMVM_HOME/config/config.toml"
  PATCHED=1
fi
# Also keep the local (gitignored) cache in sync, since a genuinely fresh
# node home is seeded from Instructions/config.toml, not from the running
# node's own config.
if patch_peers "Instructions/config.toml"; then
  ok "Patched Instructions/config.toml"
  PATCHED=1
fi

[ "$PATCHED" = "1" ] || warn "found no config.toml to patch (neither \$STEEMVM_HOME/config/config.toml nor Instructions/config.toml exists)."

if [ "${1:-}" != "--no-restart-hint" ]; then
  warn "config.toml changes only take effect on restart: docker compose down && docker compose up -d"
fi
