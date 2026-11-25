#!/usr/bin/env bash
#
# unjail.sh: find out WHY this server's validator is jailed and bring it back
# safely, only when an unjail can actually succeed (no wasted fees), and only
# with an oracle that will keep it from being jailed again.
#
# Jail types it recognises (from the chain, not guesses):
#   tombstoned   double-signing. PERMANENT: no unjail is possible, ever.
#   self-bond    self-delegation below min_self_delegation: delegate more first.
#   downtime     x/slashing jail for missed blocks: jailed_until is in the
#                future; it waits (or the cron retries) until that time passes.
#   oracle duty  x/oracle slash (bridge_duty_floor / price_duty_floor /
#                below_min_valid): no lock time, can unjail at once — after the
#                oracle is fixed AND its Steem scan cursor is caught up.
#
# Before sending the unjail it makes sure that:
#   - the node is synced and committing blocks (a node that just restarted and
#     hasn't committed a block yet answers "validator still jailed" for every
#     oracle jail — the error validators kept seeing);
#   - the oracle is healthy, configured (validator key, price API key) and
#     signs for THIS validator (./fix-oracle.sh is run first);
#   - the oracle's Steem cursor is near the Steem head. A jailed validator's
#     oracle stops scanning, so after an unjail it would resume hours or days
#     behind, attest no new bridge event in time and be jailed again
#     (bridge_duty_floor). The cursor is moved up — never past a bridge event
#     that is still waiting for attestations — and the oracle restarted;
#   - the x/oracle slash window isn't about to close, and the fee is spendable.
# If the local node still answers "still jailed" it retries via the public RPC.
#
# Usage (from the SteemVirtualMachine-EVM checkout, after `git pull`):
#   ./unjail.sh                  check + fix + unjail if possible (interactive)
#   ./unjail.sh --check          only report jail type / when unjail is possible
#   ./unjail.sh --install-cron   run it every 10 min from cron (auto-unjail)
#   ./unjail.sh --install-cron '*/5 * * * *'
#   ./unjail.sh --remove-cron
#   ./unjail.sh --cron           what the cron runs: no prompts, no waiting,
#                                timestamps, log to ./unjail.log
# Options: --restart-node (restart the node even if healthy), --no-wait.
# Env:     VALIDATOR_KEY=<key name>   KEYRING=test|file|os
#          ORACLE_CLIENT=go|python|js (if no oracle container exists yet)
#          PUBLIC_RPC=https://rpc.steemvmscan.com   MIN_WINDOW_LEFT=1200
#          AUTO_CATCHUP=1 (cron: also catch up a lagging oracle while bonded)
#
# Exit codes: 0 done / nothing to do / waiting, 1 error, 2 cannot be unjailed
# by this script (tombstoned, self-bond too low).

set -euo pipefail
cd "$(dirname "$0")"
SCRIPT_DIR="$(pwd)"

# ── arguments ──────────────────────────────────────────────────────────────
CRON=0 CHECK=0 RESTART_NODE=0 NO_WAIT=0 ACTION=run CRON_SCHED="*/10 * * * *"
while [ $# -gt 0 ]; do
  case "$1" in
    --cron) CRON=1; NO_WAIT=1; shift ;;
    --check) CHECK=1; NO_WAIT=1; shift ;;
    --restart-node) RESTART_NODE=1; shift ;;
    --no-wait) NO_WAIT=1; shift ;;
    --install-cron) ACTION=install; if [ -n "${2:-}" ] && [ "${2#--}" = "$2" ]; then CRON_SCHED="$2"; shift; fi; shift ;;
    --remove-cron) ACTION=remove; shift ;;
    -h|--help) sed -n '2,48p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1 (see --help)" >&2; exit 1 ;;
  esac
done

CONTAINER="${CONTAINER:-steemvm-node}"
BIN="${BIN:-/root/go/bin/steemvmd}"
HOME_DIR="${HOME_DIR:-/root/.steemvm}"            # inside the container
STEEMVM_HOME="${STEEMVM_HOME:-$HOME/.steemvm}"    # the same directory on this host
CHAIN_ID="${CHAIN_ID:-SteemVM-Manaslu-81630}"
GAS_PRICES="${GAS_PRICES:-1000000000asteem}"      # the chain's min gas price (1 gwei): ~0.00012 STEEM per unjail
VALIDATOR_KEY="${VALIDATOR_KEY:-}"
KEYRING="${KEYRING:-}"
NODE_RPC="${NODE_RPC:-http://127.0.0.1:26657}"    # this node's CometBFT RPC (published on localhost)
PUBLIC_RPC="${PUBLIC_RPC:-https://rpc.steemvmscan.com}"
PUBLIC_TX_NODE="${PUBLIC_TX_NODE:-https://rpc.steemvmscan.com:443}"
SLASH_WINDOW="${SLASH_WINDOW:-100800}"    # x/oracle slash_window (governance proposal 1; genesis was 14400)
MIN_WINDOW_LEFT="${MIN_WINDOW_LEFT:-1200}" # don't unjail with fewer blocks than this left in the window
CURSOR_MARGIN="${CURSOR_MARGIN:-20}"      # Steem blocks behind LIB to put the cursor
MAX_HOLDBACK="${MAX_HOLDBACK:-2400}"      # keep the cursor back for pending bridge events at most this far (~2 h)
LAG_FIX="${LAG_FIX:-1200}"                # a bonded validator's oracle this far behind (~1 h) gets caught up
AUTO_CATCHUP="${AUTO_CATCHUP:-1}"
MIN_FEE_ASTEEM=1000000000000000           # 0.001 STEEM: comfortably above an unjail's fee
LOG_FILE="${LOG_FILE:-$SCRIPT_DIR/unjail.log}"
STATE_FILE="${STATE_FILE:-$SCRIPT_DIR/.unjail-state}"
CRON_TAG="# steemvm-unjail"
ORACLE_STATE=/oracle-data/steem_relayer_state.json   # same path and format in all three clients
PUBLIC_STEEM_NODES="https://api.steemit.com https://api.steemitdev.com https://api.steem.fans https://api.steememory.com https://api.moecki.online https://api.campingclub.me https://api.pennsif.net https://api.justyy.com https://steemd.steemworld.org"

