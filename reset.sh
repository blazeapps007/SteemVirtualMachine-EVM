#!/usr/bin/env bash
#
# reset.sh — wipe an existing validator's local chain data and fast-resync
# via state-sync, without touching keys.
#
# Use this instead of a full genesis replay when your node's chain data is
# corrupted, hit an app-hash mismatch, or you just want a much faster resync
# than replaying every block from genesis. It wipes ONLY the `data/`
# directory (blocks, state, tx index, the last-signed-height guard) via
# `steemvmd comet unsafe-reset-all` — it never touches
# config/priv_validator_key.json (your validator's consensus identity) or
# your account keyring, so your validator keeps its identity: no
# re-registering your Steem name, no re-staking, no new create-validator.
#
# Usage: ./reset.sh [--drop-addrbook] [--no-statesync] [--seed-rpc URL]
#   --drop-addrbook   also wipe config/addrbook.json (default: kept)
#   --no-statesync    skip state-sync, just replay from genesis instead
#   --seed-rpc URL    RPC of a live, synced peer to pull a state-sync trust
#                      anchor + peer list from (default: see SEED_RPC below)
#
# Overridable via env: SEED_RPC, STEEMVM_HOME, START_TIMEOUT, COMPOSE,
# CONTAINER, BIN, HOME_DIR, CHAIN_ID, KEYRING

# re-exec under bash if run as `sh reset.sh`
if [ -z "${BASH_VERSION:-}" ]; then
  if command -v bash >/dev/null 2>&1; then
    exec bash "$0" "$@"
  else
    echo "ERROR: this script requires bash." >&2
    exit 1
  fi
fi

set -euo pipefail

# ── config (override via env) ────────────────────────────────────────────────
COMPOSE="${COMPOSE:-docker compose}"
CONTAINER="${CONTAINER:-steemvm-node}"
BIN="${BIN:-/root/go/bin/steemvmd}"
HOME_DIR="${HOME_DIR:-/root/.steemvm}"
STEEMVM_HOME="${STEEMVM_HOME:-$HOME/.steemvm}"    # must match docker-compose.yml's bind mount
export STEEMVM_HOME
CHAIN_ID="${CHAIN_ID:-steemvm}"
KEYRING="${KEYRING:-test}"

SEED_RPC="${SEED_RPC:-http://57.131.13.43:26657}"
START_TIMEOUT="${START_TIMEOUT:-1800}"
KEEP_ADDRBOOK=1
USE_STATESYNC=1

# ── helpers ──────────────────────────────────────────────────────────────────
log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m✔\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

node() { docker exec "$CONTAINER" "$BIN" "$@"; }
kf() { node "$@" --keyring-backend "$KEYRING" --home "$HOME_DIR"; }

command -v docker >/dev/null || die "docker not found on PATH"
$COMPOSE version >/dev/null 2>&1 || die "'$COMPOSE' not available (set COMPOSE=docker-compose ?)"
[ -f docker-compose.yml ] || die "run this from the repository root."

# ── arg parsing ──────────────────────────────────────────────────────────────
while [ $# -gt 0 ]; do
  case "$1" in
    --drop-addrbook) KEEP_ADDRBOOK=0 ;;
    --no-statesync)  USE_STATESYNC=0 ;;
    --seed-rpc)      shift; SEED_RPC="${1:-}"; [ -n "$SEED_RPC" ] || die "--seed-rpc needs a URL" ;;
    *) die "unknown argument: $1 (see the script header for usage)" ;;
  esac
  shift
done

# ── state-sync / peer discovery helpers (same logic as new-validator.sh) ────
MIN_STATESYNC_HEIGHT=1500

EXTRA_PERSISTENT_PEERS="fe9ccc3ada6f92f20028430021585e413562bdbc@95.217.44.178:26656 9ce6a5ecd05e9bd8a7f455976b308094329d7937@167.235.9.31:26656"

fetch_statesync_trust() {
  TRUST_HEIGHT="" TRUST_HASH=""
  command -v curl >/dev/null 2>&1 && command -v jq >/dev/null 2>&1 || return 1
  local latest h hash
  latest="$(curl -fsS "${SEED_RPC%/}/status" 2>/dev/null | jq -r '.result.sync_info.latest_block_height // empty' 2>/dev/null)"
  [ -n "$latest" ] || return 1
  [ "$latest" -ge "$MIN_STATESYNC_HEIGHT" ] || return 1
  h=$((latest - 1000))
  [ "$h" -lt 1 ] && h=1
  hash="$(curl -fsS "${SEED_RPC%/}/commit?height=$h" 2>/dev/null | jq -r '.result.signed_header.commit.block_id.hash // empty' 2>/dev/null)"
  [ -n "$hash" ] || return 1
  TRUST_HEIGHT="$h" TRUST_HASH="$hash"
  return 0
}

