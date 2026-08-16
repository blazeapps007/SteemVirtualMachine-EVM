#!/usr/bin/env bash
#
# relaunch-validator.sh — one-shot: preserve your operator key, wipe the node for
# a FRESH chain, bring it back up, re-import the key, and create your validator.
#
# Flow (exactly the sequence you asked for):
#   1. read your key(s) from `steemvmd keys list` (prompt if there's more than one)
#   2. export that key to the host  ─ and VERIFY it re-imports (address matches)
#      *before* anything is destroyed, so a validator key can never be lost
#   3. docker compose down -v        (wipes the node home + volumes)
#   4. docker compose up -d          (fresh chain from Instructions/genesis.json)
#   5. wait for the node to build + start producing blocks
#   6. re-import the key
#   7. build validator.json + `tx staking create-validator`
#
# PREREQUISITES you handle in config.yml / Instructions/genesis.json (fresh chain):
#   • your operator account is FUNDED in genesis (you said you'll add addresses)
#   • EITHER the name service is disabled (params.name_service_enabled = false)
#     OR your account has an ACTIVE name link whose username == $MONIKER, and
#     $DETAILS carries your Steem owner/active/posting keys — otherwise the
#     validator identity gate rejects create-validator (the script tells you so).
#
# Everything below is overridable by env var:  KEY_NAME=blazed007 ./relaunch-validator.sh
set -euo pipefail

# ── config (override via env) ────────────────────────────────────────────────
COMPOSE="${COMPOSE:-docker compose}"       # or: docker-compose
CONTAINER="${CONTAINER:-steemvm-node}"
BIN="${BIN:-/root/go/bin/steemvmd}"
HOME_DIR="${HOME_DIR:-/root/.steemvm}"
CHAIN_ID="${CHAIN_ID:-steemvm}"
KEYRING="${KEYRING:-test}"
KEY_TYPE="${KEY_TYPE:-eth_secp256k1}"
KEY_NAME="${KEY_NAME:-}"                    # empty → list & prompt

# Validator parameters (must match what your genesis allows). MONIKER must equal
# your ACTIVE Steem name link if the name service is enabled on your chain.
STAKE_AMOUNT="${STAKE_AMOUNT:-800000000000000000000asteem}"   # 800 STEEM
MONIKER="${MONIKER:-}"                      # e.g. blazed007 (your Steem username)
DETAILS="${DETAILS:-}"                      # owner=STM..;active=STM..;posting=STM..
IDENTITY="${IDENTITY:-}"
WEBSITE="${WEBSITE:-}"
SECURITY="${SECURITY:-}"
COMMISSION_RATE="${COMMISSION_RATE:-0.10}"
COMMISSION_MAX_RATE="${COMMISSION_MAX_RATE:-0.20}"
COMMISSION_MAX_CHANGE_RATE="${COMMISSION_MAX_CHANGE_RATE:-0.01}"
MIN_SELF_DELEGATION="${MIN_SELF_DELEGATION:-1}"
GAS_PRICES="${GAS_PRICES:-1000000000asteem}"

# Where the exported key backup lands on the host (repo root, gitignored).
KEY_FILE="${KEY_FILE:-./.svm-operator-key.hex}"
VALIDATOR_JSON="${VALIDATOR_JSON:-validator.json}"   # written to repo root == /workspace

# Long enough to cover a from-scratch container compile after `down -v`.
START_TIMEOUT="${START_TIMEOUT:-1800}"     # seconds

# ── helpers ──────────────────────────────────────────────────────────────────
log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m✔\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

# steemvmd inside the running node container.
node() { docker exec "$CONTAINER" "$BIN" "$@"; }
node_i() { docker exec -i "$CONTAINER" "$BIN" "$@"; }
kf() { node "$@" --keyring-backend "$KEYRING" --home "$HOME_DIR"; }

command -v docker >/dev/null || die "docker not found on PATH"
$COMPOSE version >/dev/null 2>&1 || die "'$COMPOSE' not available (set COMPOSE=docker-compose ?)"
[ -f docker-compose.yml ] || die "run this from the repository root (where docker-compose.yml lives) — validator.json must land in the mounted /workspace."

