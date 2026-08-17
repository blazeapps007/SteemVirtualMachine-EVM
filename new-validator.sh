#!/usr/bin/env bash
#
# new-validator.sh — interactive validator setup.
#
# Flow:
#   1. asks for your Steem username → also used as the node moniker and the
#      SteemVM key name
#   2. writes that moniker into Instructions/config.toml and starts the node
#      (docker compose up -d), waiting for it to build + produce blocks
#   3. asks for your Steem owner / active / posting PUBLIC keys
#   4. generates a brand-new SteemVM key via `steemvmd keys add <username>`
#      (mnemonic is shown once, never written to disk) and prints + saves the
#      resulting address to validator-address.txt
#   5. waits for that address to be funded (fund it yourself — genesis
#      allocation, a transfer, a faucet), then builds validator.json and runs
#      `tx staking create-validator`
#
# If the node home already has data and you want a clean fresh-chain start,
# run `docker compose down -v` yourself before this script — it does not wipe
# anything.
#
# Non-interactive knobs (defaults are fine for most runs):
#   STAKE_AMOUNT, COMMISSION_RATE, COMMISSION_MAX_RATE,
#   COMMISSION_MAX_CHANGE_RATE, MIN_SELF_DELEGATION, GAS_PRICES,
#   START_TIMEOUT, FUND_TIMEOUT
set -euo pipefail

# ── config (override via env) ────────────────────────────────────────────────
COMPOSE="${COMPOSE:-docker compose}"       # or: docker-compose
CONTAINER="${CONTAINER:-steemvm-node}"
BIN="${BIN:-/root/go/bin/steemvmd}"
HOME_DIR="${HOME_DIR:-/root/.steemvm}"
CHAIN_ID="${CHAIN_ID:-steemvm}"
KEYRING="${KEYRING:-test}"
KEY_TYPE="${KEY_TYPE:-eth_secp256k1}"

STAKE_AMOUNT="${STAKE_AMOUNT:-800000000000000000000asteem}"   # 800 STEEM
COMMISSION_RATE="${COMMISSION_RATE:-0.10}"
COMMISSION_MAX_RATE="${COMMISSION_MAX_RATE:-0.20}"
COMMISSION_MAX_CHANGE_RATE="${COMMISSION_MAX_CHANGE_RATE:-0.01}"
MIN_SELF_DELEGATION="${MIN_SELF_DELEGATION:-1}"
GAS_PRICES="${GAS_PRICES:-1000000000asteem}"

VALIDATOR_JSON="${VALIDATOR_JSON:-validator.json}"       # written to repo root == /workspace
ADDRESS_FILE="${ADDRESS_FILE:-validator-address.txt}"    # printed + saved here

START_TIMEOUT="${START_TIMEOUT:-1800}"     # seconds, node build+boot
FUND_TIMEOUT="${FUND_TIMEOUT:-600}"        # seconds, waiting for the new address to be funded

# ── helpers ──────────────────────────────────────────────────────────────────
log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m✔\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

node() { docker exec "$CONTAINER" "$BIN" "$@"; }
node_i() { docker exec -i "$CONTAINER" "$BIN" "$@"; }
kf() { node "$@" --keyring-backend "$KEYRING" --home "$HOME_DIR"; }

command -v docker >/dev/null || die "docker not found on PATH"
$COMPOSE version >/dev/null 2>&1 || die "'$COMPOSE' not available (set COMPOSE=docker-compose ?)"
[ -f docker-compose.yml ] || die "run this from the repository root (where docker-compose.yml lives) — validator.json must land in the mounted /workspace."

# ── 1. Steem username → moniker + key name ────────────────────────────────────
read -rp "Steem username (becomes your validator moniker): " STEEM_USERNAME < /dev/tty
[ -n "$STEEM_USERNAME" ] || die "a Steem username is required."

# ── 2. set the node moniker and start the node ────────────────────────────────
log "Setting node moniker to '$STEEM_USERNAME'…"
[ -f Instructions/config.toml ] || cp Instructions/config.toml.example Instructions/config.toml
sed -i.bak "s/^moniker = .*/moniker = \"$STEEM_USERNAME\"/" Instructions/config.toml
rm -f Instructions/config.toml.bak

log "Starting the node (docker compose up -d)…"
$COMPOSE up -d

