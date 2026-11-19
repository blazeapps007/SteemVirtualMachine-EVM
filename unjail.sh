#!/usr/bin/env bash
#
# unjail.sh: bring a jailed validator (set up with new-validator.sh) back
# safely, in one go:
#
#   1. checks the node (steemvm-node) and oracle (steemvm-oracle-go/python/js)
#      containers;
#   2. restarts the node and waits until it has caught up;
#   3. runs ./fix-oracle.sh: every working Steem node into oracle/.env,
#      the fixed oracle image pulled, the oracle recreated and checked. If the
#      oracle isn't healthy it stops here, because unjailing a validator whose
#      oracle can't attest only gets it jailed again at the next window;
#   4. finds your validator from this node's consensus key, and your key in
#      the keyring (the moniker's key, as new-validator.sh named it);
#   5. if it is jailed: checks the unjail fee is spendable and that the
#      x/oracle slash window isn't about to close (the first price period
#      after unjailing is always a miss, so unjailing right before the window
#      closes can jail it again), then sends the unjail and confirms it.
#
# Usage:  ./unjail.sh                (run from the SteemVirtualMachine-EVM checkout,
#                                     after `git pull`)
# Env:    VALIDATOR_KEY=<key name>   if auto-detection can't pick your key
#         KEYRING=test|file|os       default: the node home's client.toml
#         SKIP_NODE_RESTART=1        don't restart the node
#         ORACLE_CLIENT=go|python|js if no oracle container exists yet

set -euo pipefail
cd "$(dirname "$0")"

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m✔\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

CONTAINER="${CONTAINER:-steemvm-node}"
BIN="${BIN:-/root/go/bin/steemvmd}"
HOME_DIR="${HOME_DIR:-/root/.steemvm}"            # inside the container
STEEMVM_HOME="${STEEMVM_HOME:-$HOME/.steemvm}"    # the same directory on this host
CHAIN_ID="${CHAIN_ID:-SteemVM-Manaslu-81630}"
GAS_PRICES="${GAS_PRICES:-1000000000asteem}"
VALIDATOR_KEY="${VALIDATOR_KEY:-}"
KEYRING="${KEYRING:-}"
SLASH_WINDOW="${SLASH_WINDOW:-14400}"   # x/oracle slash_window (genesis; not changed by governance)
VOTE_PERIOD="${VOTE_PERIOD:-600}"       # x/oracle/data vote_period
MIN_FEE_ASTEEM=1000000000000000         # 0.001 STEEM: comfortably above an unjail's fee

command -v docker >/dev/null || die "docker is not installed."
command -v jq >/dev/null || die "jq is not installed (apt-get install -y jq)."
[ -f docker-compose.yml ] && [ -x ./fix-oracle.sh ] || die "run this from the SteemVirtualMachine-EVM checkout, after 'git pull' (fix-oracle.sh not found)."

node()   { docker exec "$CONTAINER" "$BIN" "$@"; }
node_i() { if [ -t 0 ]; then docker exec -it "$CONTAINER" "$BIN" "$@"; else docker exec -i "$CONTAINER" "$BIN" "$@"; fi; }
height() { node status 2>/dev/null | jq -r '.sync_info.latest_block_height // empty' 2>/dev/null || true; }
catching_up() { node status 2>/dev/null | jq -r '.sync_info.catching_up' 2>/dev/null || true; }

# ── 1. containers ──────────────────────────────────────────────────────────
log "Checking containers…"
docker inspect "$CONTAINER" >/dev/null 2>&1 || die "no '$CONTAINER' container on this server — is this the validator's machine? (start it: docker compose up -d)"
printf '   %-24s %s\n' "$CONTAINER" "$(docker inspect -f '{{.State.Status}}{{if .State.Restarting}} (restarting){{end}}' "$CONTAINER")"
for c in $(docker ps -a --format '{{.Names}}' | grep -E '^steemvm-oracle-(go|python|js)$' || true); do
  printf '   %-24s %s\n' "$c" "$(docker inspect -f '{{.State.Status}}{{if .State.Restarting}} (restarting){{end}}' "$c")"
done

# ── 2. restart the node and wait for it ────────────────────────────────────
if [ "${SKIP_NODE_RESTART:-0}" = "1" ]; then
  log "SKIP_NODE_RESTART=1 — not restarting the node."
  [ "$(docker inspect -f '{{.State.Running}}' "$CONTAINER")" = "true" ] || docker start "$CONTAINER" >/dev/null
