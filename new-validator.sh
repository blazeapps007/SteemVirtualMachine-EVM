#!/usr/bin/env bash
#
# new-validator.sh — interactive validator + oracle setup.
#
# Steps: username -> Steem pubkeys -> CMC key -> fetch genesis -> start node
# -> new SteemVM key -> Steem name registration -> faucet + stake -> create
# validator -> start oracle.
#
# Usage: ./new-validator.sh [--cleanup | --cleanup-full]
#   --cleanup       wipes generated files + `docker compose down` (keeps node home/volumes)
#   --cleanup-full  --cleanup plus wipes $STEEMVM_HOME and volumes (irreversible)
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
esac

# clear stale files from an earlier/interrupted run
if [ -f "$VALIDATOR_JSON" ] || [ -f "$ADDRESS_FILE" ] || [ -f "$MNEMONIC_FILE" ]; then
  warn "removing stale $VALIDATOR_JSON / $ADDRESS_FILE / $MNEMONIC_FILE — back up an old mnemonic now if needed."
  rm -f "$VALIDATOR_JSON" "$ADDRESS_FILE" "$MNEMONIC_FILE"
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

# ── 5. set the node moniker and start the node ──────────────────────────────
log "Setting node moniker to '$STEEM_USERNAME'…"
# Instructions/config.toml is gitignored, seeded from .example only if missing —
# rm it manually if .example changes later (e.g. persistent_peers).
[ -f Instructions/config.toml ] || cp Instructions/config.toml.example Instructions/config.toml
sed -i.bak "s/^moniker = .*/moniker = \"$STEEM_USERNAME\"/" Instructions/config.toml
rm -f Instructions/config.toml.bak

log "Starting the node…"
$COMPOSE up -d

log "Waiting for block height ≥ 1 (timeout ${START_TIMEOUT}s)…"
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

# ── 6. generate a fresh SteemVM key and save the address ────────────────────
log "Generating SteemVM key '$STEEM_USERNAME'…"
kf_i keys delete "$STEEM_USERNAME" -y || true

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
    kf_i tx steembridge confirm-name "$REG_ID" --from "$STEEM_USERNAME" --chain-id "$CHAIN_ID" \
      --gas auto --gas-adjustment 1.5 --gas-prices "$GAS_PRICES" -y
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
} > oracle/.env
ok "oracle/.env written."

log "Starting the oracle ($ORACLE_PROFILE profile)…"
$COMPOSE --profile "$ORACLE_PROFILE" up -d

ok "All done. Confirm your validator bonded:"
echo "    docker exec $CONTAINER $BIN query staking validator $VALOPER"
echo "Watch your oracle:"
echo "    $COMPOSE logs -f oracle-$ORACLE_PROFILE"