# ── output ─────────────────────────────────────────────────────────────────
if [ "$CRON" = 1 ]; then
  # Keep the log bounded, then send everything to it.
  if [ -f "$LOG_FILE" ] && [ "$(wc -c < "$LOG_FILE")" -gt 2000000 ]; then
    tail -n 5000 "$LOG_FILE" > "$LOG_FILE.tmp" && mv "$LOG_FILE.tmp" "$LOG_FILE"
  fi
  exec >>"$LOG_FILE" 2>&1
fi
if [ -t 1 ]; then B='\033[1;36m' G='\033[1;32m' Y='\033[1;33m' R='\033[1;31m' N='\033[0m'; else B='' G='' Y='' R='' N=''; fi
ts()   { if [ "$CRON" = 1 ]; then printf '%s ' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"; fi; }
log()  { printf "%s${B}==>${N} %s\n" "$(ts)" "$*"; }
ok()   { printf "%s${G}✔${N} %s\n" "$(ts)" "$*"; }
warn() { printf "%s${Y}!${N} %s\n" "$(ts)" "$*" >&2; }
die()  { printf "%s${R}ERROR:${N} %s\n" "$(ts)" "$*" >&2; exit 1; }
stop() { printf "%s${R}CANNOT UNJAIL:${N} %s\n" "$(ts)" "$*" >&2; exit 2; }
dur()  { local s=$(( $1 < 0 ? 0 : $1 )); if [ $s -ge 86400 ]; then printf '%dd %dh' $((s/86400)) $((s%86400/3600)); elif [ $s -ge 3600 ]; then printf '%dh %02dm' $((s/3600)) $((s%3600/60)); else printf '%dm' $(( (s+59)/60 )); fi; }
toml_keyring() { local f="$STEEMVM_HOME/config/client.toml"; [ -f "$f" ] || return 0; sed -n 's/^keyring-backend = "\(.*\)"/\1/p' "$f" | head -1; }
to_epoch() { date -u -d "$(printf '%s' "$1" | sed -E 's/\.[0-9]+//')" +%s 2>/dev/null || echo 0; }

# ── cron install / remove ──────────────────────────────────────────────────
if [ "$ACTION" != run ]; then
  command -v crontab >/dev/null || die "crontab is not installed (apt-get install -y cron)."
  current="$(crontab -l 2>/dev/null | grep -vF "$CRON_TAG" || true)"
  if [ "$ACTION" = remove ]; then
    printf '%s\n' "$current" | sed '/^$/d' | crontab -
    ok "cron entry removed."
    exit 0
  fi
  docker info >/dev/null 2>&1 || die "this user can't run docker; install the cron as the user that runs the node (usually root)."
  kr="${KEYRING:-$(toml_keyring)}"
  [ "${kr:-test}" = test ] || warn "keyring '$kr' needs a password, which cron can't type: the cron will only report, not unjail. new-validator.sh uses the 'test' keyring."
  line="$CRON_SCHED cd '$SCRIPT_DIR' && PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin HOME='$HOME' ./unjail.sh --cron $CRON_TAG"
  { [ -n "$current" ] && printf '%s\n' "$current"; printf '%s\n' "$line"; } | crontab -
  ok "cron installed: $CRON_SCHED"
  echo "   It checks the validator, unjails it as soon as that can succeed, and keeps the oracle caught up."
  echo "   Log: $LOG_FILE    (tail -f $LOG_FILE)     Remove: ./unjail.sh --remove-cron"
  exit 0
fi

# ── prerequisites, lock, state ─────────────────────────────────────────────
command -v docker >/dev/null || die "docker is not installed."
command -v jq >/dev/null     || die "jq is not installed (apt-get install -y jq)."
command -v curl >/dev/null   || die "curl is not installed (apt-get install -y curl)."
[ -f docker-compose.yml ] && [ -x ./fix-oracle.sh ] || die "run this from the SteemVirtualMachine-EVM checkout, after 'git pull' (fix-oracle.sh not found)."
if command -v flock >/dev/null; then
  exec 9>"$SCRIPT_DIR/.unjail.lock"
  flock -n 9 || { log "another unjail.sh is running — skipping this run."; exit 0; }
fi
st_get() { [ -f "$STATE_FILE" ] || return 0; sed -n "s/^$1=//p" "$STATE_FILE" | tail -1; }
st_set() { [ "$CHECK" = 1 ] && return 0; local t; t="$(mktemp)"; { grep -v "^$1=" "$STATE_FILE" 2>/dev/null || true; echo "$1=$2"; } > "$t"; mv "$t" "$STATE_FILE"; }

node()   { docker exec "$CONTAINER" "$BIN" "$@"; }
rpc()    { curl -fsS -m 10 "$1$2" 2>/dev/null; }
envval() { [ -f oracle/.env ] || return 0; sed -n "s/^$1=//p" oracle/.env | tail -1 | sed -E 's/[[:space:]]+#.*$//; s/^["'\'']//; s/["'\'']$//; s/[[:space:]]+$//'; }

# sync_info → {"latest_block_height","latest_block_time","catching_up"} from this node.
sync_info() {
  local s
  s="$(rpc "$NODE_RPC" /status | jq -c '(.result.sync_info // .sync_info) // empty' 2>/dev/null || true)"
  [ -n "$s" ] || s="$(node status 2>/dev/null | jq -c '(.sync_info // .SyncInfo) // empty' 2>/dev/null || true)"
  printf '%s' "$s"
}
public_height() { rpc "$PUBLIC_RPC" /status | jq -r '(.result.sync_info // .sync_info).latest_block_height // empty' 2>/dev/null || true; }

# check_node: 0 = synced and committing blocks, 1 = syncing, 2 = down / stuck.
# Sets H (height) and NOW (chain time, epoch seconds).
H=0 NOW=0
check_node() {
  local s h0 cu ref end
  s="$(sync_info)"; [ -n "$s" ] || return 2
  cu="$(printf '%s' "$s" | jq -r '.catching_up')"
  h0="$(printf '%s' "$s" | jq -r '.latest_block_height')"
  H="$h0"; NOW="$(to_epoch "$(printf '%s' "$s" | jq -r '.latest_block_time')")"
  [ "$cu" = true ] && return 1
  # It must commit a new block while we watch (a restarted node that hasn't
  # yet answers every unjail with "still jailed").
  end=$(( $(date +%s) + 40 ))
  while [ "$(date +%s)" -lt "$end" ]; do
    sleep 4
    s="$(sync_info)"; [ -n "$s" ] || return 2
    H="$(printf '%s' "$s" | jq -r '.latest_block_height')"
    NOW="$(to_epoch "$(printf '%s' "$s" | jq -r '.latest_block_time')")"
    [ "$H" -gt "$h0" ] && break
  done
  [ "$H" -gt "$h0" ] || return 2
  ref="$(public_height)"
  if [ -n "$ref" ] && [ "$H" -lt $(( ref - 10 )) ]; then return 1; fi
  return 0
}

wait_node() {   # $1 = seconds
  local end=$(( $(date +%s) + $1 )) rc
  while :; do
    rc=0; check_node || rc=$?
    [ $rc -eq 0 ] && return 0
    [ "$(date +%s)" -ge "$end" ] && return $rc
    sleep 10
  done
}

# ── 1. node ────────────────────────────────────────────────────────────────
docker inspect "$CONTAINER" >/dev/null 2>&1 || die "no '$CONTAINER' container on this server — is this the validator's machine? (start it: docker compose up -d)"
ORACLES="$(docker ps -a --format '{{.Names}}' | grep -E '^steemvm-oracle-(go|python|js)$' || true)"
if [ "$CRON" = 0 ]; then
  log "Containers:"
  for c in "$CONTAINER" $ORACLES; do
    printf '   %-24s %s\n' "$c" "$(docker inspect -f '{{.State.Status}}{{if .State.Restarting}} (restarting){{end}}' "$c")"
  done
fi
if [ "$(docker inspect -f '{{.State.Running}}' "$CONTAINER")" != true ]; then
  [ "$CHECK" = 1 ] && die "the node container is not running (start it: docker compose up -d steemvm)."
  warn "the node container is stopped — starting it."
  docker start "$CONTAINER" >/dev/null
  sleep 15
fi
if [ "$RESTART_NODE" = 1 ] && [ "$CHECK" = 0 ]; then
  log "Restarting the node (--restart-node)…"; docker restart "$CONTAINER" >/dev/null; sleep 15
fi

log "Checking the node is synced and committing blocks…"
rc=0; check_node || rc=$?
if [ $rc -ne 0 ] && [ "$CHECK" = 0 ]; then
  if [ $rc -eq 2 ]; then
    last="$(st_get last_node_restart)"
    if [ "$CRON" = 1 ] && [ -n "$last" ] && [ $(( $(date +%s) - last )) -lt 1800 ]; then
      die "the node is not producing blocks (restarted $(dur $(( $(date +%s) - last ))) ago already). Check: docker logs --tail 100 $CONTAINER   and peers: ./update_peers.sh"
    fi
    warn "the node isn't committing new blocks — restarting it."
    docker restart "$CONTAINER" >/dev/null; st_set last_node_restart "$(date +%s)"; sleep 15
  fi
  if [ "$CRON" = 1 ]; then
    rc=0; wait_node 600 || rc=$?
    [ $rc -eq 0 ] || { log "node is still syncing (height ${H:-?}, network $(public_height)) — will check again next run."; exit 0; }
  else
    log "Waiting for the node to catch up (up to 15 min)…"
    rc=0; wait_node 900 || rc=$?
    [ $rc -eq 0 ] || die "the node did not catch up within 15 minutes (height ${H:-?}, network $(public_height)). Check: docker logs --tail 100 $CONTAINER   and peers: ./update_peers.sh"
  fi
fi
NODE_OK=$rc
if [ $NODE_OK -eq 0 ]; then ok "node synced at height $H."; else warn "node is NOT healthy (height ${H:-?}, network $(public_height))."; fi

# ── 2. validator ───────────────────────────────────────────────────────────
CONS_KEY="$(node comet show-validator --home "$HOME_DIR" 2>/dev/null | jq -r '.key // empty' 2>/dev/null || true)"
[ -n "$CONS_KEY" ] || die "could not read this node's consensus key."
VALS="$(node query staking validators --output json 2>/dev/null)" || die "could not query the validator set from the node."
VAL="$(printf '%s' "$VALS" | jq -c --arg k "$CONS_KEY" '[.validators[]? | select(.consensus_pubkey.value == $k or .consensus_pubkey.key == $k)][0] // empty')"
[ -n "$VAL" ] || die "no validator on this chain uses this node's consensus key — this node isn't a validator (or isn't the one that was jailed)."
VALOPER="$(printf '%s' "$VAL" | jq -r '.operator_address')"
MONIKER="$(printf '%s' "$VAL" | jq -r '.description.moniker')"
JAILED="$(printf '%s' "$VAL" | jq -r '.jailed // false')"
STATUS="$(printf '%s' "$VAL" | jq -r '.status' | sed 's/BOND_STATUS_//')"
TOKENS="$(printf '%s' "$VAL" | jq -r '.tokens // "0"')"
MIN_SELF="$(printf '%s' "$VAL" | jq -r '.min_self_delegation // "1"')"
UNBOND_TIME="$(printf '%s' "$VAL" | jq -r '.unbonding_time // empty')"
ACC="$(node debug addr "$VALOPER" 2>/dev/null | sed -n 's/^Bech32 Acc: //p' | tr -d '\r')"
VALCONS="$(node comet show-address --home "$HOME_DIR" 2>/dev/null | tr -d '\r\n')"
steem() { awk -v a="$1" 'BEGIN{printf "%.3f", a / 1e18}'; }
ok "validator: $MONIKER ($VALOPER) — status $STATUS, jailed $JAILED, stake $(steem "$TOKENS") STEEM"

# ── 3. oracle helpers ──────────────────────────────────────────────────────
ORACLE=""
[ "$(printf '%s\n' $ORACLES | sed '/^$/d' | wc -l)" -eq 1 ] && ORACLE="$ORACLES"
oracle_cursor() { [ -n "$ORACLE" ] || return 0; docker exec "$ORACLE" cat "$ORACLE_STATE" 2>/dev/null | jq -r '.last_scanned_block // empty' 2>/dev/null || true; }
steem_nodes() {
  { envval ORACLE_STEEM_RPC; envval ORACLE_STEEM_RPCS | tr ',' '\n'; printf '%s\n' $PUBLIC_STEEM_NODES; } | sed 's/[[:space:]]//g; /^$/d' | awk '!seen[$0]++'
}
steem_lib() {
  local u v
  for u in $(steem_nodes); do
    v="$(curl -fsS -m 8 -X POST "$u" -H 'content-type: application/json' \
      -d '{"jsonrpc":"2.0","id":1,"method":"condenser_api.get_dynamic_global_properties","params":[]}' 2>/dev/null \
      | jq -r '.result.last_irreversible_block_num // empty' 2>/dev/null || true)"
    [ -n "$v" ] && { echo "$v"; return 0; }
  done
  return 1
}
oracle_running() { [ -n "$ORACLE" ] && [ "$(docker inspect -f '{{.State.Running}} {{.State.Restarting}}' "$ORACLE" 2>/dev/null)" = "true false" ]; }
oracle_bad_logs() { docker logs --since "${1:-10m}" "$ORACLE" 2>&1 | grep -qiE 'oracle exited|refusing to start|Traceback|panic:'; }

# The oracle must sign for this validator. Its logs name the valoper it
# watches; a different one means oracle/.env holds another validator's key.
check_oracle_identity() {
  local seen
  seen="$(docker logs --since 72h "$ORACLE" 2>&1 | grep -E 'idle: not (attesting|broadcasting)' | grep -oE 'steemvaloper1[0-9a-z]{38}' | sort -u || true)"
  if [ -n "$seen" ] && ! printf '%s\n' "$seen" | grep -qx "$VALOPER"; then
    die "the oracle ($ORACLE) is working for $(printf '%s' "$seen" | head -1), not this validator ($VALOPER): oracle/.env has another validator's key. Put this validator's key in ORACLE_PRIVATE_KEY (see oracle/.env.example), then re-run."
  fi
}

# The oracle must be able to do both duties, or it gets jailed again.
check_oracle_env() {
  [ -f oracle/.env ] || die "oracle/.env not found — this server has no oracle configured (see oracle/.env.example)."
  [ -n "$(envval ORACLE_PRIVATE_KEY)$(envval ORACLE_MNEMONIC)" ] || die "oracle/.env has no ORACLE_PRIVATE_KEY (or ORACLE_MNEMONIC): the oracle can't sign anything."
  local src; src="$(envval ORACLE_PRICE_SOURCE)"; src="${src:-cmc}"
  if [ "$src" = cmc ] && [ -z "$(envval ORACLE_CMC_API_KEY)" ]; then
    die "oracle/.env has no ORACLE_CMC_API_KEY, so the price feed can't run and the validator would be jailed again (price_duty_floor). Add a free CoinMarketCap key, or set ORACLE_PRICE_SOURCE=coingecko (works without a key)."
  fi
}

# Bridge events still waiting for attestations: a cursor must not jump past
# them (this oracle may still attest them). Prints Steem block numbers.
pending_steem_blocks() {
  local lib="$1"
  node query steembridge pending-deposits --output json 2>/dev/null | jq -r '.deposits[]?.steem_block // empty' 2>/dev/null || true
  node query steembridge pending-name-registrations --output json 2>/dev/null | jq -r '.registrations[]?.steem_block // empty' 2>/dev/null || true
  # A requested withdrawal may already be paid on Steem: estimate the Steem
  # block of its request (SVM ~6.5 s/block, Steem 3 s/block) with ~20 min margin.
  node query steembridge requested-withdrawals --output json 2>/dev/null | jq -r '.withdrawals[]?.created_at // empty' 2>/dev/null \
    | while read -r h; do [ -n "$h" ] && echo $(( lib - (H - h) * 65 / 30 - 400 )); done || true
}

# Move the oracle's Steem cursor up to the head (if it is behind) and restart
# the oracle. Bridge events in the skipped blocks are already final, so the
# oracle could no longer score them anyway.
catch_up_oracle() {
  local lib cur target b skipped=0 json tmp
  lib="$(steem_lib)" || { warn "no Steem node answered from this server — can't check the oracle's cursor."; return 1; }
  cur="$(oracle_cursor)"
  if [ -z "$cur" ]; then log "the oracle has no scan cursor yet (it starts at the Steem head)."; return 0; fi
  target=$(( lib - CURSOR_MARGIN ))
  for b in $(pending_steem_blocks "$lib"); do
    [ "$b" -gt 0 ] 2>/dev/null || continue
    if [ $(( lib - b )) -le "$MAX_HOLDBACK" ]; then
      [ $(( b - 1 )) -lt "$target" ] && target=$(( b - 1 ))
    else
      skipped=$(( skipped + 1 ))
    fi
  done
  [ "$skipped" -gt 0 ] && warn "$skipped pending bridge event(s) are older than $MAX_HOLDBACK Steem blocks; the other validators attest those."
  if [ "$cur" -ge "$target" ]; then
    ok "oracle cursor $cur is caught up (Steem head $lib, $(( lib - cur )) blocks behind)."
    return 0
  fi
  log "oracle cursor is at Steem block $cur, $(( lib - cur )) blocks ($(dur $(( (lib - cur) * 3 )))) behind the head $lib."
  if [ "$CHECK" = 1 ]; then log "(check only) it would be moved to $target."; return 0; fi
  log "moving it to $target and restarting $ORACLE…"
  json="$(docker exec "$ORACLE" cat "$ORACLE_STATE" 2>/dev/null || echo '{}')"
  tmp="$(mktemp)"
  printf '%s' "$json" | jq -c --argjson n "$target" '.last_scanned_block = $n' > "$tmp" 2>/dev/null || printf '{"last_scanned_block":%s}' "$target" > "$tmp"
  chmod 644 "$tmp"
  # Stopped first, so the running oracle can't overwrite the new cursor.
  docker stop -t 30 "$ORACLE" >/dev/null
  if ! docker cp "$tmp" "$ORACLE:$ORACLE_STATE"; then rm -f "$tmp"; docker start "$ORACLE" >/dev/null; die "could not write the oracle's cursor."; fi
  rm -f "$tmp"
  docker start "$ORACLE" >/dev/null
  sleep 20
  oracle_running && ! oracle_bad_logs 1m || die "the oracle did not come back healthy after the restart. Check: docker logs --tail 100 $ORACLE"
  cur="$(oracle_cursor)"
  [ "${cur:-0}" -ge "$target" ] || die "the oracle's cursor is $cur, not $target, after the restart. Check: docker logs --tail 100 $ORACLE"
  ok "oracle cursor now $cur — it attests new bridge events as they happen."
  st_set last_catchup "$(date +%s)"
}

# ── 4. not jailed: keep it that way ────────────────────────────────────────
if [ "$JAILED" != true ]; then
  ok "$MONIKER is not jailed."
  if [ -z "$ORACLE" ]; then
    warn "no single oracle container found (found: ${ORACLES:-none}) — a validator must run exactly one, or it will be jailed."
  elif ! oracle_running || oracle_bad_logs 15m; then
    warn "the oracle ($ORACLE) is not healthy."
    if [ "$CHECK" = 0 ]; then
      last="$(st_get last_fix_oracle)"
      if [ -z "$last" ] || [ $(( $(date +%s) - last )) -ge 3600 ]; then
        st_set last_fix_oracle "$(date +%s)"
        ./fix-oracle.sh || warn "fix-oracle.sh could not fix it. Check: docker logs --tail 100 $ORACLE"
      else
        warn "fix-oracle.sh already ran $(dur $(( $(date +%s) - last ))) ago. Check: docker logs --tail 100 $ORACLE"
      fi
    fi
  else
    lib="$(steem_lib || true)"; cur="$(oracle_cursor)"
    if [ -n "$lib" ] && [ -n "$cur" ]; then
      lag=$(( lib - cur ))
      if [ "$lag" -gt "$LAG_FIX" ]; then
        warn "the oracle is $lag Steem blocks ($(dur $(( lag * 3 )))) behind: it scores no bridge events like this and will be jailed at block $(( (H / SLASH_WINDOW + 1) * SLASH_WINDOW )) (bridge_duty_floor)."
        if [ "$CHECK" = 0 ] && [ "$AUTO_CATCHUP" = 1 ]; then
          last="$(st_get last_catchup)"
          if [ -z "$last" ] || [ $(( $(date +%s) - last )) -ge 3600 ]; then catch_up_oracle || true
          else warn "it was caught up $(dur $(( $(date +%s) - last ))) ago and is behind again: its Steem nodes are failing. Run ./fix-oracle.sh"; fi
        fi
      else
        ok "oracle $ORACLE healthy, $lag Steem blocks behind the head."
      fi
    fi
  fi
  exit 0
fi

# ── 5. jailed: why, and since/until when ───────────────────────────────────
SI="$(node query slashing signing-infos --output json --limit 1000 2>/dev/null || node query slashing signing-infos --output json 2>/dev/null || true)"
SI="$(printf '%s' "$SI" | jq -c --arg a "$VALCONS" '[.info[]? | select(.address == $a)][0] // empty' 2>/dev/null || true)"
TOMB="$(printf '%s' "$SI" | jq -r '.tombstoned // false' 2>/dev/null || echo false)"
JAILED_UNTIL="$(printf '%s' "$SI" | jq -r '.jailed_until // empty' 2>/dev/null || true)"
JU="$(to_epoch "${JAILED_UNTIL:-1970-01-01T00:00:00Z}")"
SELF="$(node query staking delegation "$ACC" "$VALOPER" --output json 2>/dev/null | jq -r '((.delegation_response // .).balance.amount) // "0"' 2>/dev/null || echo 0)"

# The most recent slash event against this validator, from this node's or
# the public RPC's event index: "<height> <kind> <reason> <score>".
last_slash() {
  local base q h r best=""
  for base in "$NODE_RPC" "$PUBLIC_RPC"; do
    for q in "oracle_slash.validator%3D%27$ACC%27" "slash.address%3D%27$VALCONS%27"; do
      h="$(rpc "$base" "/block_search?query=%22$q%22&per_page=1&order_by=%22desc%22" | jq -r '(.result // .).blocks[0].block.header.height // empty' 2>/dev/null || true)"
      [ -n "$h" ] || continue
      r="$(rpc "$base" "/block_results?height=$h" | jq -r --arg a "$ACC" --arg c "$VALCONS" '
          [ ((.result // .).finalize_block_events // [])[]
            | select(.type == "oracle_slash" or .type == "slash")
            | {t: .type, a: (.attributes | map({(.key): .value}) | add)}
            | select(.a.validator == $a or .a.address == $c) ][0]
          | if . == null then empty else "\(.t) \(.a.reason // "-") \(.a.score // "-")" end' 2>/dev/null || true)"
      [ -n "$r" ] || continue
      if [ -z "$best" ] || [ "$h" -gt "${best%% *}" ]; then best="$h $r"; fi
    done
    [ -n "$best" ] && break
  done
  printf '%s' "$best"
}
LAST="$(last_slash)"
if [ -n "$LAST" ]; then
  read -r SL_H SL_T SL_REASON SL_SCORE <<<"$LAST"
  case "$SL_T:$SL_REASON" in
    oracle_slash:*) ok "jailed by x/oracle at block $SL_H: $SL_REASON (score $SL_SCORE)" ;;
    slash:double_sign) ok "jailed for double-signing at block $SL_H" ;;
    *) ok "jailed by x/slashing at block $SL_H: ${SL_REASON/missing_signature/downtime (missed blocks)}" ;;
  esac
