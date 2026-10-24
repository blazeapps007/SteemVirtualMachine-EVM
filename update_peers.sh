#!/usr/bin/env bash
#
# update_peers.sh — rebuild persistent_peers for a running node from the
# network's live node list, leaving this node itself out.
#
# Candidates come from:
#   1. PEERS_RPC (the public steemvm-2 RPC): its own node (/status) and every
#      node it is connected to (/net_info) — so validators that join later are
#      picked up without editing this script;
#   2. NODE_IPS + KNOWN_PEERS, the standing list (used even when PEERS_RPC is
#      down). Each IP's node ID comes from, in order: that IP's own RPC (only
#      if public), this node's /net_info, the entry config.toml already has
#      for it (node IDs survive resets — node_key.json is kept), KNOWN_PEERS.
# A candidate is kept only if its p2p port answers from here: a node behind
# NAT (a home connection, a local-only machine) can't be dialled, so it is left
# out. Skipped too: this node (by node ID and by IP), private addresses,
# EXCLUDE_IPS, and duplicates (by node ID and by IP).
#
# Patches $STEEMVM_HOME/config/config.toml (and the Instructions/config.toml
# cache). Called from update.sh as one step of a full update; standalone, add
# --restart to restart the node so the new list takes effect.
#
# Usage: ./update_peers.sh [--restart | --no-restart-hint]
#
# Overridable via env:
#   PEERS_RPC   public CometBFT RPC listing the network (default:
#               https://rpc.steemvmscan.com; empty = standing list only)
#   NODE_IPS    space-separated standing validator IPs
#   KNOWN_PEERS space-separated id@ip:port entries used when nothing else
#               knows an IP's node ID
#   EXCLUDE_IPS space-separated IPs never to add
#   SELF_IP     skip self-detection and use this IP as "self" instead
#   P2P_PORT    p2p port appended to each peer entry (default: 26656)
#   RPC_PORT    RPC port queried for each IP's node ID (default: 26657)
#   LOCAL_RPC   this node's own RPC (default: http://127.0.0.1:$RPC_PORT)
#   STEEMVM_HOME  node home to patch (default: the running node's mount,
#                 else $HOME/.steemvm)
#   CONTAINER   the node container (default: steemvm-node)

if [ -z "${BASH_VERSION:-}" ]; then
  if command -v bash >/dev/null 2>&1; then
    exec bash "$0" "$@"
  else
    echo "ERROR: this script requires bash." >&2
    exit 1
  fi
fi

set -euo pipefail

PEERS_RPC="${PEERS_RPC-https://rpc.steemvmscan.com}"
NODE_IPS="${NODE_IPS:-62.169.19.142}"
KNOWN_PEERS="${KNOWN_PEERS:-c45b7eb5b59f2f86c270e0971df1f74ee1776348@62.169.19.142:26656}"
EXCLUDE_IPS="${EXCLUDE_IPS:-}"
P2P_PORT="${P2P_PORT:-26656}"
RPC_PORT="${RPC_PORT:-26657}"
LOCAL_RPC="${LOCAL_RPC:-http://127.0.0.1:${RPC_PORT}}"
CONTAINER="${CONTAINER:-steemvm-node}"

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m✔\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || die "curl is required."
command -v jq   >/dev/null 2>&1 || die "jq is required."

RESTART=0 RESTART_HINT=1
case "${1:-}" in
  --restart)         RESTART=1 ;;
  --no-restart-hint) RESTART_HINT=0 ;;
  "") ;;
  *) die "unknown argument: $1 (see the script header for usage)" ;;
esac

# The home the running node mounts wins over a guess from $HOME (sudo).
if [ -z "${STEEMVM_HOME:-}" ]; then
  STEEMVM_HOME="$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/root/.steemvm"}}{{.Source}}{{end}}{{end}}' "$CONTAINER" 2>/dev/null || true)"
  STEEMVM_HOME="${STEEMVM_HOME:-$HOME/.steemvm}"
fi

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

id_from_known() {
  local id
  # shellcheck disable=SC2086 # word-split the list on purpose
  id="$(printf '%s\n' $KNOWN_PEERS | awk -F@ -v ip="$1" '{ split($2, a, ":") } a[1] == ip { print $1; exit }')"
  [ -n "$id" ] || return 1
  printf '%s' "$id"
}

# private_ip: an address no other validator can dial.
private_ip() {
  case "$1" in
    10.*|127.*|0.*|192.168.*|169.254.*|172.1[6-9].*|172.2[0-9].*|172.3[01].*) return 0 ;;
  esac
  return 1
}

# p2p_open <ip> <port>: the p2p port accepts a TCP connection from here.
# (A subshell, so a timed-out attempt doesn't print "Terminated".)
p2p_open() { ( timeout 6 bash -c "exec 3<>/dev/tcp/$1/$2" ) 2>/dev/null; }

