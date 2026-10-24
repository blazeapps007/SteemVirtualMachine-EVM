#!/usr/bin/env bash
#
# reset.sh — wipe a validator's local chain data and fast-resync it via
# state-sync, keeping every key: config/priv_validator_key.json (consensus
# identity), config/node_key.json, the account keyring, and oracle/.env.
#
# Two cases, detected automatically:
#
#   Same chain (the node home's genesis has the chain-id of this checkout's
#   Instructions/genesis.json): wipe data/ -> state-sync -> restart. Your
#   validator keeps its identity: no re-registering, no re-staking.
#
#   New chain (this checkout's Instructions/genesis.json has a different
#   chain-id, e.g. steemvm -> steemvm-2): additionally installs the new
#   genesis (checked against the network's own), sets the new chain-id in
#   client.toml and the EVM chain-id in app.toml, drops the old chain's peers
#   and cosmovisor upgrade slots — then REJOINS the new chain as a validator
#   with the same keys and address:
#     1. registers your Steem name: sends 0.001 STEEM (the chain's minimum)
#        from @<you> to svm.bank with memo `svm-register <your address>`,
#        signed with your Steem ACTIVE key, then confirm-name. The key is read
#        from oracle/.env (ORACLE_STEEM_ACTIVE_WIF) or asked for once and saved
#        there — your oracle's svm.bank signer uses it too;
#     2. claims faucet coins (FAUCET_URL) and waits until your address holds
#        STAKE_AMOUNT — it stakes up to that, keeping 1 STEEM liquid for fees.
#        If the faucet doesn't cover it, bring STEEM over: bridge it (Steem
#        transfer to svm.bank, memo = your address) or ask an existing
#        validator to send it;
#     3. create-validator with your Steem public keys (the identity gate).
#   If funding takes longer than FUND_TIMEOUT, re-run with --rejoin-only.
#
# Either way the oracle that was running (go/python/js) is restarted on its
# published image with the same oracle/.env: its keys, price source and state
# (oracle-data volume) are kept.
#
# Usage: ./reset.sh [--drop-addrbook] [--no-statesync] [--seed-rpc URL] [--rejoin-only | --no-rejoin]
#   --drop-addrbook   also wipe config/addrbook.json (always done on a new chain)
#   --no-statesync    replay from genesis instead of state-sync — refused on
#                     steemvm-2: no single binary can replay across the v0.0.7
#                     upgrade at height 79664
#   --seed-rpc URL    CometBFT RPC of a synced node, for the state-sync trust
#                     anchor and peer (default: see SEED_RPC below)
#   --rejoin-only     no reset: just finish rejoining (registration, stake,
#                     create-validator) and start the oracle
#   --no-rejoin       move to a new chain as a plain full node (no validator,
#                     no Steem key asked) — e.g. an RPC node
#
# Overridable via env: SEED_RPC, STEEMVM_HOME, START_TIMEOUT, COMPOSE,
# CONTAINER, BIN, HOME_DIR, KEYRING, VALIDATOR_KEY, STEEM_USERNAME, STEEM_RPC,
# STAKE_AMOUNT, FAUCET_URL, GAS_PRICES, COMMISSION_RATE, COMMISSION_MAX_RATE,
# COMMISSION_MAX_CHANGE_RATE, MIN_SELF_DELEGATION, ORACLE_PROFILE,
# NAME_TIMEOUT, FUND_TIMEOUT

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
STEEMVM_HOME_FROM_ENV="${STEEMVM_HOME:-}"
STEEMVM_HOME="${STEEMVM_HOME:-$HOME/.steemvm}"    # must match docker-compose.yml's bind mount
export STEEMVM_HOME
KEYRING="${KEYRING:-}"   # default: keyring-backend in the node home's client.toml
GAS_PRICES="${GAS_PRICES:-1000000000asteem}"
VALIDATOR_KEY="${VALIDATOR_KEY:-}"
STEEM_USERNAME="${STEEM_USERNAME:-}"
ORACLE_PROFILE="${ORACLE_PROFILE:-}"

# Public RPC of the steemvm-2 genesis node (nginx, TLS). Validators bind their
# own RPC to 127.0.0.1 (docker-compose.yml), so they can't be a source.
SEED_RPC="${SEED_RPC:-https://rpc.steemvmscan.com}"
START_TIMEOUT="${START_TIMEOUT:-1800}"

# Rejoin (new chain) settings — the same defaults as new-validator.sh.
STAKE_AMOUNT="${STAKE_AMOUNT:-4000000000000000000000asteem}"   # 4000 STEEM
COMMISSION_RATE="${COMMISSION_RATE:-0.10}"
COMMISSION_MAX_RATE="${COMMISSION_MAX_RATE:-0.20}"
COMMISSION_MAX_CHANGE_RATE="${COMMISSION_MAX_CHANGE_RATE:-0.01}"
MIN_SELF_DELEGATION="${MIN_SELF_DELEGATION:-1}"
NAME_TIMEOUT="${NAME_TIMEOUT:-1800}"
FAUCET_URL="${FAUCET_URL-https://faucet.steemscanner.com/faucet}"   # empty: skip the faucet
FUND_TIMEOUT="${FUND_TIMEOUT:-3600}"
VALIDATOR_JSON="validator.json"

KEEP_ADDRBOOK=1
USE_STATESYNC=1
REJOIN_ONLY=0
NO_REJOIN=0

# ── helpers ──────────────────────────────────────────────────────────────────
log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m✔\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

node() { docker exec "$CONTAINER" "$BIN" "$@"; }
kf() { node "$@" --keyring-backend "$KEYRING" --home "$HOME_DIR"; }
# The node CLI against the bind-mounted home without a running node (the
# entrypoint's bootstrap is bypassed).
offline() { $COMPOSE run --rm -T --no-deps --entrypoint "$BIN" steemvm "$@" --home "$HOME_DIR"; }
# kcmd: a command that opens the keyring (keys show/list, tx …), on the
# running node or else a one-off container. A password-protected keyring
# (os/file) gets its passphrase on stdin: asked once, never on a command line.
KEYRING_PASS=""
kcmd() {
  local run
  # shellcheck disable=SC2206 # $COMPOSE is a command line on purpose
  if docker exec "$CONTAINER" true 2>/dev/null; then run=(docker exec -i "$CONTAINER" "$BIN")
  else run=($COMPOSE run --rm -T --no-deps --entrypoint "$BIN" steemvm); fi
  printf '%s\n' "$KEYRING_PASS" | "${run[@]}" "$@" --keyring-backend "$KEYRING" --home "$HOME_DIR"
}

steem_atoms() { awk -v a="${1%asteem}" 'BEGIN{printf "%.3f", a/1e18}'; }

valid_steem_name() {
  local seg segs
  [ "${#1}" -ge 3 ] && [ "${#1}" -le 16 ] || return 1
  [ "${1: -1}" != "." ] || return 1
  IFS=. read -ra segs <<< "$1"
  for seg in "${segs[@]}"; do
    [[ "$seg" =~ ^[a-z][a-z0-9-]+[a-z0-9]$ ]] && [[ "$seg" != *--* ]] || return 1
  done
}

# rpc_with_port: CometBFT's light client wants an explicit port.
rpc_with_port() {
  local u="${1%/}" rest
  rest="${u#*://}"
  case "$rest" in
    *:*) printf '%s' "$u" ;;
    *) case "$u" in https://*) printf '%s:443' "$u" ;; *) printf '%s:80' "$u" ;; esac ;;
  esac
}

command -v docker >/dev/null || die "docker not found on PATH"
command -v curl >/dev/null || die "curl is required."
command -v jq >/dev/null || die "jq is required."
$COMPOSE version >/dev/null 2>&1 || die "'$COMPOSE' not available (set COMPOSE=docker-compose ?)"
[ -f docker-compose.yml ] || die "run this from the repository root."