log "Waiting for the node to build + reach block height ≥ 1 (timeout ${START_TIMEOUT}s)…"
deadline=$(( $(date +%s) + START_TIMEOUT ))
height=0
while :; do
  if docker exec "$CONTAINER" test -x "$BIN" 2>/dev/null; then
    h="$(node status 2>/dev/null | grep -oE '"latest_block_height":"?[0-9]+"?' | grep -oE '[0-9]+' | head -1 || true)"
    [ -n "$h" ] && height="$h"
  fi
  [ "${height:-0}" -ge 1 ] 2>/dev/null && break
  [ "$(date +%s)" -ge "$deadline" ] && die "node did not reach height 1 within ${START_TIMEOUT}s. Check: \`$COMPOSE logs -f steemvm\`."
  sleep 10
done
ok "Node is producing blocks (height $height)."

# ── 3. Steem owner / active / posting public keys ─────────────────────────────
read -rp "Steem OWNER public key   (STM...): " OWNER_KEY < /dev/tty
read -rp "Steem ACTIVE public key  (STM...): " ACTIVE_KEY < /dev/tty
read -rp "Steem POSTING public key (STM...): " POSTING_KEY < /dev/tty
[ -n "$OWNER_KEY" ] && [ -n "$ACTIVE_KEY" ] && [ -n "$POSTING_KEY" ] || die "owner, active, and posting keys are all required."
DETAILS="owner=$OWNER_KEY;active=$ACTIVE_KEY;posting=$POSTING_KEY"

# ── 4. generate a fresh SteemVM key and save the address ──────────────────────
log "Generating a new SteemVM key '$STEEM_USERNAME' (steemvmd keys add)…"
kf keys delete "$STEEM_USERNAME" -y >/dev/null 2>&1 || true
node_i keys add "$STEEM_USERNAME" --key-type "$KEY_TYPE" --keyring-backend "$KEYRING" --home "$HOME_DIR"
warn "^ SAVE THAT MNEMONIC NOW. It is shown once and is never written to disk."

ADDR="$(kf keys show "$STEEM_USERNAME" -a)"
VALOPER="$(kf keys show "$STEEM_USERNAME" --bech val -a)"
{
  echo "moniker:  $STEEM_USERNAME"
  echo "address:  $ADDR"
  echo "valoper:  $VALOPER"
  echo "date:     $(date -u +%FT%TZ)"
} | tee "$ADDRESS_FILE"
ok "Address saved to $ADDRESS_FILE"

# ── 5. wait for the new address to be funded ──────────────────────────────────
log "Waiting for $ADDR to be funded (timeout ${FUND_TIMEOUT}s) — fund it now (genesis allocation, transfer, or faucet)…"
deadline=$(( $(date +%s) + FUND_TIMEOUT ))
BAL=0
while :; do
  BAL="$(kf query bank balances "$ADDR" --output json 2>/dev/null | grep -oE '"amount":"[0-9]+"' | grep -oE '[0-9]+' | head -1 || echo 0)"
  [ "${BAL:-0}" != "0" ] && break
  [ "$(date +%s)" -ge "$deadline" ] && die "no funds arrived at $ADDR within ${FUND_TIMEOUT}s. Fund it, then run create-validator manually — the key and $VALIDATOR_JSON are ready."
  sleep 10
done
ok "Account funded (balance ${BAL} asteem base units)."

# ── 6. build validator.json + create the validator ────────────────────────────
PUBKEY="$(node comet show-validator --home "$HOME_DIR")"
[ -n "$PUBKEY" ] || die "could not read the node consensus pubkey (comet show-validator)."

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
set +e
OUT="$(node tx staking create-validator "/workspace/$VALIDATOR_JSON" \
        --from "$STEEM_USERNAME" --keyring-backend "$KEYRING" --home "$HOME_DIR" \
        --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --gas-prices "$GAS_PRICES" -y 2>&1)"
rc=$?
set -e
echo "$OUT"
if [ $rc -ne 0 ] || echo "$OUT" | grep -qiE 'active name-service|different account|Description.details|public key'; then
  warn "create-validator did not succeed."
  warn "If it mentions the name service / details: $ADDR needs an ACTIVE name link named '$STEEM_USERNAME' with these Steem keys, OR set params.name_service_enabled=false in config.yml. Fix and re-run \`$BIN tx staking create-validator …\` manually (key and $VALIDATOR_JSON are ready)."
  die "stopping."
fi

ok "Validator create tx broadcast. Confirm it bonded:"
echo "    docker exec $CONTAINER $BIN query staking validator $VALOPER"
echo "Remember: the oracle uses a SEPARATE key in oracle/.env."