# ── 1. pick the key from the CURRENTLY-RUNNING node ──────────────────────────
docker ps --format '{{.Names}}' | grep -qx "$CONTAINER" \
  || die "container '$CONTAINER' is not running. Start your current node first (\`$COMPOSE up -d\`) so I can read the key, then re-run."

log "Reading keys from the current keyring…"
mapfile -t KEYS < <(kf keys list --output json 2>/dev/null | grep -oE '"name":"[^"]+"' | sed 's/"name":"//;s/"//' || true)
[ "${#KEYS[@]}" -gt 0 ] || die "no keys found in the keyring. Create one first: \`docker exec -it $CONTAINER $BIN keys add <name> --key-type $KEY_TYPE --keyring-backend $KEYRING --home $HOME_DIR\`"

if [ -n "$KEY_NAME" ]; then
  printf '%s\n' "${KEYS[@]}" | grep -qx "$KEY_NAME" || die "KEY_NAME='$KEY_NAME' not found. Available: ${KEYS[*]}"
elif [ "${#KEYS[@]}" -eq 1 ]; then
  KEY_NAME="${KEYS[0]}"
else
  echo "Multiple keys found:"; i=1; for k in "${KEYS[@]}"; do printf '  %d) %s\n' "$i" "$k"; i=$((i+1)); done
  printf 'Select the operator key [1-%d]: ' "${#KEYS[@]}"
  read -r sel < /dev/tty
  [[ "$sel" =~ ^[0-9]+$ ]] && [ "$sel" -ge 1 ] && [ "$sel" -le "${#KEYS[@]}" ] || die "invalid selection"
  KEY_NAME="${KEYS[$((sel-1))]}"
fi
ADDR="$(kf keys show "$KEY_NAME" -a)"
ok "Using key '$KEY_NAME'  ($ADDR)"

# ── 2. export + VERIFY round-trip BEFORE destroying anything ─────────────────
log "Exporting the private key to $KEY_FILE (host) …"
# --unarmored-hex needs an interactive 'y'; capture just the 64-hex key line.
KEY_HEX="$(printf 'y\n' | node_i keys export "$KEY_NAME" --unarmored-hex --unsafe \
             --keyring-backend "$KEYRING" --home "$HOME_DIR" 2>/dev/null | grep -oiE '^[0-9a-f]{64}$' | head -1)"
[ -n "$KEY_HEX" ] || die "could not export the key (unarmored-hex). Aborting BEFORE any wipe — your key is untouched."
umask 077; printf '%s\n' "$KEY_HEX" > "$KEY_FILE"; chmod 600 "$KEY_FILE"

log "Verifying the backup re-imports to the same address (dry run)…"
CHECK="__reimport_check__"
kf keys delete "$CHECK" -y >/dev/null 2>&1 || true
node keys import-hex "$CHECK" "$KEY_HEX" --key-type "$KEY_TYPE" --keyring-backend "$KEYRING" --home "$HOME_DIR" >/dev/null 2>&1 \
  || die "import-hex verification failed. Aborting BEFORE any wipe — your key is untouched. Backup left at $KEY_FILE."
CHECK_ADDR="$(kf keys show "$CHECK" -a)"
kf keys delete "$CHECK" -y >/dev/null 2>&1 || true
[ "$CHECK_ADDR" = "$ADDR" ] || die "verification address mismatch ($CHECK_ADDR != $ADDR). Aborting BEFORE any wipe."
ok "Key backup verified re-importable. Safe to wipe."

# ── 3+4. wipe and relaunch the fresh chain ───────────────────────────────────
log "docker compose down -v  (wiping node home + volumes)…"
$COMPOSE down -v
log "docker compose up -d  (fresh chain; first start compiles the binary — this can take a while)…"
$COMPOSE up -d