# ── arg parsing ──────────────────────────────────────────────────────────────
while [ $# -gt 0 ]; do
  case "$1" in
    --drop-addrbook) KEEP_ADDRBOOK=0 ;;
    --no-statesync)  USE_STATESYNC=0 ;;
    --seed-rpc)      shift; SEED_RPC="${1:-}"; [ -n "$SEED_RPC" ] || die "--seed-rpc needs a URL" ;;
    --rejoin-only)   REJOIN_ONLY=1 ;;
    --no-rejoin)     NO_REJOIN=1 ;;
    *) die "unknown argument: $1 (see the script header for usage)" ;;
  esac
  shift
done

# ── state-sync / peer discovery helpers (same logic as new-validator.sh) ────
MIN_STATESYNC_HEIGHT=1500

# Standing peers, always added to whatever $SEED_RPC resolves to: blaze.apps
# (the steemvm-2 genesis node), xpilar.witness, blazed007 — validators with
# their p2p port open. Space-separated "id@host:26656" entries.
EXTRA_PERSISTENT_PEERS="371e1665e6cff2e3317fe9befa48a69d8ba0f8b9@62.169.19.142:26656 276517876869cbb4b2f130983646ea6945920629@45.151.123.214:26656 36e386e3aaac8f892d8498c4c83355964a6458c2@57.131.13.43:26656"

fetch_statesync_trust() {
  TRUST_HEIGHT="" TRUST_HASH=""
  local latest h hash
  latest="$(curl -fsS -m 15 "${SEED_RPC%/}/status" 2>/dev/null | jq -r '.result.sync_info.latest_block_height // empty' 2>/dev/null)"
  [ -n "$latest" ] || return 1
  [ "$latest" -ge "$MIN_STATESYNC_HEIGHT" ] || return 1
  h=$((latest - 1000))
  [ "$h" -lt 1 ] && h=1
  hash="$(curl -fsS -m 15 "${SEED_RPC%/}/commit?height=$h" 2>/dev/null | jq -r '.result.signed_header.commit.block_id.hash // empty' 2>/dev/null)"
  [ -n "$hash" ] || return 1
  TRUST_HEIGHT="$h" TRUST_HASH="$hash"
  return 0
}

# fetch_seed_peer: the seed node's own p2p address (its advertised
# listen_addr, else the RPC's host on :26656) plus the standing extras.
fetch_seed_peer() {
  SEED_PEER="" SEED_RPC_LIST=""
  local status node_id host addr p rpc
  status="$(curl -fsS -m 15 "${SEED_RPC%/}/status" 2>/dev/null)" || return 1
  node_id="$(printf '%s' "$status" | jq -r '.result.node_info.id // empty' 2>/dev/null)"
  [ -n "$node_id" ] || return 1
  addr="$(printf '%s' "$status" | jq -r '.result.node_info.listen_addr // empty' 2>/dev/null)"
  addr="${addr#tcp://}"
  case "$addr" in
    ""|0.0.0.0:*|127.*|localhost:*|"[::]:"*)
      host="$(printf '%s' "$SEED_RPC" | sed -E 's#^[a-zA-Z]+://##; s#[:/].*##')"
      [ -n "$host" ] || return 1
      addr="${host}:26656" ;;
  esac
  # This node's own ID (its node_key.json is kept): a validator in the standing
  # list must not be its own persistent peer.
  local self_id
  self_id="$(offline comet show-node-id 2>/dev/null | tr -d '\r' | tail -1 || true)"
  [ "$node_id" = "$self_id" ] || SEED_PEER="${node_id}@${addr}"
  for p in $EXTRA_PERSISTENT_PEERS; do
    # Skip the seed itself — also under an old node ID at the same address
    # (a node reset with a fresh home gets a new ID) — and this node.
    case "$p" in
      "${node_id}@"*|*"@${addr}"|"${self_id:-none}@"*) ;;
      *) SEED_PEER="${SEED_PEER:+$SEED_PEER,}${p}" ;;
    esac
  done
  [ -n "$SEED_PEER" ] || return 1
  rpc="$(rpc_with_port "$SEED_RPC")"
  SEED_RPC_LIST="${rpc},${rpc}"
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

# set_evm_chain_id: app.toml's [evm] evm-chain-id must equal the chain's EVM
# chain-id, or the node refuses to start.
set_evm_chain_id() {
  local f="$1" id="$2"
  [ -f "$f" ] || return 1
  if awk '/^\[evm\]/{e=1;next} /^\[/{e=0} e && /^[[:space:]]*evm-chain-id[[:space:]]*=/{found=1} END{exit !found}' "$f"; then
    sed -i.bak "/^\[evm\]/,/^\[/{s|^\([[:space:]]*\)evm-chain-id[[:space:]]*=.*|\1evm-chain-id = $id|}" "$f"
  elif grep -q '^\[evm\]' "$f"; then
    sed -i.bak "/^\[evm\]/a\\
  evm-chain-id = $id" "$f"
  else
    return 1
  fi
  rm -f "$f.bak"
}

# ── oracle/.env (never rewritten: missing keys are appended, nothing else) ──
ENV_FILE="oracle/.env"
# A line is KEY=value, also with leading spaces, `export `, or spaces around =.
env_line() { printf '^[[:space:]]*(export[[:space:]]+)?%s[[:space:]]*=' "$1"; }
env_get() {
  [ -f "$ENV_FILE" ] || return 0
  sed -nE "s/$(env_line "$1")[[:space:]]*//p" "$ENV_FILE" | tail -1 | sed -E 's/[[:space:]]+#.*$//; s/^["'\'']//; s/["'\'']$//; s/[[:space:]]+$//'
}
env_set_if_missing() {
  [ -n "$(env_get "$1")" ] && return 0
  if [ -f "$ENV_FILE" ]; then
    sed -i.bak -E "/$(env_line "$1")[[:space:]]*\$/d" "$ENV_FILE"
    rm -f "$ENV_FILE.bak"
  fi
  printf '%s=%s\n' "$1" "$2" >> "$ENV_FILE"
}

# ── Steem helpers (the signing runs in the published oracle-python image) ───
steem() {
  curl -fsS -m 30 "$STEEM_RPC" -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":$2}"
}
STEEM_IMG=""
steem_helper() {
  if [ -z "$STEEM_IMG" ]; then
    STEEM_IMG="$($COMPOSE --profile python config --format json 2>/dev/null | jq -r '.services["oracle-python"].image // empty')"
    STEEM_IMG="${STEEM_IMG:-steemblazer/steemvm-oracle-python:v0.0.6}"
    docker pull -q "$STEEM_IMG" >/dev/null 2>&1 || docker image inspect "$STEEM_IMG" >/dev/null 2>&1 \
      || die "could not pull $STEEM_IMG (it signs the Steem registration transfer)."
  fi
  ORACLE_STEEM_ACTIVE_WIF="$ACTIVE_WIF" STEEM_RPCS="$STEEM_RPCS" \
    docker run --rm -i -e ORACLE_STEEM_ACTIVE_WIF -e STEEM_RPCS --entrypoint python "$STEEM_IMG" \
    -I - "$@" < oracle/tools/steem_transfer.py
}
# steem_memo_sent <account> <memo> <from-block>: a transfer from <account> to
# svm.bank with exactly <memo> at or after <from-block> is in its last 100
# history entries (so a re-run never pays the registration twice).
steem_memo_sent() {
  steem condenser_api.get_account_history "[\"$1\",-1,100]" 2>/dev/null \
    | jq -e --arg m "$2" --argjson b "$3" \
        '[.result[]? | .[1] | select(.block >= $b and .op[0] == "transfer" and .op[1].to == "svm.bank" and .op[1].memo == $m)] | length > 0' >/dev/null 2>&1
}

# ── sanity checks before touching anything ──────────────────────────────────
# The home the RUNNING node mounts wins over a guess from $HOME (sudo, or a
# compose stack started by another user).
LIVE_HOME="$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "'"$HOME_DIR"'"}}{{.Source}}{{end}}{{end}}' "$CONTAINER" 2>/dev/null || true)"
if [ -n "$LIVE_HOME" ]; then
  if [ -n "$STEEMVM_HOME_FROM_ENV" ] && [ "$STEEMVM_HOME_FROM_ENV" != "$LIVE_HOME" ]; then
    die "STEEMVM_HOME=$STEEMVM_HOME_FROM_ENV, but '$CONTAINER' uses $LIVE_HOME. Unset STEEMVM_HOME (or fix it). Nothing was touched."
  fi
  STEEMVM_HOME="$LIVE_HOME"
  export STEEMVM_HOME
