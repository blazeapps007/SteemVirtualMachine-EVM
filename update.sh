#!/usr/bin/env bash
#
# update.sh — full validator update: checkout latest code, reset + fast
# resync the chain via state-sync, refresh persistent_peers, restart the
# node, unjail if needed, and rebuild + restart the oracle client.
#
# Usage: ./update.sh [branch-or-tag]
#   branch-or-tag   optional git ref to check out before pulling (default:
#                    stay on the current branch and just pull it)
#
# Overridable via env: COMPOSE, NODE_IPS, SELF_IP, P2P_PORT, RPC_PORT,
# STEEMVM_HOME, CONTAINER, BIN, HOME_DIR, CHAIN_ID, KEYRING, GAS_PRICES,
# VALIDATOR_KEY (keyring key name used for the unjail check — auto-detected
# if you have exactly one key), ORACLE_PROFILE (go|python|js — auto-detected
# from whatever's currently running if not set), START_TIMEOUT

if [ -z "${BASH_VERSION:-}" ]; then
  if command -v bash >/dev/null 2>&1; then
    exec bash "$0" "$@"
  else
    echo "ERROR: this script requires bash." >&2
    exit 1
  fi
fi

set -euo pipefail

COMPOSE="${COMPOSE:-docker compose}"
CONTAINER="${CONTAINER:-steemvm-node}"
BIN="${BIN:-/root/go/bin/steemvmd}"
HOME_DIR="${HOME_DIR:-/root/.steemvm}"
STEEMVM_HOME="${STEEMVM_HOME:-$HOME/.steemvm}"
export STEEMVM_HOME
CHAIN_ID="${CHAIN_ID:-steemvm}"
KEYRING="${KEYRING:-test}"
GAS_PRICES="${GAS_PRICES:-1000000000asteem}"
VALIDATOR_KEY="${VALIDATOR_KEY:-}"
ORACLE_PROFILE="${ORACLE_PROFILE:-}"

NODE_IPS="${NODE_IPS:-95.217.44.178 62.169.19.142 57.131.13.43 167.235.9.31}"
export NODE_IPS
P2P_PORT="${P2P_PORT:-26656}"
RPC_PORT="${RPC_PORT:-26657}"
export P2P_PORT RPC_PORT
MIN_STATESYNC_HEIGHT=1500
START_TIMEOUT="${START_TIMEOUT:-1800}"

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m✔\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

node() { docker exec "$CONTAINER" "$BIN" "$@"; }
node_i() { docker exec -it "$CONTAINER" "$BIN" "$@"; }
kf() { node "$@" --keyring-backend "$KEYRING" --home "$HOME_DIR"; }
kf_i() { node_i "$@" --keyring-backend "$KEYRING" --home "$HOME_DIR"; }

command -v docker >/dev/null || die "docker not found on PATH"
$COMPOSE version >/dev/null 2>&1 || die "'$COMPOSE' not available (set COMPOSE=docker-compose ?)"
command -v curl >/dev/null 2>&1 || die "curl is required."
command -v jq   >/dev/null 2>&1 || die "jq is required."
[ -f docker-compose.yml ] || die "run this from the repository root."
[ -d .git ] || die "not a git checkout — run this from the repository root."
[ -f update_peers.sh ] || die "update_peers.sh not found next to this script."

CONFIG_TOML="$STEEMVM_HOME/config/config.toml"
KEY_FILE="$STEEMVM_HOME/config/priv_validator_key.json"
[ -f "$KEY_FILE" ] || die "$KEY_FILE not found — this isn't an existing validator home. Use new-validator.sh for a first-time setup instead."

# ── 1. git checkout + pull ───────────────────────────────────────────────────
[ -z "$(git status --porcelain)" ] || die "you have local changes (git status is not clean) — commit, stash, or discard them first."

TARGET_REF="${1:-}"
log "Fetching…"
git fetch --all --tags --quiet
if [ -n "$TARGET_REF" ]; then
  log "Checking out $TARGET_REF…"
  git checkout "$TARGET_REF"
fi
if git symbolic-ref -q HEAD >/dev/null; then
  log "Pulling latest…"
  git pull
else
  warn "detached HEAD (on a tag, not a branch) — skipping pull, already at $(git rev-parse --short HEAD)."
fi
ok "At $(git rev-parse --short HEAD) ($(git describe --tags --always 2>/dev/null))."

# ── 2. detect a currently-running oracle profile (before we stop anything) ──
if [ -z "$ORACLE_PROFILE" ]; then
  RUNNING="$(docker ps --format '{{.Names}}' 2>/dev/null || true)"
  if printf '%s' "$RUNNING" | grep -qx 'steemvm-oracle-go'; then ORACLE_PROFILE=go
  elif printf '%s' "$RUNNING" | grep -qx 'steemvm-oracle-python'; then ORACLE_PROFILE=python
  elif printf '%s' "$RUNNING" | grep -qx 'steemvm-oracle-js'; then ORACLE_PROFILE=js
  fi
fi
if [ -n "$ORACLE_PROFILE" ]; then
  ok "Oracle profile currently running: $ORACLE_PROFILE (will rebuild + restart it)."
else
  warn "no oracle container currently running — none will be started. Set ORACLE_PROFILE=go|python|js to force one."
fi

# ── 3. stop everything (node + oracle, whichever is up) ─────────────────────
log "Stopping the node (and oracle, if running)…"
$COMPOSE down