fi

if [ "$TOMB" = true ]; then
  stop "$MONIKER is TOMBSTONED (it double-signed: the same priv_validator_key ran on two machines). That is permanent — no transaction can unjail it. Create a new validator with a NEW consensus key (fresh node home); delegators can redelegate to it without waiting."
fi
if awk -v s="$SELF" -v m="$MIN_SELF" 'BEGIN{exit !(s+0 < m+0)}'; then
  stop "$MONIKER's self-delegation ($(steem "$SELF") STEEM) is below its min_self_delegation ($(steem "$MIN_SELF") STEEM). Delegate more from $ACC first (steemvmd tx staking delegate $VALOPER <amount>asteem ...), then re-run."
fi
if [ "$JU" -gt "$NOW" ]; then
  left=$(( JU - NOW ))
  log "jailed (downtime) until $JAILED_UNTIL — $(dur "$left") from now. An unjail before then is refused, so none is sent."
  if [ "$NO_WAIT" = 1 ] || [ "$left" -gt 1200 ]; then
    [ "$CRON" = 1 ] && exit 0
    echo "   Run ./unjail.sh again after that time, or let the cron do it: ./unjail.sh --install-cron"
    exit 0
  fi
  log "waiting $(dur "$left")…"
  while [ "$(to_epoch "$(sync_info | jq -r '.latest_block_time')")" -le "$JU" ]; do sleep 20; done
  check_node || true