fi

CONFIG_TOML="$STEEMVM_HOME/config/config.toml"
APP_TOML="$STEEMVM_HOME/config/app.toml"
CLIENT_TOML="$STEEMVM_HOME/config/client.toml"
KEY_FILE="$STEEMVM_HOME/config/priv_validator_key.json"

[ -f "$KEY_FILE" ] || die "$KEY_FILE not found — this isn't an existing validator home. Use new-validator.sh for a first-time setup instead."
[ -f "$CONFIG_TOML" ] || die "$CONFIG_TOML not found."
if [ -z "$KEYRING" ]; then
  KEYRING="$(sed -n 's/^keyring-backend = "\(.*\)"/\1/p' "$CLIENT_TOML" 2>/dev/null | head -1)"
  KEYRING="${KEYRING:-os}"   # the SDK's default when client.toml doesn't say
fi
[ -w "$CONFIG_TOML" ] || die "can't write $CONFIG_TOML — run this as the owner of the node home (usually root: sudo ./reset.sh)."

# During a coordinated upgrade (the branch carries a stage-vX.Y.Z service), which
# binary cosmovisor must run after a reset depends on whether the upgrade has
# already applied — update.sh works that out; this script doesn't.
SERVICES="$($COMPOSE --profile stage config --services)" || die "docker-compose.yml does not parse."
if printf '%s\n' "$SERVICES" | grep -q '^stage-v'; then
  die "a coordinated upgrade is pending on this branch ($(printf '%s\n' "$SERVICES" | grep '^stage-v' | tr '\n' ' ')). Use ./update.sh --reset instead — it stages the new binary and points cosmovisor at the right one. Nothing was touched."
fi

TARGET_CHAIN="$(tr -d '\r' < Instructions/genesis.json | jq -r '.chain_id // empty')"
[ -n "$TARGET_CHAIN" ] || die "Instructions/genesis.json has no chain_id."
TARGET_GTIME="$(tr -d '\r' < Instructions/genesis.json | jq -r '.genesis_time // empty')"
CURRENT_CHAIN="$(jq -r '.chain_id // empty' "$STEEMVM_HOME/config/genesis.json" 2>/dev/null || true)"
CURRENT_GTIME="$(jq -r '.genesis_time // empty' "$STEEMVM_HOME/config/genesis.json" 2>/dev/null || true)"
# A new chain is another chain-id OR another genesis: a relaunch can keep the
# chain-id (steemvm-2 was relaunched), and then only the genesis time differs.
NEW_CHAIN=0
if [ "$CURRENT_CHAIN" != "$TARGET_CHAIN" ] || [ "$CURRENT_GTIME" != "$TARGET_GTIME" ]; then NEW_CHAIN=1; fi
CURRENT_LABEL="${CURRENT_CHAIN:-?}" TARGET_LABEL="$TARGET_CHAIN"
if [ "$CURRENT_CHAIN" = "$TARGET_CHAIN" ]; then
  CURRENT_LABEL="$CURRENT_CHAIN (genesis ${CURRENT_GTIME:-?})" TARGET_LABEL="$TARGET_CHAIN (genesis $TARGET_GTIME)"
fi
if [ "$REJOIN_ONLY" = "1" ]; then
  [ "$NEW_CHAIN" = "0" ] || die "--rejoin-only: this node is still on '$CURRENT_LABEL', not '$TARGET_LABEL' — run ./reset.sh without it first."
  docker exec "$CONTAINER" true 2>/dev/null || die "--rejoin-only needs the node running: $COMPOSE up -d steemvm"
fi
[ "$REJOIN_ONLY" = "1" ] && [ "$NO_REJOIN" = "1" ] && die "--rejoin-only and --no-rejoin contradict each other."
REJOIN=0
if [ "$NEW_CHAIN" = "1" ] || [ "$REJOIN_ONLY" = "1" ]; then REJOIN=1; fi
[ "$NO_REJOIN" = "1" ] && REJOIN=0

# Which oracle runs here (read before anything is stopped).
if [ -z "$ORACLE_PROFILE" ]; then
  RUNNING="$(docker ps --format '{{.Names}}' 2>/dev/null || true)"
  if printf '%s\n' "$RUNNING" | grep -qx 'steemvm-oracle-go'; then ORACLE_PROFILE=go
  elif printf '%s\n' "$RUNNING" | grep -qx 'steemvm-oracle-python'; then ORACLE_PROFILE=python
  elif printf '%s\n' "$RUNNING" | grep -qx 'steemvm-oracle-js'; then ORACLE_PROFILE=js
  elif [ "$REJOIN" = "1" ]; then ORACLE_PROFILE=go   # a validator must run one
  fi
fi

if [ "$REJOIN_ONLY" = "0" ]; then
  # The seed must serve the chain this checkout's genesis describes.
  SEED_NET="$(curl -fsS -m 15 "${SEED_RPC%/}/status" 2>/dev/null | jq -r '.result.node_info.network // empty' 2>/dev/null || true)"
  [ -n "$SEED_NET" ] || die "can't reach $SEED_RPC — set --seed-rpc to a synced node's RPC. Nothing was touched."
  [ "$SEED_NET" = "$TARGET_CHAIN" ] || die "$SEED_RPC runs '$SEED_NET', but this checkout's genesis is '$TARGET_CHAIN' — run git pull, or fix --seed-rpc. Nothing was touched."

  # Fetch the trust anchor BEFORE touching anything: a wipe that can't resync
  # must be found out while the data is still intact.
  if [ "$USE_STATESYNC" = "1" ]; then
    fetch_statesync_trust || die "state-sync unavailable (chain below height $MIN_STATESYNC_HEIGHT, or $SEED_RPC unreachable) — refusing to wipe data that could not be resynced. Nothing was touched. Try --seed-rpc <live peer>."
    ok "State-sync trust anchor from $SEED_RPC: height $TRUST_HEIGHT."
  else
    # A replay from genesis can't cross a coordinated upgrade with one binary,
    # and steemvm-2 has had one (v0.0.7 at height 79664).
    die "--no-statesync: a replay from genesis can't cross the v0.0.7 upgrade (height 79664) — state-sync is the only way to resync. Nothing was touched."
  fi
fi

WANT_VER="$(sed -n 's/^[[:space:]]*VERSION[[:space:]]*:=[[:space:]]*\([0-9][^[:space:]]*\).*/\1/p' Makefile | head -1)"

# Repo-root .env: a move to a new chain (or a rejoin) runs THIS release's
# images, node and oracle alike. Other pins there (an old chain's tags) are
# overridden for this run and commented out after YES.
ROOT_ENV=".env"
ROOT_FIXES=()
if [ "$NEW_CHAIN" = "1" ] || [ "$REJOIN_ONLY" = "1" ]; then
  [ -n "$WANT_VER" ] || die "the Makefile has no VERSION. Nothing was touched."
  for pair in "STEEMVM_IMAGE=steemblazer/steemvmd:v$WANT_VER" \
              "ORACLE_GO_IMAGE=steemblazer/steemvm-oracle-go:v$WANT_VER" \
              "ORACLE_PYTHON_IMAGE=steemblazer/steemvm-oracle-python:v$WANT_VER" \
              "ORACLE_JS_IMAGE=steemblazer/steemvm-oracle-js:v$WANT_VER"; do
    v="${pair%%=*}" want="${pair#*=}"
    have="$(ENV_FILE="$ROOT_ENV" env_get "$v")"
    [ -n "$have" ] && [ "$have" != "$want" ] && ROOT_FIXES+=("$v|$v=$have (this release runs $want)")
    export "${v}=${want}"
  done
fi