else
  log "Restarting the node ($CONTAINER)…"
  docker restart "$CONTAINER" >/dev/null
fi
log "Waiting for the node to answer and catch up (up to 15 min)…"
deadline=$(( $(date +%s) + 900 ))
until [ "$(catching_up)" = "false" ]; do
  [ "$(date +%s)" -ge "$deadline" ] && die "the node did not catch up within 15 minutes. Check: docker logs --tail 100 $CONTAINER"
  sleep 5
done
ok "Node is synced at height $(height)."

# ── 3. oracle ──────────────────────────────────────────────────────────────
log "Fixing and restarting the oracle (./fix-oracle.sh)…"
if [ -n "${ORACLE_CLIENT:-}" ]; then ./fix-oracle.sh --client "$ORACLE_CLIENT"; else ./fix-oracle.sh; fi \
  || die "the oracle isn't healthy, so the validator was NOT unjailed (it would be jailed again at the next window). Fix the oracle first, then re-run ./unjail.sh."

# ── 4. validator + key ─────────────────────────────────────────────────────
if [ -z "$KEYRING" ]; then
  KEYRING="$(sed -n 's/^keyring-backend = "\(.*\)"/\1/p' "$STEEMVM_HOME/config/client.toml" 2>/dev/null | head -1)"
  KEYRING="${KEYRING:-test}"   # new-validator.sh's default
fi
kf()   { node "$@" --keyring-backend "$KEYRING" --home "$HOME_DIR"; }
kf_i() { node_i "$@" --keyring-backend "$KEYRING" --home "$HOME_DIR"; }

CONS_KEY="$(node comet show-validator --home "$HOME_DIR" 2>/dev/null | jq -r '.key // empty' 2>/dev/null || true)"
[ -n "$CONS_KEY" ] || die "could not read this node's consensus key."
VALS="$(node query staking validators --output json 2>/dev/null)" || die "could not query the validator set from the node."
VAL="$(printf '%s' "$VALS" | jq -c --arg k "$CONS_KEY" '[.validators[]? | select(.consensus_pubkey.value == $k or .consensus_pubkey.key == $k)][0] // empty')"
[ -n "$VAL" ] || die "no validator on this chain uses this node's consensus key — this node isn't a validator (or isn't the one that was jailed)."
VALOPER="$(printf '%s' "$VAL" | jq -r '.operator_address')"
MONIKER="$(printf '%s' "$VAL" | jq -r '.description.moniker')"
JAILED="$(printf '%s' "$VAL" | jq -r '.jailed // false')"
STATUS="$(printf '%s' "$VAL" | jq -r '.status' | sed 's/BOND_STATUS_//')"
ok "validator: $MONIKER ($VALOPER) — jailed: $JAILED, status: $STATUS"

if [ "$JAILED" != "true" ]; then
  ok "Not jailed — nothing to unjail. Node and oracle were restarted and checked."
  exit 0
fi

# The key: VALIDATOR_KEY, else the moniker's key (new-validator.sh names it
# after the Steem username), else the keyring's only key. It must control the
# validator's operator address.
KEYS="$(kf keys list --output json 2>/dev/null || echo '[]')"
if [ -z "$VALIDATOR_KEY" ]; then
  if printf '%s' "$KEYS" | jq -e --arg m "$MONIKER" '.[]? | select(.name == $m)' >/dev/null 2>&1; then VALIDATOR_KEY="$MONIKER"
  elif [ "$(printf '%s' "$KEYS" | jq 'length' 2>/dev/null || echo 0)" = "1" ]; then VALIDATOR_KEY="$(printf '%s' "$KEYS" | jq -r '.[0].name')"
  else die "could not pick your key from the '$KEYRING' keyring (found: $(printf '%s' "$KEYS" | jq -r '[.[]?.name] | join(", ")')). Re-run with VALIDATOR_KEY=<name> ./unjail.sh"; fi
fi
KEY_VALOPER="$(kf keys show "$VALIDATOR_KEY" --bech val -a 2>/dev/null | tr -d '\r' || true)"
[ "$KEY_VALOPER" = "$VALOPER" ] || die "key '$VALIDATOR_KEY' is ${KEY_VALOPER:-not in the '$KEYRING' keyring}, not this validator's operator $VALOPER. Set VALIDATOR_KEY (and KEYRING) to the validator's key."
ADDR="$(kf keys show "$VALIDATOR_KEY" -a | tr -d '\r')"
ok "key: $VALIDATOR_KEY ($ADDR, keyring '$KEYRING')"