fi
ok "eligible for unjail now (jailed_until ${JAILED_UNTIL:-none} has passed, not tombstoned, self-bond $(steem "$SELF") STEEM)."
[ -n "$UNBOND_TIME" ] && echo "   Stake is safe: it stays delegated while jailed. $(steem "$TOKENS") STEEM; it would only finish unbonding on ${UNBOND_TIME%%.*} if never unjailed."

# ── 6. timing ──────────────────────────────────────────────────────────────
LEFT=$(( SLASH_WINDOW - H % SLASH_WINDOW ))
NEXT=$(( H + LEFT ))
if [ "$LEFT" -lt "$MIN_WINDOW_LEFT" ]; then
  log "the x/oracle slash window closes at block $NEXT, only $LEFT blocks (~$(dur $(( LEFT * 13 / 2 )))) away: too little time to score duties, so it unjails after that block."
  if [ "$NO_WAIT" = 1 ]; then exit 0; fi
  until [ "$(sync_info | jq -r '.latest_block_height')" -gt "$NEXT" ]; do sleep 30; done
  check_node || true
  ok "window closed — a fresh one starts now."
fi

if [ "$CHECK" = 1 ]; then
  log "(check only) next steps would be: fix-oracle.sh, oracle cursor catch-up, then the unjail."
  [ -n "$ORACLE" ] && catch_up_oracle || true
  exit 0
