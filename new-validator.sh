#!/usr/bin/env bash
#
# new-validator.sh — interactive validator + oracle setup.
#
# Steps: username -> Steem pubkeys -> CMC key -> fetch genesis -> start node
# -> new SteemVM key -> Steem name registration -> faucet + stake -> create
# validator -> start oracle.
#
# Usage: ./new-validator.sh [--cleanup | --cleanup-full | --fix-config]
#   --cleanup       wipes generated files + `docker compose down` (keeps node home/volumes)
#   --cleanup-full  --cleanup plus wipes $STEEMVM_HOME and volumes (irreversible)
#   --fix-config    patches known-stale config values (e.g. mempool.type) on an
#                    already-running validator, then restarts the node. Does NOT
#                    touch keys/validator.json/mnemonic. Safe to run any time.
#
# Overridable via env: STAKE_AMOUNT, COMMISSION_RATE, COMMISSION_MAX_RATE,
# COMMISSION_MAX_CHANGE_RATE, MIN_SELF_DELEGATION, GAS_PRICES, START_TIMEOUT,
# FUND_TIMEOUT, NAME_TIMEOUT, ORACLE_PROFILE, FAUCET_URL

# re-exec under bash if run as `sh new-validator.sh` (needs set -o pipefail)
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
KEYRING="${KEYRING:-test}"    # unencrypted, no prompts. Set KEYRING=file/os for a password-protected keyring.
KEY_TYPE="${KEY_TYPE:-eth_secp256k1}"

STAKE_AMOUNT="${STAKE_AMOUNT:-4000000000000000000000asteem}"   # 4000 STEEM
COMMISSION_RATE="${COMMISSION_RATE:-0.10}"
COMMISSION_MAX_RATE="${COMMISSION_MAX_RATE:-0.20}"
COMMISSION_MAX_CHANGE_RATE="${COMMISSION_MAX_CHANGE_RATE:-0.01}"
MIN_SELF_DELEGATION="${MIN_SELF_DELEGATION:-1}"
GAS_PRICES="${GAS_PRICES:-1000000000asteem}"
ORACLE_PROFILE="${ORACLE_PROFILE:-go}"     # go|python|js
FAUCET_URL="${FAUCET_URL:-https://faucet.steemscanner.com/faucet}"

VALIDATOR_JSON="${VALIDATOR_JSON:-validator.json}"
ADDRESS_FILE="${ADDRESS_FILE:-validator-address.txt}"
MNEMONIC_FILE="${MNEMONIC_FILE:-validator-mnemonic.txt}"    # master recovery phrase — chmod 600

START_TIMEOUT="${START_TIMEOUT:-1800}"
NAME_TIMEOUT="${NAME_TIMEOUT:-1800}"
FUND_TIMEOUT="${FUND_TIMEOUT:-300}"

# ── helpers ──────────────────────────────────────────────────────────────────
log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m✔\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

node() { docker exec "$CONTAINER" "$BIN" "$@"; }
node_i() { docker exec -it "$CONTAINER" "$BIN" "$@"; }    # -it: real TTY, needed for password prompts to show/mask
kf() { node "$@" --keyring-backend "$KEYRING" --home "$HOME_DIR"; }
kf_i() { node_i "$@" --keyring-backend "$KEYRING" --home "$HOME_DIR"; }

command -v docker >/dev/null || die "docker not found on PATH"
$COMPOSE version >/dev/null 2>&1 || die "'$COMPOSE' not available (set COMPOSE=docker-compose ?)"
[ -f docker-compose.yml ] || die "run this from the repository root."

# fix_mempool_type patches a known-stale config.toml value: an old/carried-over
# config.toml (repo-level Instructions/ cache, or an already-initialized node's
# own ~/.steemvm/config/config.toml) can still have [mempool] type = "flood"
# (the stock cosmos-sdk default) instead of "app", which this chain's EVM
# mempool integration rejects at boot: "EVM mempool enabled, but comet-bft has
# invalid config.toml:mempool.type (want 'app', got 'flood')". Patches in
# place (not a full re-copy) so any other local hand-edits (persistent_peers,
# etc.) are untouched. Returns 0 if it changed something, 1 if nothing to do.
fix_mempool_type() {
  local f="$1"
  [ -f "$f" ] || return 1
  awk '/^\[mempool\]/{m=1;next} /^\[/{m=0} m && /^type[ \t]*=[ \t]*"flood"/{found=1} END{exit !found}' "$f" || return 1
  sed -i.bak '/^\[mempool\]/,/^\[/{s/^type = "flood"/type = "app"/}' "$f"
  rm -f "$f.bak"
  warn "patched stale mempool.type \"flood\" -> \"app\" in $f"
  return 0
}