if [ "$NEW_CHAIN" = "1" ]; then
  # The genesis must be the one the network actually runs (a stale checkout
  # would split this node off at block 1).
  SERVED="$(curl -fsS -m 60 "${SEED_RPC%/}/genesis" 2>/dev/null | jq -cS '.result.genesis | {chain_id, genesis_time, app_state}' 2>/dev/null || true)"
  LOCAL="$(tr -d '\r' < Instructions/genesis.json | jq -cS '{chain_id, genesis_time, app_state}')"
  [ -n "$SERVED" ] || die "could not fetch the genesis from $SEED_RPC. Nothing was touched."
  [ "$SERVED" = "$LOCAL" ] || die "Instructions/genesis.json differs from the genesis $SEED_RPC runs — run git pull, then retry. Nothing was touched."
  ok "Instructions/genesis.json matches the genesis $TARGET_CHAIN runs."

  EVM_CHAIN_ID="$(sed -n 's/^[[:space:]]*evm-chain-id[[:space:]]*=[[:space:]]*\([0-9][0-9]*\).*/\1/p' Instructions/app.toml.example | head -1)"
  [ -n "$EVM_CHAIN_ID" ] || die "Instructions/app.toml.example has no evm-chain-id."

  # The node image must be this release's (a new chain's genesis can't run on
  # an older binary).
  log "Pulling $STEEMVM_IMAGE…"
  $COMPOSE pull steemvm || warn "image pull failed — checking the cached image."
  GOT_VER="$(offline version 2>/dev/null | tr -d '\r' | tail -1 || true)"
  [ "$GOT_VER" = "$WANT_VER" ] \
    || die "$STEEMVM_IMAGE reports version '${GOT_VER:-?}', but this release is '$WANT_VER'. Nothing was touched."
  ok "Node image is v$GOT_VER."
fi

# ── oracle/.env review (new chain / rejoin): what can't be right any more ────
# Each problem becomes a "VAR|reason" entry in ENV_FIXES — commented out after
# YES (secrets are removed instead) — and each replacement a "VAR=value" in
# ENV_SETS, added after YES. Nothing else in the file is touched.
#   - a node URL override pointing away from this node (the old chain's RPC/
#     REST): the compose default http://steemvm:… is this node;
#   - a Steem RPC that doesn't serve this chain's Steem anchor (unreachable,
#     a dead domain, another network): replaced by working public nodes;
#   - an owner/posting key or password: only the active key belongs on the
#     server, and the svm.bank signer refuses to start next to them;
#   - ORACLE_GAS_PRICES far from this chain's floor (an old chain's value):
#     price-feed txs pay it, and they aren't fee-exempt;
#   - ORACLE_PRICE_SOURCE=cmc without a CoinMarketCap key (CoinGecko is keyless);
#   - an unreachable price-API base URL (the client's default is used).
ENV_FIXES=()
ENV_SETS=()
# Owner/posting keys and passwords are removed, not commented out: a secret
# must not stay on disk.
secret_var() {
  case "$1" in
    ORACLE_STEEM_OWNER_WIF|ORACLE_STEEM_OWNER|ORACLE_STEEM_POSTING_WIF|ORACLE_STEEM_MASTER_PASSWORD|ORACLE_STEEM_PASSWORD|OWNER|POSTING|MASTER_PASSWORD) return 0 ;;
  esac
  return 1
}
# Public Steem nodes to fall back on (all checked against the anchor first).
STEEM_FALLBACKS="https://api.steemit.com https://api.justyy.com https://steemd.steemworld.org https://api.steememory.com https://api.pennsif.net"
ANCHOR_BLOCK="$(tr -d '\r' < Instructions/genesis.json | jq -r '.app_state.steembridge.params.relayer_start_block // "0"')"
ANCHOR_ID="$(tr -d '\r' < Instructions/genesis.json | jq -r '.app_state.steembridge.params.relayer_start_block_id // empty')"
# steem_ok <url>: it answers get_block for this chain's Steem anchor with the
# anchor's ID — reachable, the API the oracle uses, the right network.
steem_ok() {
  local id
  id="$(curl -fsS -m 15 "$1" -H 'Content-Type: application/json' \
          -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"condenser_api.get_block\",\"params\":[$ANCHOR_BLOCK]}" 2>/dev/null \
        | jq -r '.result.block_id // empty' 2>/dev/null || true)"
  [ -n "$ANCHOR_ID" ] && [ "$id" = "$ANCHOR_ID" ]
}
# A host without a dot (a compose service, localhost) is only reachable from
# the oracle's container, not from here: it is trusted as it is.
steem_unverifiable() { case "$1" in *://*.*) return 1 ;; *) return 0 ;; esac; }

STEEM_RPC_ARG="${STEEM_RPC:-}"
STEEM_RPC="" STEEM_RPCS=""
if [ "$NEW_CHAIN" = "1" ] || [ "$REJOIN_ONLY" = "1" ]; then
  log "Checking the Steem and API URLs in oracle/.env…"
  for v in ORACLE_NODE_RPC ORACLE_NODE_REST; do
    u="$(env_get "$v")"
    case "$u" in
      ""|http://steemvm:*|http://steemvm-node:*) ;;
      *) ENV_FIXES+=("$v|$v=$u points away from this node — the oracle uses http://steemvm:…") ;;
    esac
  done

  # The main Steem RPC: the configured one if it works, else STEEM_RPC= from
  # the command line, else the first working fallback.
  u="$(env_get ORACLE_STEEM_RPC)"
  if [ -n "$u" ]; then
    if steem_unverifiable "$u" || steem_ok "$u"; then STEEM_RPC="$u"
    else ENV_FIXES+=("ORACLE_STEEM_RPC|ORACLE_STEEM_RPC=$u doesn't serve $TARGET_CHAIN's Steem anchor (unreachable, or another network)"); fi
  fi
  if [ -z "$STEEM_RPC" ]; then
    for c in $STEEM_RPC_ARG $STEEM_FALLBACKS; do
      if steem_ok "$c"; then STEEM_RPC="$c"; break; fi
    done
    [ -n "$STEEM_RPC" ] || die "no Steem RPC answers for block $ANCHOR_BLOCK — check this server's internet access. Nothing was touched."
    ENV_SETS+=("ORACLE_STEEM_RPC=$STEEM_RPC")
  fi

  # The signer's node list: keep the working entries, top up to two.
  u="$(env_get ORACLE_STEEM_RPCS)"
  n=0
  for c in $(printf '%s' "$u" | tr ',' ' '); do
    if steem_unverifiable "$c" || steem_ok "$c"; then STEEM_RPCS="${STEEM_RPCS:+$STEEM_RPCS,}$c"; n=$((n + 1)); fi
  done
  for c in $STEEM_RPC $STEEM_FALLBACKS; do
    [ "$n" -ge 2 ] && break
    case ",$STEEM_RPCS," in *",$c,"*) continue ;; esac
    if steem_ok "$c"; then STEEM_RPCS="${STEEM_RPCS:+$STEEM_RPCS,}$c"; n=$((n + 1)); fi
  done
  if [ "$STEEM_RPCS" != "$u" ]; then
    [ -n "$u" ] && ENV_FIXES+=("ORACLE_STEEM_RPCS|ORACLE_STEEM_RPCS=$u (not all of it serves $TARGET_CHAIN's Steem anchor, or fewer than two nodes)")
    ENV_SETS+=("ORACLE_STEEM_RPCS=$STEEM_RPCS")
  fi

  for v in ORACLE_STEEM_OWNER_WIF ORACLE_STEEM_OWNER ORACLE_STEEM_POSTING_WIF ORACLE_STEEM_MASTER_PASSWORD ORACLE_STEEM_PASSWORD OWNER POSTING MASTER_PASSWORD; do
    [ -n "$(env_get "$v")" ] && ENV_FIXES+=("$v|$v (value not shown) — only the active key belongs on this server")
  done
  CHAIN_GAS="$(sed -n 's/^minimum-gas-prices = "\([0-9]*\)asteem".*/\1/p' Instructions/app.toml.example | head -1)"
  GAS="$(env_get ORACLE_GAS_PRICES)"
  if [ -n "$GAS" ] && [ -n "$CHAIN_GAS" ] \
     && ! awk -v g="${GAS%asteem}" -v m="$CHAIN_GAS" 'BEGIN{exit !(g+0 >= m+0 && g+0 <= 10*m)}'; then
    ENV_FIXES+=("ORACLE_GAS_PRICES|ORACLE_GAS_PRICES=$GAS is not near $TARGET_CHAIN's ${CHAIN_GAS}asteem floor — the default is that floor")
  fi
  if [ "$(env_get ORACLE_PRICE_SOURCE)" = "cmc" ] && [ -z "$(env_get ORACLE_CMC_API_KEY)" ]; then
    ENV_FIXES+=("ORACLE_PRICE_SOURCE|ORACLE_PRICE_SOURCE=cmc has no ORACLE_CMC_API_KEY — CoinGecko (keyless) instead")
  fi
  for v in ORACLE_COINGECKO_BASE_URL ORACLE_CMC_BASE_URL; do
    u="$(env_get "$v")"
    [ -n "$u" ] || continue
    code="$(curl -sS -o /dev/null -m 15 -w '%{http_code}' "$u" 2>/dev/null || true)"
    [ "${code:-000}" = "000" ] && ENV_FIXES+=("$v|$v=$u is unreachable — the client's default is used")
  done