fetch_seed_peer() {
  SEED_PEER="" SEED_RPC_LIST=""
  command -v curl >/dev/null 2>&1 && command -v jq >/dev/null 2>&1 || return 1
  local node_id host p
  node_id="$(curl -fsS "${SEED_RPC%/}/status" 2>/dev/null | jq -r '.result.node_info.id // empty' 2>/dev/null)"
  [ -n "$node_id" ] || return 1
  host="$(printf '%s' "$SEED_RPC" | sed -E 's#^[a-zA-Z]+://##; s#[:/].*##')"
  [ -n "$host" ] || return 1
  SEED_PEER="${node_id}@${host}:26656"
  for p in $EXTRA_PERSISTENT_PEERS; do
    case "$p" in
      *"@${host}:"*) ;;
      *) SEED_PEER="${SEED_PEER},${p}" ;;
    esac
  done
  SEED_RPC_LIST="${SEED_RPC},${SEED_RPC}"
  return 0
}

apply_seed_peer() {
  local f="$1"
  [ -f "$f" ] || return 1
  [ -n "${SEED_PEER:-}" ] || return 1
  sed -i.bak \
    -e "s|^persistent_peers = .*|persistent_peers = \"$SEED_PEER\"|" \
    -e "/^\[statesync\]/,/^\[/{s|^rpc_servers = .*|rpc_servers = \"$SEED_RPC_LIST\"|}" \
    "$f"
  rm -f "$f.bak"
  warn "peers set live from \$SEED_RPC: $SEED_PEER"
  return 0
}

enable_statesync() {
  local f="$1"
  [ -f "$f" ] || return 1
  [ -n "${TRUST_HEIGHT:-}" ] && [ -n "${TRUST_HASH:-}" ] || return 1
  sed -i.bak \
    -e '/^\[statesync\]/,/^\[/{s/^enable = false/enable = true/}' \
    -e "/^\[statesync\]/,/^\[/{s/^trust_height = .*/trust_height = $TRUST_HEIGHT/}" \
    -e "/^\[statesync\]/,/^\[/{s/^trust_hash = .*/trust_hash = \"$TRUST_HASH\"/}" \
    "$f"
  rm -f "$f.bak"
  return 0
}

disable_statesync() {
  local f="$1"
  [ -f "$f" ] || return 1
  grep -q '^\[statesync\]' "$f" || return 1
  sed -i.bak -e '/^\[statesync\]/,/^\[/{s/^enable = true/enable = false/}' "$f"
  rm -f "$f.bak"
  return 0
}

# ── sanity checks before touching anything ──────────────────────────────────
CONFIG_TOML="$STEEMVM_HOME/config/config.toml"
KEY_FILE="$STEEMVM_HOME/config/priv_validator_key.json"

[ -f "$KEY_FILE" ] || die "$KEY_FILE not found — this isn't an existing validator home. Use new-validator.sh for a first-time setup instead."
[ -f "$CONFIG_TOML" ] || die "$CONFIG_TOML not found."

log "This will wipe local chain data (blocks/state/tx-index) under $STEEMVM_HOME and"
log "resync it, either via state-sync or a full replay from genesis."
log "NOT touched: config/priv_validator_key.json (your validator identity), your"
log "account keyring, validator.json, genesis.json, and every other config file."
warn "Stop here if you're not sure — your node will be briefly out of sync while it"
warn "resyncs, which risks missing blocks/duty windows until it catches up."
read -rp "Type YES to confirm: " CONFIRM < /dev/tty
[ "$CONFIRM" = "YES" ] || die "reset cancelled — nothing was touched."

# ── 1. stop the node (keep the volume/bind mount, keep the oracle-data volume) ─
log "Stopping the node…"
$COMPOSE down