# A snapshot can't exist before the chain has passed one snapshot-interval
# (Instructions/app.toml.example ships snapshot-interval = 1000). On a chain
# younger than this, state-sync's "discovering snapshots" phase hangs
# forever — there's nothing to discover — so fetch_statesync_trust refuses
# to hand back a trust anchor below this height and the caller falls back to
# a normal replay from genesis instead (cheap anyway at this height).
MIN_STATESYNC_HEIGHT=1500

# fetch_statesync_trust queries $SEED_RPC for a recent, verifiable block
# height+hash to use as state-sync's light-client trust anchor. Sets
# TRUST_HEIGHT/TRUST_HASH; returns 1 (leaving both unset) on any failure —
# including "chain too young for a snapshot to exist yet" — so the caller
# can fall back to a normal full replay from genesis instead.
fetch_statesync_trust() {
  TRUST_HEIGHT="" TRUST_HASH=""
  command -v curl >/dev/null 2>&1 && command -v jq >/dev/null 2>&1 || return 1
  local latest h hash
  latest="$(curl -fsS "${SEED_RPC%/}/status" 2>/dev/null | jq -r '.result.sync_info.latest_block_height // empty' 2>/dev/null)"
  [ -n "$latest" ] || return 1
  [ "$latest" -ge "$MIN_STATESYNC_HEIGHT" ] || return 1
  # Trust a height comfortably behind the tip — a snapshot at/before it is
  # far more likely to already exist, and it stays safely inside the trust
  # period.
  h=$((latest - 1000))
  [ "$h" -lt 1 ] && h=1
  hash="$(curl -fsS "${SEED_RPC%/}/commit?height=$h" 2>/dev/null | jq -r '.result.signed_header.commit.block_id.hash // empty' 2>/dev/null)"
  [ -n "$hash" ] || return 1
  TRUST_HEIGHT="$h" TRUST_HASH="$hash"
  return 0
}

# Standing extra peers (xpilar.witness, blaze.apps), always included
# alongside whatever $SEED_RPC resolves to, so every new validator connects
# to multiple independent nodes instead of a single point of failure.
# Space-separated "id@host:26656" entries — add more here as needed, or
# update an entry if that node's identity/address ever changes.
EXTRA_PERSISTENT_PEERS="fe9ccc3ada6f92f20028430021585e413562bdbc@95.217.44.178:26656 9ce6a5ecd05e9bd8a7f455976b308094329d7937@167.235.9.31:26656"

# fetch_seed_peer derives this network's persistent_peers/rpc_servers entries
# LIVE from $SEED_RPC, instead of relying on Instructions/config.toml's
# static, easy-to-go-stale hardcoded values. A validator restart (e.g. after
# a fresh-genesis reset) changes that node's P2P node ID every time, and
# nothing keeps the committed template in sync automatically — deriving it
# fresh here means this script can never connect to a stale/wrong peer as
# long as $SEED_RPC itself is correct. Sets SEED_PEER ("id@host:26656[,extra]")
# and SEED_RPC_LIST ("SEED_RPC,SEED_RPC" — cometbft's light client wants 2+
# witnesses; reusing the one source twice isn't independently-verifying, but
# this is a convenience default, not a security-critical public network).
# Returns 1 (leaving both unset) on any failure.
fetch_seed_peer() {
  SEED_PEER="" SEED_RPC_LIST=""
  command -v curl >/dev/null 2>&1 && command -v jq >/dev/null 2>&1 || return 1
  local node_id host p
  node_id="$(curl -fsS "${SEED_RPC%/}/status" 2>/dev/null | jq -r '.result.node_info.id // empty' 2>/dev/null)"
  [ -n "$node_id" ] || return 1
  host="$(printf '%s' "$SEED_RPC" | sed -E 's#^[a-zA-Z]+://##; s#[:/].*##')"
  [ -n "$host" ] || return 1
  SEED_PEER="${node_id}@${host}:26656"
  # Append the standing extras, skipping any whose host matches $SEED_RPC's
  # own (avoids a duplicate self-peer entry).
  for p in $EXTRA_PERSISTENT_PEERS; do
    case "$p" in
      *"@${host}:"*) ;;
      *) SEED_PEER="${SEED_PEER},${p}" ;;
    esac
  done
  SEED_RPC_LIST="${SEED_RPC},${SEED_RPC}"
  return 0
}