fi
STEEM_RPC="${STEEM_RPC:-${STEEM_RPC_ARG:-https://api.steemit.com}}"
STEEM_RPCS="${STEEM_RPCS:-$STEEM_RPC}"

# ── rejoin pre-flight: everything it needs, asked/checked before any change ─
if [ "$REJOIN" = "1" ]; then
  if [ -z "$STEEM_USERNAME" ]; then
    STEEM_USERNAME="$(sed -n 's/^moniker = "\(.*\)"/\1/p' "$CONFIG_TOML" | head -1)"
  fi
  valid_steem_name "$STEEM_USERNAME" \
    || die "the moniker '$STEEM_USERNAME' in $CONFIG_TOML is not a Steem account name — set STEEM_USERNAME=<your Steem username>. Nothing was touched."

  ACCT="$(steem condenser_api.get_accounts "[[\"$STEEM_USERNAME\"]]" | jq -c '.result[0] // empty' 2>/dev/null || true)"
  [ -n "$ACCT" ] || die "Steem account @$STEEM_USERNAME not found (via $STEEM_RPC). Nothing was touched."
  single_key() { printf '%s' "$ACCT" | jq -e ".$1.weight_threshold == 1 and (.$1.key_auths | length) == 1" >/dev/null; }
  if single_key owner && single_key active && single_key posting; then
    OWNER_KEY="$(printf '%s' "$ACCT" | jq -r '.owner.key_auths[0][0]')"
    ACTIVE_KEY="$(printf '%s' "$ACCT" | jq -r '.active.key_auths[0][0]')"
    POSTING_KEY="$(printf '%s' "$ACCT" | jq -r '.posting.key_auths[0][0]')"
  else
    warn "@$STEEM_USERNAME has multi-key authorities — enter the public keys for your validator's details."
    read -rp "Steem OWNER public key   (STM...): " OWNER_KEY < /dev/tty
    read -rp "Steem ACTIVE public key  (STM...): " ACTIVE_KEY < /dev/tty
    read -rp "Steem POSTING public key (STM...): " POSTING_KEY < /dev/tty
  fi
  [ -n "$OWNER_KEY" ] && [ -n "$ACTIVE_KEY" ] && [ -n "$POSTING_KEY" ] || die "owner, active and posting public keys are all required."
  DETAILS="owner=$OWNER_KEY;active=$ACTIVE_KEY;posting=$POSTING_KEY"

  # The account key: VALIDATOR_KEY, else the key named after the moniker, else
  # the only key. Found by the keyring's *.info file names, so a
  # password-protected keyring needs no passphrase just to find it.
  case "$KEYRING" in
    test) KDIR="$STEEMVM_HOME/keyring-test" ;;
    file) KDIR="$STEEMVM_HOME/keyring-file" ;;
    *)    KDIR="$STEEMVM_HOME" ;;   # os: in a container it is files in the home itself
  esac
  KEY_NAMES="$(find "$KDIR" -maxdepth 1 -name '*.info' -printf '%f\n' 2>/dev/null | sed 's/\.info$//' | sort || true)"
  if [ -z "$VALIDATOR_KEY" ]; then
    if printf '%s\n' "$KEY_NAMES" | grep -qxF "$STEEM_USERNAME"; then VALIDATOR_KEY="$STEEM_USERNAME"
    elif [ "$(printf '%s\n' "$KEY_NAMES" | grep -c .)" = "1" ]; then VALIDATOR_KEY="$KEY_NAMES"; fi
  fi
  [ -n "$VALIDATOR_KEY" ] || die "can't tell which key is your validator's (keyring-backend '$KEYRING', keys: $(printf '%s' "$KEY_NAMES" | tr '\n' ' ')) — set VALIDATOR_KEY=<name>, and KEYRING=<backend> if that one is wrong. Nothing was touched."

  if [ "$KEYRING" != "test" ]; then
    # Exporting from a password-protected keyring asks an extra question, so
    # the oracle's key must already be in oracle/.env.
    [ -n "$(env_get ORACLE_PRIVATE_KEY)$(env_get ORACLE_MNEMONIC)" ] \
      || die "oracle/.env has no ORACLE_PRIVATE_KEY. Export it (docker exec -it $CONTAINER $BIN keys unsafe-export-eth-key $VALIDATOR_KEY --keyring-backend $KEYRING --home $HOME_DIR), put ORACLE_PRIVATE_KEY=<hex> in oracle/.env, then retry. Nothing was touched."
    log "Your keyring (backend '$KEYRING') is password-protected."
    read -rsp "Keyring passphrase (what 'steemvmd keys list' asks for): " KEYRING_PASS < /dev/tty || die "no passphrase entered."
    echo
  fi
  ADDR="$(kcmd keys show "$VALIDATOR_KEY" -a 2>/dev/null | tr -d '\r' | tail -1 || true)"
  [[ "$ADDR" == steem1* ]] || die "could not read key '$VALIDATOR_KEY' from the '$KEYRING' keyring (wrong passphrase?). Nothing was touched."
  VALOPER="$(offline debug addr "$ADDR" 2>/dev/null | tr -d '\r' | sed -n 's/^Bech32 Val: //p')"
  [ -n "$VALOPER" ] || die "could not derive the operator address of $ADDR. Nothing was touched."
  ok "Validator key '$VALIDATOR_KEY': $ADDR"

  # The oracle must sign as this same account, or it never acts for the validator.
  ORACLE_KEY="$(env_get ORACLE_PRIVATE_KEY)"
  if [ -n "$ORACLE_KEY" ] && [ "$KEYRING" = "test" ]; then
    KEYRING_HEX="$(kcmd keys unsafe-export-eth-key "$VALIDATOR_KEY" 2>/dev/null | tr -d '\r' | tail -1 | tr -d '[:space:]' || true)"
    norm() { printf '%s' "${1#0x}" | tr 'A-F' 'a-f'; }
    if [ -n "$KEYRING_HEX" ] && [ "$(norm "$ORACLE_KEY")" != "$(norm "$KEYRING_HEX")" ]; then
      die "oracle/.env's ORACLE_PRIVATE_KEY is not the key of '$VALIDATOR_KEY' ($ADDR) — fix oracle/.env or set VALIDATOR_KEY. Nothing was touched."
    fi
    unset KEYRING_HEX
  fi
  unset ORACLE_KEY

  # The Steem ACTIVE key: signs the 0.001 STEEM registration, and the oracle's
  # svm.bank signer uses it. Shown as typed so a bad paste is visible, then
  # checked against @user's active authority before anything is sent.
  ACTIVE_WIF="$(env_get ORACLE_STEEM_ACTIVE_WIF)"
  ACTIVE_WIF_NEW=0
  if [ -z "$ACTIVE_WIF" ]; then
    echo
    log "Your Steem ACTIVE private key for @$STEEM_USERNAME (starts with 5)."
    log "It pays the 0.001 STEEM name registration and is saved to oracle/.env for your oracle's svm.bank signer."
    log "Never the owner key, posting key or master password."
    read -rp "Steem active key: " ACTIVE_WIF < /dev/tty || die "no active key entered."
    ACTIVE_WIF="$(printf '%s' "$ACTIVE_WIF" | tr -d '[:space:]')"
    ACTIVE_WIF_NEW=1
  fi
  [[ "$ACTIVE_WIF" =~ ^5[1-9A-HJ-NP-Za-km-z]{50}$ ]] || die "that is not a Steem private key (51 base58 characters starting with 5). Nothing was touched."
  log "Checking the key against @$STEEM_USERNAME's active authority…"
  ACTIVE_PUB="$(steem_helper check "$STEEM_USERNAME")" || die "the Steem active key was rejected (see above). Nothing was touched."
  ok "Active key matches @$STEEM_USERNAME ($ACTIVE_PUB)."