# public_addr <listen_addr> <remote_ip>: a node advertising 0.0.0.0 (or a
# private address) is dialled at the address it connects from.
public_addr() {
  local a="${1#tcp://}" ip port
  ip="${a%:*}" port="${a##*:}"
  [ "$port" = "$a" ] && port="$P2P_PORT"
  case "$ip" in ""|0.0.0.0|"[::]") ip="$2" ;; esac
  if private_ip "$ip" && [ -n "$2" ]; then ip="$2"; fi
  printf '%s:%s' "$ip" "${port:-$P2P_PORT}"
}

if detect_self_ip; then
  ok "This host's IP: $SELF_IP (excluded from its own persistent_peers)"
else
  warn "could not determine this host's own IP — relying on the node ID to exclude this node. Set SELF_IP to override."
  SELF_IP=""
fi
SELF_ID="$(curl -fsS --max-time 5 "${LOCAL_RPC%/}/status" 2>/dev/null | jq -r '.result.node_info.id // empty' 2>/dev/null || true)"
[ -n "$SELF_ID" ] && ok "This node's ID: $SELF_ID (excluded too)"

# ── candidates: "id ip:port source" lines ───────────────────────────────────
CANDIDATES=""
add_candidate() { CANDIDATES="${CANDIDATES}$1 $2 $3"$'\n'; }

if [ -n "$PEERS_RPC" ]; then
  if STATUS="$(curl -fsS --max-time 10 "${PEERS_RPC%/}/status" 2>/dev/null)"; then
    sid="$(printf '%s' "$STATUS" | jq -r '.result.node_info.id // empty')"
    shost="$(printf '%s' "$PEERS_RPC" | sed -E 's#^[a-zA-Z]+://##; s#[:/].*##')"
    saddr="$(public_addr "$(printf '%s' "$STATUS" | jq -r '.result.node_info.listen_addr // empty')" "$shost")"
    [ -n "$sid" ] && add_candidate "$sid" "$saddr" "$PEERS_RPC"
    NET_INFO="$(curl -fsS --max-time 10 "${PEERS_RPC%/}/net_info" 2>/dev/null || true)"
    while read -r pid plisten pip; do
      [ -n "$pid" ] && add_candidate "$pid" "$(public_addr "$plisten" "$pip")" "$PEERS_RPC/net_info"
    done < <(printf '%s' "$NET_INFO" | jq -r '.result.peers[]? | "\(.node_info.id) \(.node_info.listen_addr) \(.remote_ip)"' 2>/dev/null || true)
  else
    warn "$PEERS_RPC is unreachable — using the standing list only."
  fi
fi

for ip in $NODE_IPS; do
  if id="$(fetch_node_id "$ip")"; then src="its RPC"
  elif id="$(id_from_net_info "$ip")"; then src="connected to this node now"
  elif id="$(id_from_config "$ip")"; then src="kept from config.toml"
  elif id="$(id_from_known "$ip")"; then src="KNOWN_PEERS"
  else
    warn "$ip -> node ID unknown (RPC private, not connected, not in config.toml), skipping"
    continue
  fi
  add_candidate "$id" "${ip}:${P2P_PORT}" "$src"
done

# ── filter ───────────────────────────────────────────────────────────────────
PEER_LIST="" SEEN_IDS=" " SEEN_IPS=" "
while read -r id addr src; do
  [ -n "$id" ] || continue
  ip="${addr%:*}" port="${addr##*:}"
  case "$SEEN_IDS" in *" $id "*) continue ;; esac
  SEEN_IDS="$SEEN_IDS$id "
  if [ "$id" = "$SELF_ID" ] || { [ -n "$SELF_IP" ] && [ "$ip" = "$SELF_IP" ]; }; then
    log "skipping $id@$addr (this node)"; continue
  fi
  case " $EXCLUDE_IPS " in *" $ip "*) log "skipping $id@$addr (EXCLUDE_IPS)"; continue ;; esac
  private_ip "$ip" && { log "skipping $id@$addr (private address)"; continue; }
  case "$SEEN_IPS" in *" $ip "*) log "skipping $id@$addr (another node ID already listed for $ip)"; continue ;; esac
  if ! p2p_open "$ip" "$port"; then
    warn "skipping $id@$addr ($src): its p2p port doesn't answer from here (behind NAT, or down)"
    continue
  fi
  SEEN_IPS="$SEEN_IPS$ip "
  ok "$id@$addr ($src)"
  PEER_LIST="${PEER_LIST:+$PEER_LIST,}${id}@${addr}"
done <<< "$CANDIDATES"

[ -n "$PEER_LIST" ] || die "no reachable peers found (PEERS_RPC: ${PEERS_RPC:-none}; NODE_IPS: $NODE_IPS)."

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

if [ "$RESTART" = "1" ]; then
  if docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "$CONTAINER"; then
    log "Restarting $CONTAINER so the new peers take effect…"
    docker restart "$CONTAINER" >/dev/null
    ok "Restarted $CONTAINER."
  else
    warn "$CONTAINER isn't running — the new peers apply on its next start."
  fi
elif [ "$RESTART_HINT" = "1" ]; then
  warn "config.toml changes only take effect on restart: ./update_peers.sh --restart (or docker restart $CONTAINER)"
fi