# apply_seed_peer patches persistent_peers/rpc_servers in a config.toml with
# the live values from fetch_seed_peer, so this run always connects to the
# actual current network instead of whatever Instructions/config.toml.example
# last happened to have hardcoded. Returns 1 (no-op) if fetch_seed_peer never
# succeeded.
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

# enable_statesync patches a config.toml's [statesync] section (enable,
# trust_height, trust_hash) so the new node fast-bootstraps from a snapshot
# instead of fully replaying from genesis. Scoped to just that section so
# nothing else is touched. Requires fetch_statesync_trust to have already
# populated TRUST_HEIGHT/TRUST_HASH; no-ops (returns 1) otherwise.
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

# disable_statesync forces [statesync] enable = false. Needed because a prior
# run of this script (e.g. before MIN_STATESYNC_HEIGHT existed, or while the
# chain was still too young for any snapshot to exist) may have already
# baked `enable = true` into an existing home's config.toml — enable_statesync
# only ever flips false->true, so without this an already-bootstrapped,
# stuck home has no way back to a plain replay from genesis.
disable_statesync() {
  local f="$1"
  [ -f "$f" ] || return 1
  grep -q '^\[statesync\]' "$f" || return 1
  sed -i.bak -e '/^\[statesync\]/,/^\[/{s/^enable = true/enable = false/}' "$f"
  rm -f "$f.bak"
  return 0
}

# ── --fix-config ─────────────────────────────────────────────────────────────
run_fix_config() {
  log "Checking for known stale config values…"
  changed=0
  [ -f Instructions/config.toml ] || cp Instructions/config.toml.example Instructions/config.toml
  if fix_mempool_type Instructions/config.toml; then changed=1; fi
  if fix_mempool_type "$STEEMVM_HOME/config/config.toml"; then changed=1; fi
  if [ "$changed" = "1" ]; then
    log "Restarting the node to pick up the fix…"
    $COMPOSE down
    $COMPOSE up -d --build
    ok "Done — node restarted with the corrected config."
  else
    ok "No stale config values found — nothing to fix."
  fi
  exit 0
}

# ── --cleanup / --cleanup-full ──────────────────────────────────────────────
run_cleanup() {
  local full="${1:-}"
  warn "This will delete: $VALIDATOR_JSON, $ADDRESS_FILE, $MNEMONIC_FILE, oracle/.env,"
  warn "Instructions/{config,app}.toml, and stop containers ($COMPOSE down)."
  if [ "$full" = "full" ]; then
    warn "--full also wipes $STEEMVM_HOME (keys + chain data, irreversible) and volumes."
  fi
  read -rp "Type YES to confirm: " CONFIRM < /dev/tty
  [ "$CONFIRM" = "YES" ] || die "cleanup cancelled — nothing was deleted."

  log "Stopping containers…"
  if [ "$full" = "full" ]; then $COMPOSE down -v 2>&1 || true; else $COMPOSE down 2>&1 || true; fi

  rm -f "$VALIDATOR_JSON" "$ADDRESS_FILE" "$MNEMONIC_FILE" oracle/.env \
        Instructions/config.toml Instructions/app.toml

  if [ "$full" = "full" ]; then
    rm -rf "$STEEMVM_HOME"
    ok "Wiped $STEEMVM_HOME."
  fi
  ok "Cleanup done."
  exit 0
}

case "${1:-}" in
  --cleanup)      run_cleanup ;;
  --cleanup-full) run_cleanup full ;;
  --fix-config)   run_fix_config ;;
esac

# clear stale files from an earlier/interrupted run
if [ -f "$VALIDATOR_JSON" ] || [ -f "$ADDRESS_FILE" ] || [ -f "$MNEMONIC_FILE" ]; then
  warn "removing stale $VALIDATOR_JSON / $ADDRESS_FILE / $MNEMONIC_FILE — back up an old mnemonic now if needed."
  rm -f "$VALIDATOR_JSON" "$ADDRESS_FILE" "$MNEMONIC_FILE"
fi