fi

# ── confirm ──────────────────────────────────────────────────────────────────
echo
if [ "$REJOIN_ONLY" = "0" ]; then
  if [ "$NEW_CHAIN" = "1" ]; then
    log "This moves this node from '$CURRENT_LABEL' to '$TARGET_LABEL':"
    log "  wipes local chain data and the old chain's peers + cosmovisor upgrade slots,"
    log "  installs the $TARGET_CHAIN genesis, chain-id and EVM chain-id $EVM_CHAIN_ID, and state-syncs."
  else
    log "This wipes local chain data (blocks/state/tx-index) under $STEEMVM_HOME and"
    log "resyncs it via state-sync."
  fi
  log "Kept: priv_validator_key.json, node_key.json, your keyring and oracle/.env."
fi
if [ "$REJOIN" = "1" ]; then
  log "Then it rejoins $TARGET_CHAIN as validator '$STEEM_USERNAME' ($ADDR):"
  log "  sends 0.001 STEEM from @$STEEM_USERNAME to svm.bank (memo: svm-register $ADDR) if not yet registered,"
  log "  claims faucet coins, waits for $(steem_atoms "$STAKE_AMOUNT") STEEM, and creates the validator."
fi
for f in ${ROOT_FIXES[@]+"${ROOT_FIXES[@]}"}; do
  log ".env: comments out ${f#*|}."
done
for f in ${ENV_FIXES[@]+"${ENV_FIXES[@]}"}; do
  if secret_var "${f%%|*}"; then log "oracle/.env: removes ${f#*|}."
  else log "oracle/.env: comments out ${f#*|}."; fi
done
for p in ${ENV_SETS[@]+"${ENV_SETS[@]}"}; do
  log "oracle/.env: sets $p."
done
if [ "$NEW_CHAIN" = "1" ] || [ "$REJOIN_ONLY" = "1" ]; then
  case "$ORACLE_PROFILE" in
    go) OIMG="$ORACLE_GO_IMAGE" ;; python) OIMG="$ORACLE_PYTHON_IMAGE" ;; js) OIMG="$ORACLE_JS_IMAGE" ;; *) OIMG="" ;;
  esac
  log "Images: $STEEMVM_IMAGE${OIMG:+ and $OIMG}."
fi
[ -n "$ORACLE_PROFILE" ] && log "Finally it restarts the $ORACLE_PROFILE oracle on its published image."
[ "$REJOIN_ONLY" = "0" ] && warn "Your node will be out of sync until it catches up."
read -rp "Type YES to confirm: " CONFIRM < /dev/tty
[ "$CONFIRM" = "YES" ] || die "reset cancelled — nothing was touched."

for f in ${ROOT_FIXES[@]+"${ROOT_FIXES[@]}"}; do
  v="${f%%|*}"
  sed -i.bak -E "s/($(env_line "$v"))/# (reset.sh: this release runs v$WANT_VER) \1/" "$ROOT_ENV" && rm -f "$ROOT_ENV.bak"
  ok ".env: commented out $v."
done
for f in ${ENV_FIXES[@]+"${ENV_FIXES[@]}"}; do
  v="${f%%|*}"
  if secret_var "$v"; then
    sed -i.bak -E "/$(env_line "$v")/d" "$ENV_FILE" && rm -f "$ENV_FILE.bak"
    ok "oracle/.env: removed $v."
  else
    sed -i.bak -E "s/($(env_line "$v"))/# (reset.sh: not for $TARGET_CHAIN) \1/" "$ENV_FILE" && rm -f "$ENV_FILE.bak"
    ok "oracle/.env: commented out $v."
  fi
done

# The replacements, and what every oracle needs, for a file that's missing
# them (e.g. recreated by hand with only the key): it exits without a Steem
# RPC, and the default price source (CoinMarketCap) needs an API key.
if [ -f "$ENV_FILE" ] || [ "$REJOIN" = "1" ]; then
  [ -f "$ENV_FILE" ] || touch "$ENV_FILE"
  chmod 600 "$ENV_FILE"
  for p in ${ENV_SETS[@]+"${ENV_SETS[@]}"}; do
    env_set_if_missing "${p%%=*}" "${p#*=}"
  done
  env_set_if_missing ORACLE_START_BLOCK latest
  env_set_if_missing ORACLE_STEEM_RPC "$STEEM_RPC"
  if [ -z "$(env_get ORACLE_PRICE_SOURCE)" ] && [ -z "$(env_get ORACLE_CMC_API_KEY)" ]; then
    env_set_if_missing ORACLE_PRICE_SOURCE coingecko
  fi
  env_set_if_missing ORACLE_SBD_SYMBOL SBD
fi

# Save the active key first, so a later --rejoin-only never asks again.
if [ "$REJOIN" = "1" ]; then
  if [ -z "$(env_get ORACLE_PRIVATE_KEY)" ] && [ -z "$(env_get ORACLE_MNEMONIC)" ] && [ "$KEYRING" = "test" ]; then
    PRIVKEY="$(kcmd keys unsafe-export-eth-key "$VALIDATOR_KEY" 2>/dev/null | tr -d '\r' | tail -1 | tr -d '[:space:]' || true)"
    [ -n "$PRIVKEY" ] && env_set_if_missing ORACLE_PRIVATE_KEY "$PRIVKEY"
    unset PRIVKEY
  fi
  env_set_if_missing ORACLE_STEEM_ACTIVE_WIF "$ACTIVE_WIF"
  env_set_if_missing ORACLE_MULTISIG_ENABLED true
  env_set_if_missing ORACLE_STEEM_RPCS "$STEEM_RPCS"
  [ "$ACTIVE_WIF_NEW" = "1" ] && ok "Steem active key saved to oracle/.env (mode 600)."
fi