fi
[ $NODE_OK -eq 0 ] || die "not unjailing: this node isn't synced, so the validator would miss blocks and be jailed again for downtime."

# Don't hammer: one attempt per 100 blocks.
last="$(st_get last_attempt_height)"
if [ -n "$last" ] && [ $(( H - last )) -lt 100 ] && [ "$CRON" = 1 ]; then
  log "an unjail was attempted at block $last — retrying after block $(( last + 100 ))."; exit 0
fi

# ── 7. key + fee ───────────────────────────────────────────────────────────
if [ -z "$KEYRING" ]; then
  KEYRING="$(toml_keyring)"
  KEYRING="${KEYRING:-test}"   # new-validator.sh's default
fi
[ "$CRON" = 1 ] && [ "$KEYRING" != test ] && die "keyring '$KEYRING' needs a password, which cron can't type. Run ./unjail.sh by hand."
kf() { node "$@" --keyring-backend "$KEYRING" --home "$HOME_DIR"; }
KEYS="$(kf keys list --output json 2>/dev/null || echo '[]')"
if [ -z "$VALIDATOR_KEY" ]; then
  if printf '%s' "$KEYS" | jq -e --arg m "$MONIKER" '.[]? | select(.name == $m)' >/dev/null 2>&1; then VALIDATOR_KEY="$MONIKER"
  elif [ "$(printf '%s' "$KEYS" | jq 'length' 2>/dev/null || echo 0)" = "1" ]; then VALIDATOR_KEY="$(printf '%s' "$KEYS" | jq -r '.[0].name')"
  else die "could not pick your key from the '$KEYRING' keyring (found: $(printf '%s' "$KEYS" | jq -r '[.[]?.name] | join(", ")')). Re-run with VALIDATOR_KEY=<name> ./unjail.sh"; fi