# check for a leftover node home from an earlier run. This script ALWAYS
# generates a brand-new SteemVM key and always create-validators fresh — a
# stale $STEEMVM_HOME left over from an interrupted/earlier run (old
# priv_validator_key.json, partial chain data) is exactly what caused real
# failures this session: an orphaned validator identity whose consensus key
# no longer matched anything useful, and nodes stuck on inconsistent partial
# state. Since this script can never safely reuse an existing home anyway,
# offer to wipe it up front instead of limping along on stale state.
if [ -f "$STEEMVM_HOME/config/priv_validator_key.json" ]; then
  warn "$STEEMVM_HOME already has a node identity from an earlier run."
  warn "This script always generates a fresh SteemVM key and validator — leaving that"
  warn "old node home in place risks the exact stale-state failures seen before."
  read -rp "Wipe $STEEMVM_HOME and docker volumes, and start completely fresh? Type YES to confirm: " WIPE_HOME < /dev/tty
  if [ "$WIPE_HOME" = "YES" ]; then
    $COMPOSE down -v 2>&1 || true
    rm -rf "$STEEMVM_HOME"
    ok "Wiped $STEEMVM_HOME."
  else
    die "aborting — back up anything you need from $STEEMVM_HOME first (e.g. an old mnemonic), or set STEEMVM_HOME to a different path, then try again."
  fi
fi

# ── 1. Steem username → moniker + key name ────────────────────────────────
read -rp "Steem username (becomes your validator moniker): " STEEM_USERNAME < /dev/tty
[ -n "$STEEM_USERNAME" ] || die "a Steem username is required."

# ── 2. Steem owner / active / posting public keys ─────────────────────────
# Auto-fetched (public data); falls back to manual entry for multisig/delegated accounts.
STEEM_RPC="${STEEM_RPC:-https://api.steemit.com}"

fetch_steem_keys() {
  local resp
  resp="$(curl -fsS -X POST "$STEEM_RPC" -H 'content-type: application/json' \
            -d "{\"jsonrpc\":\"2.0\",\"method\":\"condenser_api.get_accounts\",\"params\":[[\"$STEEM_USERNAME\"]],\"id\":1}" 2>/dev/null)" || return 1
  [ -n "$resp" ] || return 1
  echo "$resp" | jq -e '.result[0] != null' >/dev/null 2>&1 || return 1

  for role in owner active posting; do
    wt="$(echo "$resp" | jq -r ".result[0].$role.weight_threshold")"
    nk="$(echo "$resp" | jq -r ".result[0].$role.key_auths | length")"
    [ "$wt" = "1" ] && [ "$nk" = "1" ] || return 1
  done

  OWNER_KEY="$(echo "$resp" | jq -r '.result[0].owner.key_auths[0][0]')"
  ACTIVE_KEY="$(echo "$resp" | jq -r '.result[0].active.key_auths[0][0]')"
  POSTING_KEY="$(echo "$resp" | jq -r '.result[0].posting.key_auths[0][0]')"
}

OWNER_KEY="" ACTIVE_KEY="" POSTING_KEY=""
if command -v curl >/dev/null 2>&1 && command -v jq >/dev/null 2>&1 && fetch_steem_keys; then
  log "Fetched public keys for '$STEEM_USERNAME':"
  echo "  owner:   $OWNER_KEY"
  echo "  active:  $ACTIVE_KEY"
  echo "  posting: $POSTING_KEY"
  read -rp "Use these? [Y/n]: " USE_FETCHED < /dev/tty
  case "$USE_FETCHED" in
    [nN]*) OWNER_KEY="" ACTIVE_KEY="" POSTING_KEY="" ;;
  esac
fi

if [ -z "$OWNER_KEY" ] || [ -z "$ACTIVE_KEY" ] || [ -z "$POSTING_KEY" ]; then
  [ -z "${USE_FETCHED:-}" ] && warn "could not auto-fetch keys — falling back to manual entry."
  read -rp "Steem OWNER public key   (STM...): " OWNER_KEY < /dev/tty
  read -rp "Steem ACTIVE public key  (STM...): " ACTIVE_KEY < /dev/tty
  read -rp "Steem POSTING public key (STM...): " POSTING_KEY < /dev/tty