# ── 5a. fee ────────────────────────────────────────────────────────────────
SPENDABLE="$(node query bank spendable-balances "$ADDR" --output json 2>/dev/null | jq -r '[.balances[]? | select(.denom=="asteem") | .amount][0] // "0"' 2>/dev/null || echo 0)"
fmt_steem() { awk -v a="$1" 'BEGIN{printf "%.6f", a / 1e18}'; }
if ! awk -v b="$SPENDABLE" -v m="$MIN_FEE_ASTEEM" 'BEGIN{exit !(b+0 >= m+0)}'; then
  die "$ADDR has $(fmt_steem "$SPENDABLE") STEEM spendable — not enough for the unjail fee. Bridge a little in: send 2 STEEM on Steem to 'svm.bank' with memo EXACTLY 'svm-deposit $ADDR', wait for it to arrive, then re-run ./unjail.sh"
fi
ok "spendable: $(fmt_steem "$SPENDABLE") STEEM"
awk -v b="$SPENDABLE" 'BEGIN{exit !(b+0 < 5e17)}' \
  && warn "less than 0.5 STEEM liquid: price-feed votes pay fees (about 0.01 STEEM a day). Keep some STEEM unstaked."

# ── 5b. slash-window timing ────────────────────────────────────────────────
H="$(height)"
LEFT=$(( SLASH_WINDOW - H % SLASH_WINDOW ))
if [ "$LEFT" -lt $(( 2 * VOTE_PERIOD )) ]; then
  NEXT=$(( H + LEFT ))
  warn "the x/oracle slash window closes at block $NEXT, only $LEFT blocks away. Unjailing now would leave too few price periods to score, so waiting for it to close first (about $(( LEFT * 6 / 60 + 1 )) min)…"
  until [ "$(height)" -gt "$NEXT" ]; do sleep 30; done
  ok "window closed — a fresh window starts now."
fi

# ── 5c. unjail ─────────────────────────────────────────────────────────────
log "Sending the unjail transaction…"
# Shown as it runs (a password-protected keyring prompts here) and kept to read the result.
TMP_OUT="$(mktemp)"
set +e
kf_i tx slashing unjail --from "$VALIDATOR_KEY" --chain-id "$CHAIN_ID" \
  --gas auto --gas-adjustment 1.5 --gas-prices "$GAS_PRICES" --output json -y 2>&1 | tee "$TMP_OUT"
rc=${PIPESTATUS[0]}
set -e
OUT="$(tr -d '\r' < "$TMP_OUT")"
rm -f "$TMP_OUT"
echo
TXHASH="$(printf '%s' "$OUT" | grep -o '"txhash": *"[0-9A-F]*"' | head -1 | grep -o '[0-9A-F]\{64\}')"
CODE="$(printf '%s' "$OUT" | grep -o '"code": *[0-9]*' | head -1 | grep -o '[0-9]*$')"
if [ $rc -ne 0 ] || [ -z "$TXHASH" ] || [ "${CODE:-0}" != "0" ]; then
  die "the unjail transaction was rejected (see the output above)."
fi
ok "unjail broadcast: $TXHASH"

log "Waiting for it to be included…"
for _ in $(seq 1 20); do
  sleep 6
  J="$(node query staking validator "$VALOPER" --output json 2>/dev/null | jq -r '(.validator // .) | .jailed // false' 2>/dev/null || true)"
  if [ "$J" = "false" ]; then
    S="$(node query staking validator "$VALOPER" --output json 2>/dev/null | jq -r '(.validator // .) | .status' | sed 's/BOND_STATUS_//')"
    ok "$MONIKER is unjailed (status: $S). It rejoins the active set at the next block."
    echo "   Keep the oracle healthy: docker logs --since 15m steemvm-oracle-<client> | grep -E 'heartbeat|failed'"
    exit 0
  fi
done
TXR="$(node query tx "$TXHASH" --output json 2>/dev/null | jq -r '"code \(.code) \(.raw_log // "")"' 2>/dev/null || true)"
die "the validator still shows jailed after 2 minutes${TXR:+ (tx: $TXR)}. Check: docker exec $CONTAINER $BIN query staking validator $VALOPER"