fi
KEY_VALOPER="$(kf keys show "$VALIDATOR_KEY" --bech val -a 2>/dev/null | tr -d '\r' || true)"
[ "$KEY_VALOPER" = "$VALOPER" ] || die "key '$VALIDATOR_KEY' is ${KEY_VALOPER:-not in the '$KEYRING' keyring}, not this validator's operator $VALOPER. Set VALIDATOR_KEY (and KEYRING) to the validator's key."
ok "key: $VALIDATOR_KEY ($ACC, keyring '$KEYRING')"

SPENDABLE="$(node query bank spendable-balances "$ACC" --output json 2>/dev/null | jq -r '[.balances[]? | select(.denom=="asteem") | .amount][0] // "0"' 2>/dev/null || echo 0)"
if ! awk -v b="$SPENDABLE" -v m="$MIN_FEE_ASTEEM" 'BEGIN{exit !(b+0 >= m+0)}'; then
  die "$ACC has $(steem "$SPENDABLE") STEEM spendable — not enough for the unjail fee. Bridge a little in: send 2 STEEM on Steem to 'svm.bank' with memo EXACTLY 'svm-deposit $ACC', wait for it to arrive, then re-run."
fi
ok "spendable: $(steem "$SPENDABLE") STEEM (the unjail costs ~0.0002)"
awk -v b="$SPENDABLE" 'BEGIN{exit !(b+0 < 5e17)}' \
  && warn "less than 0.5 STEEM liquid: price-feed votes pay fees (about 0.01 STEEM a day). Keep some STEEM unstaked."