fi
[ -n "$OWNER_KEY" ] && [ -n "$ACTIVE_KEY" ] && [ -n "$POSTING_KEY" ] || die "owner, active, and posting keys are all required."
DETAILS="owner=$OWNER_KEY;active=$ACTIVE_KEY;posting=$POSTING_KEY"

# ── 3. CoinMarketCap API key — required for price-feed duty ───────────────
CMC_KEY=""
while [ -z "$CMC_KEY" ]; do
  read -rp "CoinMarketCap API key (REQUIRED — free at coinmarketcap.com/api): " CMC_KEY < /dev/tty
  [ -z "$CMC_KEY" ] && warn "required — without it your oracle misses price-feed duty and risks jailing."
done

# ── 4. fetch the network's live genesis.json ───────────────────────────────
# Fetched live (not from Instructions/genesis.json) so it can't go stale vs the running network.
read -rp "Existing node's RPC to fetch genesis + sync from [http://57.131.13.43:26657]: " SEED_RPC < /dev/tty
SEED_RPC="${SEED_RPC:-http://57.131.13.43:26657}"
mkdir -p "$STEEMVM_HOME/config"

if command -v curl >/dev/null 2>&1 && command -v jq >/dev/null 2>&1 \
   && curl -fsS "${SEED_RPC%/}/genesis" 2>/dev/null | jq -e '.result.genesis' > "$STEEMVM_HOME/config/genesis.json.fetch" 2>/dev/null; then
  mv "$STEEMVM_HOME/config/genesis.json.fetch" "$STEEMVM_HOME/config/genesis.json"
  ok "Genesis fetched from ${SEED_RPC%/}/genesis."
else
  rm -f "$STEEMVM_HOME/config/genesis.json.fetch"
  warn "could not fetch genesis from $SEED_RPC — falling back to Instructions/genesis.json."
  [ -f Instructions/genesis.json ] || die "Instructions/genesis.json not found either."
  cp Instructions/genesis.json "$STEEMVM_HOME/config/genesis.json"
  ok "Genesis staged from Instructions/genesis.json."
fi

if fetch_statesync_trust; then
  ok "State-sync trust anchor: height $TRUST_HEIGHT."
else
  warn "state-sync unavailable (chain below height $MIN_STATESYNC_HEIGHT, or $SEED_RPC unreachable) — will fall back to a full replay from genesis (cheap at this height)."
fi

if fetch_seed_peer; then
  ok "Live peer info fetched: $SEED_PEER"
else
  warn "could not fetch live peer info from $SEED_RPC — falling back to Instructions/config.toml's persistent_peers/rpc_servers (may be stale)."
fi

# ── 5. set the node moniker and start the node ──────────────────────────────
log "Setting node moniker to '$STEEM_USERNAME'…"
# Instructions/config.toml is gitignored, seeded from .example only if missing —
# rm it manually if .example changes later (e.g. persistent_peers).
[ -f Instructions/config.toml ] || cp Instructions/config.toml.example Instructions/config.toml
sed -i.bak "s/^moniker = .*/moniker = \"$STEEM_USERNAME\"/" Instructions/config.toml
rm -f Instructions/config.toml.bak
fix_mempool_type Instructions/config.toml || true
HOME_CONFIG_EXISTED=0
[ -f "$STEEMVM_HOME/config/config.toml" ] && HOME_CONFIG_EXISTED=1
fix_mempool_type "$STEEMVM_HOME/config/config.toml" || true
apply_seed_peer Instructions/config.toml || true
apply_seed_peer "$STEEMVM_HOME/config/config.toml" || true
if enable_statesync Instructions/config.toml; then
  ok "State-sync enabled — new node will fast-bootstrap from a snapshot instead of a full replay."
else
  # Correct any stale enable=true left over from an earlier run of this
  # script (e.g. before this chain had a snapshot to sync from) — otherwise
  # an already-bootstrapped home stays stuck retrying state-sync forever,
  # since Instructions/config.toml is only ever copied into a genuinely
  # fresh home (see docker-entrypoint.sh), never re-synced into one that
  # already has a priv_validator_key.json.
  disable_statesync Instructions/config.toml || true
fi
enable_statesync "$STEEMVM_HOME/config/config.toml" || disable_statesync "$STEEMVM_HOME/config/config.toml" || true

log "Starting the node…"
$COMPOSE up -d
if [ "$HOME_CONFIG_EXISTED" = "1" ]; then
  # An existing home's config.toml was just patched in place — `up -d` alone
  # won't make an already-running container reread it, so force a restart.
  log "Existing node home found — restarting so it picks up the corrected config…"
  $COMPOSE restart