# ── 2. fetch a state-sync trust anchor + fresh peer list before wiping data ──
if [ "$USE_STATESYNC" = "1" ]; then
  if fetch_statesync_trust; then
    ok "State-sync trust anchor from $SEED_RPC: height $TRUST_HEIGHT."
  else
    warn "state-sync unavailable (chain below height $MIN_STATESYNC_HEIGHT, or $SEED_RPC unreachable) — falling back to a full replay from genesis."
    USE_STATESYNC=0
  fi
fi
if fetch_seed_peer; then
  ok "Live peer info fetched: $SEED_PEER"
else
  warn "could not fetch live peer info from $SEED_RPC — keeping config.toml's existing persistent_peers."
fi

# ── 3. wipe chain data, keys untouched ───────────────────────────────────────
log "Running unsafe-reset-all…"
RESET_ARGS="comet unsafe-reset-all --home $HOME_DIR"
[ "$KEEP_ADDRBOOK" = "1" ] && RESET_ARGS="$RESET_ARGS --keep-addr-book"
# -T: no pseudo-TTY, so this also works over a non-interactive SSH session.
# --entrypoint overrides docker-entrypoint.sh's full node-bootstrap logic —
# this runs the CLI subcommand directly against the bind-mounted home and
# nothing else.
# shellcheck disable=SC2086
$COMPOSE run --rm -T --entrypoint "$BIN" steemvm $RESET_ARGS
ok "Chain data wiped."

[ -f "$KEY_FILE" ] || die "priv_validator_key.json is gone after reset — this should never happen. STOP and investigate before restarting; do not create a new key."
ok "priv_validator_key.json still present — validator identity intact."

# ── 4. patch config.toml: fresh peers, state-sync (or explicit disable) ─────
apply_seed_peer "$CONFIG_TOML" || true
if [ "$USE_STATESYNC" = "1" ] && enable_statesync "$CONFIG_TOML"; then
  ok "State-sync enabled in $CONFIG_TOML — will fast-bootstrap from a snapshot."
else
  disable_statesync "$CONFIG_TOML" || true
  log "State-sync disabled — will do a full replay from genesis."
fi

# ── 5. start the node ─────────────────────────────────────────────────────────
log "Starting the node…"
$COMPOSE up -d

log "Waiting for the node to catch up (timeout ${START_TIMEOUT}s)…"
deadline=$(( $(date +%s) + START_TIMEOUT ))
height=0
while :; do
  if docker exec "$CONTAINER" test -x "$BIN" 2>/dev/null; then
    STATUS="$(node status 2>/dev/null || true)"
    if [ -n "$STATUS" ]; then
      CATCHING_UP="$(printf '%s' "$STATUS" | jq -r '.sync_info.catching_up' 2>/dev/null || true)"
      h="$(printf '%s' "$STATUS" | jq -r '.sync_info.latest_block_height // empty' 2>/dev/null || true)"
      [ -n "$h" ] && height="$h"
      log "sync check: height=${height:-0} catching_up=${CATCHING_UP:-<empty>}"
      if [ "$CATCHING_UP" = "false" ] && [ "${height:-0}" -ge 1 ] 2>/dev/null; then
        break
      fi
    else
      warn "sync check: 'node status' returned nothing (container not ready yet?)"
    fi
  else
    warn "sync check: $BIN not found/executable in $CONTAINER yet"
  fi
  [ "$(date +%s)" -ge "$deadline" ] && die "node did not finish syncing within ${START_TIMEOUT}s (still at height ${height:-0}). Check: \`$COMPOSE logs -f steemvm\`."
  sleep 10
done
ok "Node is caught up (height $height)."

# ── 6. confirm keys are all still there ──────────────────────────────────────
if kf keys list --output json >/dev/null 2>&1; then
  N="$(kf keys list --output json 2>/dev/null | jq 'length' 2>/dev/null || echo '?')"
  ok "Keyring intact — $N key(s) found under keyring-backend '$KEYRING'."
else
  warn "could not list keys with keyring-backend '$KEYRING' — if you use a different backend, check manually: docker exec -it $CONTAINER $BIN keys list --keyring-backend <yours> --home $HOME_DIR"
fi

ok "Done. Confirm your validator is bonded and signing:"
echo "    docker exec $CONTAINER $BIN query staking validators --home $HOME_DIR"
warn "If you run an oracle client (oracle-go/python/js), it was stopped by 'compose down'"
warn "and needs to be brought back up once the node is healthy:"
echo "    $COMPOSE --profile <go|python|js> up -d"