# ── 8. oracle: fixed, ours, configured, caught up ──────────────────────────
check_oracle_env
log "Fixing and restarting the oracle (./fix-oracle.sh)…"
st_set last_fix_oracle "$(date +%s)"
if [ -n "${ORACLE_CLIENT:-}" ]; then ./fix-oracle.sh --client "$ORACLE_CLIENT"; else ./fix-oracle.sh; fi \
  || die "the oracle isn't healthy, so the validator was NOT unjailed (it would be jailed again at the next window). Fix the oracle first, then re-run."
ORACLE="$(docker ps --format '{{.Names}}' | grep -E '^steemvm-oracle-(go|python|js)$' | head -1 || true)"
[ -n "$ORACLE" ] || die "no oracle container is running after fix-oracle.sh."
check_oracle_identity
catch_up_oracle || die "could not catch the oracle up to the Steem head, so the validator was NOT unjailed (it would miss every bridge event and be jailed again)."

# ── 9. unjail ──────────────────────────────────────────────────────────────
send_unjail() {   # extra args (e.g. --node URL); prints the tx output; returns the CLI's exit code
  local tmp rc
  tmp="$(mktemp)"
  set +e
  if [ "$CRON" = 0 ] && [ -t 0 ]; then
    docker exec -it "$CONTAINER" "$BIN" tx slashing unjail --from "$VALIDATOR_KEY" --keyring-backend "$KEYRING" --home "$HOME_DIR" \
      --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --gas-prices "$GAS_PRICES" --output json -y "$@" 2>&1 | tee "$tmp"
    rc=${PIPESTATUS[0]}
  else
    docker exec -i "$CONTAINER" "$BIN" tx slashing unjail --from "$VALIDATOR_KEY" --keyring-backend "$KEYRING" --home "$HOME_DIR" \
      --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --gas-prices "$GAS_PRICES" --output json -y "$@" > "$tmp" 2>&1 </dev/null
    rc=$?
    cat "$tmp"
  fi
  set -e
  OUT="$(tr -d '\r' < "$tmp")"; rm -f "$tmp"
  echo
  return $rc
}
tx_ok() {
  TXHASH="$(printf '%s' "$OUT" | grep -o '"txhash": *"[0-9A-F]*"' | head -1 | grep -o '[0-9A-F]\{64\}' || true)"
  local code; code="$(printf '%s' "$OUT" | grep -o '"code": *[0-9]*' | head -1 | grep -o '[0-9]*$' || true)"
  [ -n "$TXHASH" ] && [ "${code:-0}" = 0 ]
}