fi

log "Waiting for the node to fully sync (timeout ${START_TIMEOUT}s)…"
deadline=$(( $(date +%s) + START_TIMEOUT ))
height=0
while :; do
  if docker exec "$CONTAINER" test -x "$BIN" 2>/dev/null; then
    # docker exec, not a host-side curl: avoids assuming the published RPC
    # port is reachable from wherever this script happens to run. steemvmd's
    # own `status` CLI output has NO "result" envelope (unlike the raw RPC
    # endpoint) — top-level keys are already sync_info/catching_up directly.
    STATUS="$(node status 2>/dev/null || true)"
    if [ -n "$STATUS" ]; then
      # NOT `// empty`: jq's `//` treats `false` as falsy too, same as
      # null/missing — that silently swallows the one value we actually need
      # to detect (catching_up genuinely becoming false), leaving this loop
      # spinning forever even once the node is fully synced. A bare filter
      # gives "true"/"false" for real values and an empty string only when
      # the key is genuinely absent (null), which is the fallback we want.
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
ok "Node is fully synced (height $height)."

# ── 6. generate a fresh SteemVM key and save the address ────────────────────
log "Generating SteemVM key '$STEEM_USERNAME'…"
# The leftover-home check earlier already guarantees a fresh or freshly-wiped
# keyring by this point, so this is only a safety net for the rare edge case
# where a stale key survives some other way — check first so the common case
# (key genuinely doesn't exist) doesn't print a confusing "not found" as if
# something went wrong.
if kf_i keys show "$STEEM_USERNAME" >/dev/null 2>&1; then
  warn "an existing key named '$STEEM_USERNAME' was found — deleting it to start fresh."
  kf_i keys delete "$STEEM_USERNAME" -y
fi

TMP_KEYADD="$(mktemp)"
node_i keys add "$STEEM_USERNAME" --key-type "$KEY_TYPE" --keyring-backend "$KEYRING" --home "$HOME_DIR" | tee "$TMP_KEYADD"
MNEMONIC="$(grep -oE '^([a-z]+ ){11,23}[a-z]+$' "$TMP_KEYADD" | tail -1 || true)"
rm -f "$TMP_KEYADD"

if [ -n "$MNEMONIC" ]; then
  printf '%s\n' "$MNEMONIC" > "$MNEMONIC_FILE"
  chmod 600 "$MNEMONIC_FILE"
  warn "Mnemonic saved to $MNEMONIC_FILE (chmod 600) — this is your master recovery phrase, back it up."
else
  warn "could not auto-capture the mnemonic — copy it from the output above, it won't be shown again."
fi

# tr -d '\r': docker exec -it's pseudo-TTY adds \r\n line endings, which corrupts
# $ADDR for downstream use even though it looks fine when printed.
ADDR="$(kf_i keys show "$STEEM_USERNAME" -a | tr -d '\r')"
VALOPER="$(kf_i keys show "$STEEM_USERNAME" --bech val -a | tr -d '\r')"
{
  echo "moniker:  $STEEM_USERNAME"
  echo "address:  $ADDR"
  echo "valoper:  $VALOPER"
  echo "date:     $(date -u +%FT%TZ)"
} | tee "$ADDRESS_FILE"
ok "Address saved to $ADDRESS_FILE"

# ── 7. Steem-side name registration (required by the validator identity gate) ─
MIN_MILLISTEEM="$(kf query steembridge params --output json 2>/dev/null | jq -r '.params.name_registration_min_millisteem // empty' 2>/dev/null || true)"
MIN_MILLISTEEM="${MIN_MILLISTEEM:-1}"
MIN_STEEM="$(awk -v m="$MIN_MILLISTEEM" 'BEGIN{printf "%.3f", m/1000}')"

RESOLVED_ADDR="$(kf query steembridge resolve-name "$STEEM_USERNAME" --output json 2>/dev/null | jq -r '.name.address // empty' 2>/dev/null || true)"
if [ "$RESOLVED_ADDR" = "$ADDR" ]; then
  ok "'$STEEM_USERNAME' already ACTIVE for $ADDR — skipping name registration."