if [ "$REJOIN_ONLY" = "0" ]; then
  # ── 1. stop the node and oracle (the bind mount and oracle-data volume stay) ─
  # All profiles, so the oracle is stopped too: nothing else may sign with the
  # validator's key while it rejoins.
  log "Stopping the node and oracle…"
  $COMPOSE --profile go --profile python --profile js down

  # ── 2. fetch a fresh peer list before wiping data (trust anchor fetched above) ─
  if fetch_seed_peer; then
    ok "Live peer info fetched: $SEED_PEER"
  else
    warn "could not fetch live peer info from $SEED_RPC — keeping config.toml's existing persistent_peers."
  fi

  # ── 3. wipe chain data, keys untouched ─────────────────────────────────────
  log "Running unsafe-reset-all…"
  RESET_ARGS="comet unsafe-reset-all --home $HOME_DIR"
  # A new chain's peers are all new: the old address book is dropped.
  [ "$KEEP_ADDRBOOK" = "1" ] && [ "$NEW_CHAIN" = "0" ] && RESET_ARGS="$RESET_ARGS --keep-addr-book"
  # -T: no pseudo-TTY, so this also works over a non-interactive SSH session.
  # --entrypoint overrides docker-entrypoint.sh's full node-bootstrap logic —
  # this runs the CLI subcommand directly against the bind-mounted home and
  # nothing else.
  # shellcheck disable=SC2086
  $COMPOSE run --rm -T --entrypoint "$BIN" steemvm $RESET_ARGS
  ok "Chain data wiped."

  [ -f "$KEY_FILE" ] || die "priv_validator_key.json is gone after reset — this should never happen. STOP and investigate before restarting; do not create a new key."
  ok "priv_validator_key.json still present — validator identity intact."

  # The node will state-sync to the chain tip, so cosmovisor must run the
  # image's binary (the chain's current version) — not whatever `current`
  # pointed at before. Pointing `current` at genesis makes docker-entrypoint.sh
  # refresh genesis/bin from the image on start. A new chain also drops the old
  # chain's upgrade slots (the entrypoint re-stages this image's own).
  CLEAR_UPGRADES=""
  [ "$NEW_CHAIN" = "1" ] && CLEAR_UPGRADES="rm -rf $HOME_DIR/cosmovisor/upgrades;"
  $COMPOSE run --rm -T --entrypoint sh steemvm -c \
    "[ -d $HOME_DIR/cosmovisor ] && { $CLEAR_UPGRADES ln -sfn genesis $HOME_DIR/cosmovisor/current; } || true" \
    || die "could not point cosmovisor at the image's binary."
  ok "cosmovisor will run the image's binary."

  # ── 4. new chain: genesis, chain-id, EVM chain-id, no old seeds ────────────
  if [ "$NEW_CHAIN" = "1" ]; then
    tr -d '\r' < Instructions/genesis.json > "$STEEMVM_HOME/config/genesis.json"
    if [ -f "$CLIENT_TOML" ]; then
      sed -i.bak "s|^chain-id = .*|chain-id = \"$TARGET_CHAIN\"|" "$CLIENT_TOML" && rm -f "$CLIENT_TOML.bak"
    fi
    set_evm_chain_id "$APP_TOML" "$EVM_CHAIN_ID" || die "$APP_TOML has no [evm] section — add: [evm] evm-chain-id = $EVM_CHAIN_ID"
    set_evm_chain_id Instructions/app.toml "$EVM_CHAIN_ID" || true
    sed -i.bak 's|^seeds = .*|seeds = ""|' "$CONFIG_TOML" && rm -f "$CONFIG_TOML.bak"
    apply_seed_peer Instructions/config.toml || true
    ok "Installed the $TARGET_CHAIN genesis, chain-id and EVM chain-id $EVM_CHAIN_ID."
  fi

  # ── 5. patch config.toml: fresh peers, state-sync (or explicit disable) ───
  if apply_seed_peer "$CONFIG_TOML"; then
    warn "persistent_peers set from $SEED_RPC: $SEED_PEER"
  fi
  if [ "$USE_STATESYNC" = "1" ] && enable_statesync "$CONFIG_TOML"; then
    ok "State-sync enabled in $CONFIG_TOML — will fast-bootstrap from a snapshot."
  else
    disable_statesync "$CONFIG_TOML" || true
    log "State-sync disabled — will do a full replay from genesis."
  fi

  # ── 6. start the node ───────────────────────────────────────────────────────
  log "Starting the node…"
  $COMPOSE up -d steemvm
fi

wait_synced() {
  local deadline=$(( $(date +%s) + START_TIMEOUT )) h
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
          return 0
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
}

log "Waiting for the node to catch up (timeout ${START_TIMEOUT}s)…"
wait_synced
ok "Node is caught up (height $height)."

# The EVM JSON-RPC indexer (cosmos/evm, --json-rpc.enable-indexer) starts
# while state-sync is still running, asks for block 1 — which a state-synced
# node never has — and then stops for good (it never retries a failed fetch),
# so eth_getTransactionReceipt & co. would find nothing on this node. One
# restart once synced makes it start at the synced height instead.
if [ "$REJOIN_ONLY" = "0" ] && [ "$USE_STATESYNC" = "1" ]; then
  log "Restarting the node once so its EVM indexer starts at the synced height…"
  docker restart "$CONTAINER" >/dev/null
  wait_synced
  ok "Node restarted and caught up (height $height)."
fi

# ── 7. confirm keys are all still there ────────────────────────────────────
if [ "$KEYRING" != "test" ] && [ -z "$KEYRING_PASS" ]; then
  log "Keyring (backend '$KEYRING') not opened — check it with: docker exec -it $CONTAINER $BIN keys list --home $HOME_DIR"
elif N="$(kcmd keys list --output json 2>/dev/null | tr -d '\r' | grep -m1 '^\[' | jq 'length' 2>/dev/null)" && [ -n "$N" ]; then
  ok "Keyring intact — $N key(s) found under keyring-backend '$KEYRING'."
else
  warn "could not list keys with keyring-backend '$KEYRING' — check manually: docker exec -it $CONTAINER $BIN keys list --keyring-backend $KEYRING --home $HOME_DIR"
fi

# ── 8. rejoin: name registration -> stake -> create-validator ──────────────
resolve_name() { kf query steembridge resolve-name "$STEEM_USERNAME" --output json 2>/dev/null | jq -r '.name.address // empty' 2>/dev/null || true; }
awaiting_id() { kf query steembridge awaiting-name-registrations-by-destination "$ADDR" --output json 2>/dev/null | jq -r '.registrations[0].id // empty' 2>/dev/null || true; }

register_name() {
  local resolved reg_id params min anchor amount memo txid poll_n=0
  resolved="$(resolve_name)"
  if [ "$resolved" = "$ADDR" ]; then ok "@$STEEM_USERNAME is already ACTIVE for $ADDR."; return 0; fi
  [ -z "$resolved" ] || die "@$STEEM_USERNAME is linked to $resolved on $TARGET_CHAIN, not to $ADDR."

  reg_id="$(awaiting_id)"
  if [ -z "$reg_id" ]; then
    params="$(kf query steembridge params --output json 2>/dev/null || true)"
    min="$(printf '%s' "$params" | jq -r '.params.name_registration_min_millisteem // "1"' 2>/dev/null || echo 1)"
    anchor="$(printf '%s' "$params" | jq -r '.params.relayer_start_block // "0"' 2>/dev/null || echo 0)"
    [[ "$min" =~ ^[0-9]+$ ]] && [ "$min" -ge 1 ] || min=1
    [[ "$anchor" =~ ^[0-9]+$ ]] || anchor=0
    amount="$(printf '%d.%03d STEEM' $((min / 1000)) $((min % 1000)))"
    memo="svm-register $ADDR"
    # A registration sent on an earlier chain (before this chain's Steem
    # anchor) doesn't count here, so it is never mistaken for this one.
    if steem_memo_sent "$STEEM_USERNAME" "$memo" "$anchor"; then
      ok "The registration transfer was already sent — waiting for it to be attested."
    else
      log "Sending $amount from @$STEEM_USERNAME to svm.bank (memo: $memo)…"
      txid="$(steem_helper transfer "$STEEM_USERNAME" svm.bank "$amount" "$memo")" \
        || die "the registration transfer failed (see above). Send it by hand — $amount to svm.bank, memo EXACTLY: $memo — then: ./reset.sh --rejoin-only"
      ok "Sent (Steem transaction $txid)."
    fi
    log "Waiting for the validators to attest it (timeout ${NAME_TIMEOUT}s)…"
    local deadline=$(( $(date +%s) + NAME_TIMEOUT ))
    while :; do
      poll_n=$((poll_n + 1))
      reg_id="$(awaiting_id)"
      [ -n "$reg_id" ] && break
      if [ "$(resolve_name)" = "$ADDR" ]; then ok "@$STEEM_USERNAME is ACTIVE for $ADDR."; return 0; fi
      [ $((poll_n % 4)) -eq 0 ] && log "still waiting ($(( $(date +%s) - deadline + NAME_TIMEOUT ))s)…"
      [ "$(date +%s)" -ge "$deadline" ] && die "no attested registration for $ADDR within ${NAME_TIMEOUT}s. Once it shows up, run: ./reset.sh --rejoin-only"
      sleep 15
    done
  fi

  ok "Registration #$reg_id is awaiting confirmation — confirming…"
  local attempt out
  for attempt in 1 2 3; do
    if out="$(kcmd tx steembridge confirm-name "$reg_id" --from "$VALIDATOR_KEY" --chain-id "$TARGET_CHAIN" \
          --gas auto --gas-adjustment 1.5 --gas-prices "$GAS_PRICES" -y --output json 2>&1)" \
       && ! printf '%s' "$out" | grep -qE '"code": ?[1-9]|does not exist|unauthorized'; then
      break
    fi
    warn "confirm-name attempt $attempt failed: $(printf '%s' "$out" | tail -1)"
    [ "$attempt" = "3" ] && die "confirm-name failed. Retry: ./reset.sh --rejoin-only"
    sleep 10
  done
  for _ in 1 2 3 4 5 6; do
    sleep 5
    if [ "$(resolve_name)" = "$ADDR" ]; then ok "@$STEEM_USERNAME confirmed ACTIVE for $ADDR."; return 0; fi
  done
  die "confirm-name was broadcast but @$STEEM_USERNAME isn't ACTIVE yet. Check: docker exec $CONTAINER $BIN query steembridge resolve-name $STEEM_USERNAME — then ./reset.sh --rejoin-only"
}