log "Sending the unjail (fee ~0.0002 STEEM at $GAS_PRICES)…"
st_set last_attempt_height "$H"
rc=0; send_unjail || rc=$?
VIA=""
if ! { [ $rc -eq 0 ] && tx_ok; }; then
  if printf '%s' "$OUT" | grep -qi 'still jailed'; then
    # Every check above passed, so this node is answering from stale state
    # (it just restarted). The failed simulation cost nothing; ask the network.
    warn "this node answered 'still jailed' from stale state — sending through $PUBLIC_TX_NODE instead."
    VIA="--node $PUBLIC_TX_NODE"
    rc=0; send_unjail --node "$PUBLIC_TX_NODE" || rc=$?
  fi
  { [ $rc -eq 0 ] && tx_ok; } || die "the unjail was rejected (output above). No fee is charged for a rejected simulation."
fi
ok "unjail broadcast: $TXHASH"

log "Waiting for it to be included…"
for _ in $(seq 1 20); do
  sleep 6
  # shellcheck disable=SC2086
  J="$(node query staking validator "$VALOPER" --output json $VIA 2>/dev/null | jq -r '(.validator // .) | .jailed // false' 2>/dev/null || true)"
  if [ "$J" = false ]; then
    # shellcheck disable=SC2086
    S="$(node query staking validator "$VALOPER" --output json $VIA 2>/dev/null | jq -r '(.validator // .) | .status' | sed 's/BOND_STATUS_//')"
    ok "$MONIKER is unjailed (status: $S). It rejoins the active set at the next block."
    echo "   The oracle starts attesting now. Watch it: docker logs -f --since 5m $ORACLE"
    [ "$CRON" = 0 ] && echo "   Keep it from happening again: ./unjail.sh --install-cron"
    exit 0
  fi
done
# shellcheck disable=SC2086
TXR="$(node query tx "$TXHASH" --output json $VIA 2>/dev/null | jq -r '"code \(.code) \(.raw_log // "")"' 2>/dev/null || true)"
die "the validator still shows jailed after 2 minutes${TXR:+ (tx: $TXR)}. Check: docker exec $CONTAINER $BIN query staking validator $VALOPER"