else
  log "Send $MIN_STEEM+ STEEM from '$STEEM_USERNAME' to 'svm.bank', memo EXACTLY: svm-register $ADDR"
  log "Waiting for attestation (timeout ${NAME_TIMEOUT}s)…"
  deadline=$(( $(date +%s) + NAME_TIMEOUT ))
  REG_ID=""
  poll_n=0
  while :; do
    poll_n=$((poll_n + 1))
    QOUT="$(kf query steembridge awaiting-name-registrations-by-destination "$ADDR" --output json 2>&1)"
    QRC=$?
    if [ $QRC -ne 0 ]; then
      warn "query failed (exit $QRC), retrying: $QOUT"
    else
      REG_ID="$(printf '%s' "$QOUT" | jq -r '.registrations[0].id // empty' 2>/dev/null || true)"
      [ -n "$REG_ID" ] && break
      if [ $((poll_n % 4)) -eq 0 ]; then
        log "still waiting ($(( $(date +%s) - (deadline - NAME_TIMEOUT) ))s elapsed)"
      fi
    fi
    RESOLVED_ADDR="$(kf query steembridge resolve-name "$STEEM_USERNAME" --output json 2>/dev/null | jq -r '.name.address // empty' 2>/dev/null || true)"
    if [ "$RESOLVED_ADDR" = "$ADDR" ]; then
      ok "'$STEEM_USERNAME' already ACTIVE for $ADDR."
      REG_ID=""
      break
    fi
    [ "$(date +%s)" -ge "$deadline" ] && die "no attested registration for $ADDR within ${NAME_TIMEOUT}s. Then run: $BIN tx steembridge confirm-name <id> --from $STEEM_USERNAME --keyring-backend $KEYRING --home $HOME_DIR --chain-id $CHAIN_ID --gas auto --gas-adjustment 1.5 --gas-prices $GAS_PRICES -y"
    sleep 15
  done
  if [ -n "$REG_ID" ]; then
    ok "Registration #$REG_ID awaiting confirmation — confirming…"
    # Retry a few times (transient timing/sync issues do happen) and verify
    # against actual on-chain state afterward — sync broadcast mode only
    # proves CheckTx acceptance, not that the tx actually succeeded, and this
    # script previously just trusted the broadcast blindly and pressed on
    # even when confirm-name had genuinely failed, surfacing only later as a
    # confusing "no active name-service registration" at create-validator.
    CONFIRM_OK=0
    for attempt in 1 2 3; do
      TMP_CONFIRM="$(mktemp)"
      set +e
      kf_i tx steembridge confirm-name "$REG_ID" --from "$STEEM_USERNAME" --chain-id "$CHAIN_ID" \
        --gas auto --gas-adjustment 1.5 --gas-prices "$GAS_PRICES" -y 2>&1 | tee "$TMP_CONFIRM"
      rc=$?
      set -e
      CONFIRM_OUT="$(cat "$TMP_CONFIRM")"
      rm -f "$TMP_CONFIRM"
      if [ $rc -eq 0 ] && ! printf '%s' "$CONFIRM_OUT" | grep -qE '"code": [1-9]|does not exist|unauthorized'; then
        CONFIRM_OK=1
        break
      fi
      warn "confirm-name attempt $attempt failed, retrying in 10s…"
      sleep 10
    done
    [ "$CONFIRM_OK" = "1" ] || die "confirm-name failed after 3 attempts. Run manually: $BIN tx steembridge confirm-name $REG_ID --from $STEEM_USERNAME --keyring-backend $KEYRING --home $HOME_DIR --chain-id $CHAIN_ID --gas auto --gas-adjustment 1.5 --gas-prices $GAS_PRICES -y"

    sleep 6
    RESOLVED_ADDR="$(kf query steembridge resolve-name "$STEEM_USERNAME" --output json 2>/dev/null | jq -r '.name.address // empty' 2>/dev/null || true)"
    [ "$RESOLVED_ADDR" = "$ADDR" ] || die "confirm-name broadcast but the name isn't ACTIVE on-chain yet. Check: $BIN query steembridge resolve-name $STEEM_USERNAME --home $HOME_DIR"
    ok "Name '$STEEM_USERNAME' confirmed ACTIVE for $ADDR."
  fi
fi

# ── 8. claim faucet + wait for balance ───────────────────────────────────────
STAKE_NUM="${STAKE_AMOUNT%asteem}"   # awk, not [ -ge ]: these numbers exceed bash's 64-bit arithmetic