# fit_stake <balance>: STAKE_AMOUNT, or less so 1 STEEM stays liquid for the
# oracle's price-feed fees (those txs are not fee-exempt). Whole STEEM, so the
# amount is exact despite awk's floating point.
fit_stake() {
  local whole
  whole="$(awk -v b="$1" -v s="${STAKE_AMOUNT%asteem}" 'BEGIN{x = b - 1e18; if (x > s) x = s; printf "%d", x / 1e18}')"
  [ "$whole" -ge 1 ] 2>/dev/null || die "the balance ($(steem_atoms "$1") STEEM) leaves nothing to stake after 1 STEEM for fees."
  STAKE_AMOUNT="${whole}000000000000000000asteem"
}

wait_for_stake() {
  local need bal poll_n=0 out
  need="${STAKE_AMOUNT%asteem}"
  balance() { kf query bank balances "$ADDR" --output json 2>/dev/null | jq -r '.balances[]? | select(.denom=="asteem") | .amount' 2>/dev/null || true; }
  covered() { awk -v b="$1" -v n="$need" 'BEGIN{exit !(b+0 >= n+0)}'; }
  bal="$(balance)"; bal="${bal:-0}"
  if ! covered "$bal"; then
    if [ -n "$FAUCET_URL" ]; then
      log "Claiming faucet coins for $ADDR…"
      out="$(curl -fsS -m 60 -X POST "$FAUCET_URL" -H 'Content-Type: application/json' -d "{\"address\":\"$ADDR\"}" 2>&1)" || true
      if printf '%s' "$out" | jq -e '.success == true' >/dev/null 2>&1; then
        ok "Faucet sent $(printf '%s' "$out" | jq -r '.amount') STEEM (tx $(printf '%s' "$out" | jq -r '.txHash'))."
      else
        warn "faucet claim failed: $(printf '%s' "$out" | head -c 300)"
      fi
    fi
    echo
    warn "$ADDR needs $(steem_atoms "$need") STEEM on $TARGET_CHAIN for the stake (it holds $(steem_atoms "$bal"))."
    echo "   If the faucet didn't cover it, bridge STEEM from Steem: send it from @$STEEM_USERNAME to"
    echo "   svm.bank with memo EXACTLY:  $ADDR"
    echo "   or ask an existing $TARGET_CHAIN validator to send it. (Smaller stake: STAKE_AMOUNT=<n>asteem ./reset.sh --rejoin-only)"
    log "Waiting for the balance (timeout ${FUND_TIMEOUT}s)…"
    local deadline=$(( $(date +%s) + FUND_TIMEOUT ))
    while :; do
      sleep 15
      poll_n=$((poll_n + 1))
      bal="$(balance)"; bal="${bal:-0}"
      covered "$bal" && break
      [ $((poll_n % 8)) -eq 0 ] && log "still waiting — $(steem_atoms "$bal") of $(steem_atoms "$need") STEEM"
      [ "$(date +%s)" -ge "$deadline" ] && die "still $(steem_atoms "$bal") of $(steem_atoms "$need") STEEM after ${FUND_TIMEOUT}s. Once funded, run: ./reset.sh --rejoin-only"
    done
  fi
  fit_stake "$bal"
  ok "Funded: $(steem_atoms "$bal") STEEM — staking $(steem_atoms "$STAKE_AMOUNT"), the rest stays liquid for fees."
}

create_validator() {
  local pubkey out
  pubkey="$(node comet show-validator --home "$HOME_DIR" | tr -d '\r')"
  [ -n "$pubkey" ] || die "could not read the node's consensus pubkey."
  cat > "$VALIDATOR_JSON" <<EOF
{
  "pubkey": $pubkey,
  "amount": "$STAKE_AMOUNT",
  "moniker": "$STEEM_USERNAME",
  "identity": "",
  "website": "",
  "security": "",
  "details": "$DETAILS",
  "commission-rate": "$COMMISSION_RATE",
  "commission-max-rate": "$COMMISSION_MAX_RATE",
  "commission-max-change-rate": "$COMMISSION_MAX_CHANGE_RATE",
  "min-self-delegation": "$MIN_SELF_DELEGATION"
}
EOF
  log "Creating validator '$STEEM_USERNAME' with $(steem_atoms "$STAKE_AMOUNT") STEEM…"
  out="$(kcmd tx staking create-validator "/workspace/$VALIDATOR_JSON" --from "$VALIDATOR_KEY" --chain-id "$TARGET_CHAIN" \
        --gas auto --gas-adjustment 1.5 --gas-prices "$GAS_PRICES" -y --output json 2>&1)" \
    || die "create-validator failed: $(printf '%s' "$out" | tail -3)"
  printf '%s' "$out" | grep -qE '"code": ?[1-9]' && die "create-validator was rejected: $(printf '%s' "$out" | tail -3)"
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    sleep 6
    if kf query staking validator "$VALOPER" --output json >/dev/null 2>&1; then
      ok "Validator $VALOPER created."
      return 0
    fi
  done
  die "create-validator was broadcast but $VALOPER doesn't exist yet. Check: docker exec $CONTAINER $BIN query staking validator $VALOPER"
}

if [ "$REJOIN" = "1" ]; then
  if kf query staking validator "$VALOPER" --output json >/dev/null 2>&1; then
    ok "$VALOPER is already a validator on $TARGET_CHAIN."
  else
    register_name
    wait_for_stake
    create_validator
  fi
fi
unset ACTIVE_WIF

# ── 9. restart the oracle on its published image (same oracle/.env) ─────────
if [ -n "$ORACLE_PROFILE" ]; then
  svc="oracle-$ORACLE_PROFILE"
  if [ ! -f "$ENV_FILE" ]; then
    warn "no oracle/.env — not starting $svc. Set it up (oracle/.env.example), then: $COMPOSE --profile $ORACLE_PROFILE up -d $svc"
  else
    log "Starting the $ORACLE_PROFILE oracle on its published image…"
    $COMPOSE --profile "$ORACLE_PROFILE" pull "$svc" || warn "could not pull the $svc image — using the cached one."
    $COMPOSE --profile "$ORACLE_PROFILE" up -d --no-deps "$svc" \
      || warn "could not start $svc — fix the error above, then: $COMPOSE --profile $ORACLE_PROFILE up -d --no-deps $svc"
  fi
fi

echo
ok "Done. Check your validator is bonded and signing:"
if [ -n "${VALOPER:-}" ]; then
  echo "    docker exec $CONTAINER $BIN query staking validator $VALOPER --home $HOME_DIR"
else
  echo "    docker exec $CONTAINER $BIN query staking validators --home $HOME_DIR"
fi
[ -n "$ORACLE_PROFILE" ] && echo "    $COMPOSE logs -f oracle-$ORACLE_PROFILE"
exit 0