# ── 4. fetch a state-sync trust anchor from a live peer, before wiping data ─
fetch_statesync_trust_from_nodes() {
  TRUST_HEIGHT="" TRUST_HASH="" TRUST_SEED=""
  local ip latest h hash
  for ip in $NODE_IPS; do
    latest="$(curl -fsS --max-time 5 "http://${ip}:${RPC_PORT}/status" 2>/dev/null | jq -r '.result.sync_info.latest_block_height // empty' 2>/dev/null)"
    [ -n "$latest" ] || continue
    [ "$latest" -ge "$MIN_STATESYNC_HEIGHT" ] || continue
    h=$((latest - 1000)); [ "$h" -lt 1 ] && h=1
    hash="$(curl -fsS --max-time 5 "http://${ip}:${RPC_PORT}/commit?height=$h" 2>/dev/null | jq -r '.result.signed_header.commit.block_id.hash // empty' 2>/dev/null)"
    [ -n "$hash" ] || continue
    TRUST_HEIGHT="$h" TRUST_HASH="$hash" TRUST_SEED="$ip"
    return 0
  done
  return 1
}

USE_STATESYNC=0
if fetch_statesync_trust_from_nodes; then
  ok "State-sync trust anchor from $TRUST_SEED: height $TRUST_HEIGHT."
  USE_STATESYNC=1
else
  warn "could not get a state-sync trust anchor from any of: $NODE_IPS — falling back to a full replay from genesis."
fi

# ── 5. reset chain data — unconditional, keys untouched ──────────────────────
log "Resetting chain data (keys are never touched)…"
# shellcheck disable=SC2086
$COMPOSE run --rm -T --entrypoint "$BIN" steemvm comet unsafe-reset-all --home "$HOME_DIR" --keep-addr-book
[ -f "$KEY_FILE" ] || die "priv_validator_key.json is gone after reset — STOP and investigate before starting the node."
ok "Chain data reset. priv_validator_key.json still present."

# ── 6. refresh persistent_peers ──────────────────────────────────────────────
log "Refreshing persistent_peers…"
./update_peers.sh --no-restart-hint

# ── 7. write the state-sync trust anchor into config.toml (or disable it) ───
enable_statesync() {
  local f="$1"
  [ -f "$f" ] || return 1
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
if [ "$USE_STATESYNC" = "1" ] && enable_statesync "$CONFIG_TOML"; then
  ok "State-sync enabled in $CONFIG_TOML."
else
  disable_statesync "$CONFIG_TOML" || true
  log "State-sync disabled — will replay from genesis."
fi

# ── 8. pull the latest published image and start the node ──────────────────
log "Pulling the latest node image…"
$COMPOSE pull steemvm || warn "image pull failed — continuing with whatever is cached locally."
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

# ── 9. unjail if needed ───────────────────────────────────────────────────────
if [ -z "$VALIDATOR_KEY" ]; then
  VALIDATOR_KEY="$(kf keys list --output json 2>/dev/null | jq -r 'if length==1 then .[0].name else empty end' 2>/dev/null || true)"
fi
if [ -n "$VALIDATOR_KEY" ]; then
  VALOPER="$(kf keys show "$VALIDATOR_KEY" --bech val -a 2>/dev/null | tr -d '\r' || true)"
  if [ -n "$VALOPER" ]; then
    JAILED="$(kf query staking validator "$VALOPER" --output json 2>/dev/null | jq -r '.jailed // empty' 2>/dev/null || true)"
    if [ "$JAILED" = "true" ]; then
      log "$VALOPER is jailed — unjailing…"
      set +e
      kf_i tx slashing unjail --from "$VALIDATOR_KEY" --chain-id "$CHAIN_ID" \
        --gas auto --gas-adjustment 1.5 --gas-prices "$GAS_PRICES" -y
      rc=$?
      set -e
      if [ $rc -eq 0 ]; then ok "Unjail tx broadcast."; else warn "unjail tx failed — check manually: kf query slashing signing-info <consensus-pubkey>"; fi
    else
      ok "$VALOPER is not jailed."
    fi
  else
    warn "could not resolve a valoper address for key '$VALIDATOR_KEY' — skipping the unjail check. Run it manually if needed."
  fi
else
  warn "could not auto-detect your validator's keyring key (found none, or more than one) — set VALIDATOR_KEY=<name> to enable the automatic unjail check. Skipping."
fi

# ── 10. rebuild + restart the oracle, if one was running ────────────────────
if [ -n "$ORACLE_PROFILE" ]; then
  log "Rebuilding and restarting the $ORACLE_PROFILE oracle (picks up any oracle code changes)…"
  $COMPOSE --profile "$ORACLE_PROFILE" up -d --build
  ok "Oracle restarted."
fi

# ── 11. verification checklist ────────────────────────────────────────────────
echo
ok "Update complete. Verify:"
echo "  version:        docker exec $CONTAINER $BIN version"
echo "  sync status:     docker exec $CONTAINER $BIN status | jq '.sync_info'"
echo "  peer count:      docker exec $CONTAINER $BIN status | jq '.node_info.id' ; curl -s http://localhost:26657/net_info | jq '.result.n_peers'"
echo "  bonded/jailed:   docker exec $CONTAINER $BIN query staking validator \$($COMPOSE exec -T steemvm $BIN keys show ${VALIDATOR_KEY:-<your-key>} --bech val -a --keyring-backend $KEYRING --home $HOME_DIR)"
if [ -n "$ORACLE_PROFILE" ]; then
  echo "  oracle logs:     $COMPOSE logs -f oracle-$ORACLE_PROFILE"
fi
echo "  node logs:       $COMPOSE logs -f steemvm"