log "Claiming faucet…"
FAUCET_OUT="$(curl -fsS -X POST "$FAUCET_URL" -H 'Content-Type: application/json' -d "{\"address\":\"$ADDR\"}" 2>&1)" || true
if echo "$FAUCET_OUT" | jq -e '.success == true' >/dev/null 2>&1; then
  ok "Faucet sent $(echo "$FAUCET_OUT" | jq -r '.amount') STEEM (tx $(echo "$FAUCET_OUT" | jq -r '.txHash'))."
else
  warn "faucet claim failed: $FAUCET_OUT"
  echo "  Send STEEM to 'svm.bank', memo EXACTLY your address: $ADDR"
  read -rp "Press ENTER once funded… " _unused < /dev/tty
fi

log "Waiting for balance (timeout ${FUND_TIMEOUT}s)…"
deadline=$(( $(date +%s) + FUND_TIMEOUT ))
BAL=0
poll_n=0
while :; do
  poll_n=$((poll_n + 1))
  BAL="$(kf query bank balances "$ADDR" --output json 2>/dev/null | jq -r '.balances[]? | select(.denom=="asteem") | .amount' 2>/dev/null || true)"
  BAL="${BAL:-0}"
  if awk -v b="$BAL" -v n="$STAKE_NUM" 'BEGIN{exit !(b+0>=n+0)}'; then
    break
  fi
  if [ $((poll_n % 6)) -eq 0 ]; then
    log "still waiting — balance ${BAL}, need ${STAKE_NUM}"
  fi
  [ "$(date +%s)" -ge "$deadline" ] && die "balance still below stake requirement after ${FUND_TIMEOUT}s (current: ${BAL}). Fund more, then run create-validator manually."
  sleep 10
done
ok "Funded (balance ${BAL})."

# ── 9. build validator.json + create the validator ──────────────────────────
PUBKEY="$(node comet show-validator --home "$HOME_DIR")"
[ -n "$PUBKEY" ] || die "could not read the node consensus pubkey."

log "Writing $VALIDATOR_JSON…"
cat > "$VALIDATOR_JSON" <<EOF
{
  "pubkey": $PUBKEY,
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

log "Creating the validator…"
TMP_CREATEVAL="$(mktemp)"
set +e
node_i tx staking create-validator "/workspace/$VALIDATOR_JSON" \
  --from "$STEEM_USERNAME" --keyring-backend "$KEYRING" --home "$HOME_DIR" \
  --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --gas-prices "$GAS_PRICES" -y 2>&1 | tee "$TMP_CREATEVAL"
rc=$?
set -e
OUT="$(cat "$TMP_CREATEVAL")"
rm -f "$TMP_CREATEVAL"
if [ $rc -ne 0 ] || echo "$OUT" | grep -qiE 'active name-service|different account|Description.details|public key'; then
  warn "create-validator did not succeed."
  warn "Check: query steembridge resolve-name $STEEM_USERNAME. Re-run manually: $BIN tx staking create-validator …"
  die "stopping before oracle setup."
fi
ok "Validator create tx broadcast."

# ── 10. wire up and start this validator's oracle client ────────────────────
log "Exporting key for oracle/.env…"
PRIVKEY="$(kf_i keys unsafe-export-eth-key "$STEEM_USERNAME" 2>/dev/null | tail -1 | tr -d '[:space:]')"
[ -n "$PRIVKEY" ] || die "could not export the private key — set it up manually in oracle/.env and run: $COMPOSE --profile $ORACLE_PROFILE up -d"

[ -f oracle/.env ] && warn "oracle/.env already exists — overwriting."
{
  echo "ORACLE_PRIVATE_KEY=$PRIVKEY"
  echo "ORACLE_START_BLOCK=latest"
  echo "ORACLE_STEEM_RPC=https://api.steemit.com"
  echo "ORACLE_CMC_API_KEY=$CMC_KEY"
  echo "ORACLE_SBD_SYMBOL=SBD"
} > oracle/.env
ok "oracle/.env written."

log "Starting the oracle ($ORACLE_PROFILE profile)…"
$COMPOSE --profile "$ORACLE_PROFILE" up -d

ok "All done. Confirm your validator bonded:"
echo "    docker exec $CONTAINER $BIN query staking validator $VALOPER"
echo "Watch your oracle:"
echo "    $COMPOSE logs -f oracle-$ORACLE_PROFILE"