# ── 5. wait for the node to build and start producing blocks ─────────────────
log "Waiting for the node to build and reach block height ≥ 1 (timeout ${START_TIMEOUT}s)…"
deadline=$(( $(date +%s) + START_TIMEOUT ))
height=0
while :; do
  if docker exec "$CONTAINER" test -x "$BIN" 2>/dev/null; then
    h="$(node status 2>/dev/null | grep -oE '"latest_block_height":"?[0-9]+"?' | grep -oE '[0-9]+' | head -1 || true)"
    [ -n "$h" ] && height="$h"
  fi
  [ "${height:-0}" -ge 1 ] 2>/dev/null && break
  [ "$(date +%s)" -ge "$deadline" ] && die "node did not reach height 1 within ${START_TIMEOUT}s. Check: \`$COMPOSE logs -f steemvm\`. Your key backup is safe at $KEY_FILE."
  sleep 10
done
ok "Node is producing blocks (height $height)."

# ── 6. re-import the operator key ────────────────────────────────────────────
log "Re-importing '$KEY_NAME'…"
kf keys delete "$KEY_NAME" -y >/dev/null 2>&1 || true
node keys import-hex "$KEY_NAME" "$KEY_HEX" --key-type "$KEY_TYPE" --keyring-backend "$KEYRING" --home "$HOME_DIR" \
  || die "re-import failed. Your key backup is safe at $KEY_FILE (import-hex it manually)."
NEW_ADDR="$(kf keys show "$KEY_NAME" -a)"
[ "$NEW_ADDR" = "$ADDR" ] || die "re-imported address mismatch ($NEW_ADDR != $ADDR)."
ok "Key re-imported ($NEW_ADDR)."

# funds check — the account must be funded by genesis for the self-stake
BAL="$(kf query bank balances "$ADDR" --output json 2>/dev/null | grep -oE '"amount":"[0-9]+"' | grep -oE '[0-9]+' | head -1 || echo 0)"
if [ "${BAL:-0}" = "0" ]; then
  warn "account $ADDR has a ZERO balance on the fresh chain."
  warn "Add it (with coins) to config.yml 'accounts:', regenerate genesis, put it in Instructions/genesis.json, and re-run — or fund it, then run create-validator manually."
  die "no funds to self-stake; stopping before create-validator. Key is imported and safe."
fi
ok "Account funded (balance ${BAL} asteem base units)."

# ── 7. build validator.json + create the validator ───────────────────────────
[ -n "$MONIKER" ] || die "MONIKER is required (your Steem username / ACTIVE name link). Set MONIKER=... and re-run (key is already imported, so create-validator can be re-run standalone)."
PUBKEY="$(node comet show-validator --home "$HOME_DIR")"
[ -n "$PUBKEY" ] || die "could not read the node consensus pubkey (comet show-validator)."

log "Writing $VALIDATOR_JSON…"
cat > "$VALIDATOR_JSON" <<EOF
{
  "pubkey": $PUBKEY,
  "amount": "$STAKE_AMOUNT",
  "moniker": "$MONIKER",
  "identity": "$IDENTITY",
  "website": "$WEBSITE",
  "security": "$SECURITY",
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
        --from "$KEY_NAME" --keyring-backend "$KEYRING" --home "$HOME_DIR" \
        --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --gas-prices "$GAS_PRICES" -y 2>&1)"
rc=$?
set -e
echo "$OUT"
if [ $rc -ne 0 ] || echo "$OUT" | grep -qiE 'active name-service|different account|Description.details|public key'; then
  warn "create-validator did not succeed."
  warn "If it mentions the name service / details: your genesis must give $ADDR an ACTIVE name link named '$MONIKER' with your Steem keys, OR set params.name_service_enabled=false in config.yml. Fix genesis and re-run \`$BIN tx staking create-validator …\` (key is already imported)."
  die "stopping; the operator key is imported and $VALIDATOR_JSON is written for you to reuse."
fi

ok "Validator create tx broadcast. Confirm it bonded:"
echo "    docker exec $CONTAINER $BIN query staking validator \$(docker exec $CONTAINER $BIN keys show $KEY_NAME --bech val -a --keyring-backend $KEYRING --home $HOME_DIR)"

# ── cleanup: shred the on-disk key backup now that it's safely re-imported ────
if command -v shred >/dev/null 2>&1; then shred -u "$KEY_FILE" 2>/dev/null || rm -f "$KEY_FILE"; else rm -f "$KEY_FILE"; fi
ok "Done. Key backup removed from disk. Remember: the oracle uses a SEPARATE key in oracle/.env."
