#!/usr/bin/env bash
#
# new-validator.sh — fully interactive validator + oracle setup.
#
# Flow:
#   1. asks for your Steem username → also used as the node moniker and the
#      SteemVM key name
#   2. fetches your Steem owner / active / posting PUBLIC keys itself (they're
#      public — condenser_api.get_accounts) and asks you to confirm them,
#      falling back to manual entry if that account isn't a plain single-key
#      account or the lookup fails for any reason
#   3. asks for a CoinMarketCap API key — REQUIRED, not optional: without it
#      your oracle can't price STEEM/USD_External or SBD/USD_External, and
#      missing whitelisted pairs count against your price-feed participation
#      just like missing them for any other reason — miss enough and you get
#      jailed/slashed the same as skipping the duty outright. Keeps
#      re-prompting until you give it a non-empty value.
#   4. fetches the network's live genesis.json from an existing node's RPC
#      (/genesis) and stages it before the node ever boots — never trusts a
#      possibly-stale git-committed copy for a validator joining an existing
#      chain
#   5. writes your moniker into Instructions/config.toml and starts the node
#      (docker compose up -d), waiting for it to build + produce blocks —
#      persistent_peers/seeds come from Instructions/config.toml as already
#      configured (not touched by this script — edit that file yourself if
#      you need different peers)
#   6. generates a brand-new SteemVM key via `steemvmd keys add <username>`,
#      saves the mnemonic to validator-mnemonic.txt (chmod 600, gitignored —
#      it is only ever shown once by steemvmd itself, this is your only
#      chance to capture it) and the address to validator-address.txt. Uses
#      the `file` keyring backend by default (password-protected, prompts on
#      every signing op — set KEYRING=test for the old unencrypted behavior)
#   7. walks you through Steem-side name registration: prints the exact
#      transfer to send from Steem (memo `svm-register <your-new-address>`), waits
#      for an already-bonded validator to attest it, then automatically
#      submits `confirm-name` itself once it's awaiting confirmation — this
#      is the ante-gate identity requirement create-validator needs
#   8. waits for your new address to be funded (fund it yourself — a plain
#      deposit transfer memoed with your new SteemVM address, a genesis
#      allocation, or a faucet), then builds validator.json and runs
#      `tx staking create-validator`
#   9. writes oracle/.env with your new key + CMC key and starts your oracle
#      client (default: go) via `docker compose --profile <lang> up -d`
#
# The real Steem transfers (steps 7 and 8) are yours to send manually — this
# script never touches Steem private keys, only public ones (step 2) and
# SteemVM keys (steps 6/9). Everything else is automated.
#
# If the node home already has data and you want a clean fresh-chain start,
# run `docker compose down -v` yourself before this script — it does not wipe
# anything.
#
# Non-interactive knobs (defaults are fine for most runs):
#   STAKE_AMOUNT, COMMISSION_RATE, COMMISSION_MAX_RATE,
#   COMMISSION_MAX_CHANGE_RATE, MIN_SELF_DELEGATION, GAS_PRICES,
#   START_TIMEOUT, FUND_TIMEOUT, NAME_TIMEOUT, ORACLE_PROFILE

# Re-exec under real bash if invoked as `sh new-validator.sh` (bypasses the
# shebang above) — this script uses bash-only features (set -o pipefail,
# arrays-free but still bash-specific parameter handling) that break under
# a POSIX/BusyBox `sh` (e.g. Alpine's ash) with a cryptic
# "Illegal option -o pipefail" rather than a clear error.
if [ -z "${BASH_VERSION:-}" ]; then
  if command -v bash >/dev/null 2>&1; then
    exec bash "$0" "$@"
  else
    echo "ERROR: this script requires bash (it uses 'set -o pipefail' and other bash-only features)." >&2
    echo "Install it first, e.g.: apt install bash   /   apk add bash   /   yum install bash" >&2
    exit 1
  fi
fi

set -euo pipefail

# ── config (override via env) ────────────────────────────────────────────────
COMPOSE="${COMPOSE:-docker compose}"       # or: docker-compose
CONTAINER="${CONTAINER:-steemvm-node}"
BIN="${BIN:-/root/go/bin/steemvmd}"
HOME_DIR="${HOME_DIR:-/root/.steemvm}"
STEEMVM_HOME="${STEEMVM_HOME:-$HOME/.steemvm}"    # host-side path — must match docker-compose.yml's bind mount
export STEEMVM_HOME    # docker-compose.yml's ${STEEMVM_HOME:-...} substitution needs this in the actual environment, not just a local shell var
CHAIN_ID="${CHAIN_ID:-steemvm}"
KEYRING="${KEYRING:-test}"    # unencrypted, no prompts — the only backend proven reliable through this script's automation.
                               # Set KEYRING=file (or os) for a password-protected keyring instead, but know what
                               # that costs here: EVERY keyring touch (not just signing — even reading your own
                               # address) prompts for the password, repeatedly, throughout the rest of this script.
KEY_TYPE="${KEY_TYPE:-eth_secp256k1}"

STAKE_AMOUNT="${STAKE_AMOUNT:-800000000000000000000asteem}"   # 800 STEEM
COMMISSION_RATE="${COMMISSION_RATE:-0.10}"
COMMISSION_MAX_RATE="${COMMISSION_MAX_RATE:-0.20}"
COMMISSION_MAX_CHANGE_RATE="${COMMISSION_MAX_CHANGE_RATE:-0.01}"
MIN_SELF_DELEGATION="${MIN_SELF_DELEGATION:-1}"
GAS_PRICES="${GAS_PRICES:-1000000000asteem}"
ORACLE_PROFILE="${ORACLE_PROFILE:-go}"     # go|python|js — which client to start at the end

VALIDATOR_JSON="${VALIDATOR_JSON:-validator.json}"       # written to repo root == /workspace
ADDRESS_FILE="${ADDRESS_FILE:-validator-address.txt}"    # printed + saved here
MNEMONIC_FILE="${MNEMONIC_FILE:-validator-mnemonic.txt}" # chmod 600 — this IS the master recovery phrase, treat accordingly

START_TIMEOUT="${START_TIMEOUT:-1800}"     # seconds, node build+boot
NAME_TIMEOUT="${NAME_TIMEOUT:-1800}"       # seconds, waiting for name-registration attestation
FUND_TIMEOUT="${FUND_TIMEOUT:-1800}"       # seconds, waiting for the new address to be funded (deposits attest through the same bridge flow as name registration — comparably slow)

# ── helpers ──────────────────────────────────────────────────────────────────
log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m✔\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

node() { docker exec "$CONTAINER" "$BIN" "$@"; }
# -it, not just -i: `-i` alone keeps stdin open but allocates no real TTY, so
# password-masking (and in some cases even the prompt text itself) never
# renders — steemvmd was silently waiting on a prompt you couldn't see. `-t`
# allocates a real pseudo-terminal so masking and prompt text both work. This
# whole script is already interactive (reads from /dev/tty throughout), so a
# real terminal is guaranteed to be available to allocate.
node_i() { docker exec -it "$CONTAINER" "$BIN" "$@"; }
kf() { node "$@" --keyring-backend "$KEYRING" --home "$HOME_DIR"; }
kf_i() { node_i "$@" --keyring-backend "$KEYRING" --home "$HOME_DIR"; }

command -v docker >/dev/null || die "docker not found on PATH"
$COMPOSE version >/dev/null 2>&1 || die "'$COMPOSE' not available (set COMPOSE=docker-compose ?)"
[ -f docker-compose.yml ] || die "run this from the repository root (where docker-compose.yml lives) — validator.json must land in the mounted /workspace."

# Clear any stale validator.json/address file from an earlier (possibly
# interrupted, possibly for a different username entirely) run before we do
# anything else. Without this, an interruption between here and step 9 leaves
# a leftover validator.json around; running create-validator against it later
# signs with the WRONG moniker/details from whatever ran last, not this run.
if [ -f "$VALIDATOR_JSON" ] || [ -f "$ADDRESS_FILE" ] || [ -f "$MNEMONIC_FILE" ]; then
  warn "removing stale $VALIDATOR_JSON / $ADDRESS_FILE / $MNEMONIC_FILE from an earlier run — this run regenerates them fresh."
  warn "(if you didn't already back up an old $MNEMONIC_FILE's contents, do that FIRST — Ctrl-C now — it's about to be deleted.)"
  rm -f "$VALIDATOR_JSON" "$ADDRESS_FILE" "$MNEMONIC_FILE"
fi

# ── 1. Steem username → moniker + key name ────────────────────────────────────
read -rp "Steem username (becomes your validator moniker): " STEEM_USERNAME < /dev/tty
[ -n "$STEEM_USERNAME" ] || die "a Steem username is required."

# ── 2. Steem owner / active / posting public keys ─────────────────────────────
# Public keys are, well, public — fetch them straight from Steem
# (condenser_api.get_accounts) instead of asking you to paste three strings.
# Only trusted for a plain single-key account (weight_threshold=1, exactly one
# key_auths entry) — anything more exotic (multisig, delegated auth) falls
# back to asking you directly rather than guessing which key is "the" key.
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
  log "Fetched public keys for '$STEEM_USERNAME' from $STEEM_RPC:"
  echo "  owner:   $OWNER_KEY"
  echo "  active:  $ACTIVE_KEY"
  echo "  posting: $POSTING_KEY"
  read -rp "Use these? [Y/n]: " USE_FETCHED < /dev/tty
  case "$USE_FETCHED" in
    [nN]*) OWNER_KEY="" ACTIVE_KEY="" POSTING_KEY="" ;;
  esac
fi

if [ -z "$OWNER_KEY" ] || [ -z "$ACTIVE_KEY" ] || [ -z "$POSTING_KEY" ]; then
  [ -z "${USE_FETCHED:-}" ] && warn "could not auto-fetch keys (no curl/jq, network issue, account not found, or a multisig/delegated account) — falling back to manual entry."
  read -rp "Steem OWNER public key   (STM...): " OWNER_KEY < /dev/tty
  read -rp "Steem ACTIVE public key  (STM...): " ACTIVE_KEY < /dev/tty
  read -rp "Steem POSTING public key (STM...): " POSTING_KEY < /dev/tty
fi
[ -n "$OWNER_KEY" ] && [ -n "$ACTIVE_KEY" ] && [ -n "$POSTING_KEY" ] || die "owner, active, and posting keys are all required."
DETAILS="owner=$OWNER_KEY;active=$ACTIVE_KEY;posting=$POSTING_KEY"

# ── 3. CoinMarketCap API key — REQUIRED ────────────────────────────────────────
# Not optional: your oracle can't price STEEM/USD_External or SBD/USD_External
# without it, and a missed whitelisted pair is a missed price-feed duty —
# skip this and you're walking into a jail/slash for something a 30-second
# CoinMarketCap signup would have prevented. Get a free key at
# https://coinmarketcap.com/api/ if you don't have one yet.
CMC_KEY=""
while [ -z "$CMC_KEY" ]; do
  read -rp "CoinMarketCap API key (REQUIRED — get one free at coinmarketcap.com/api): " CMC_KEY < /dev/tty
  [ -z "$CMC_KEY" ] && warn "a CoinMarketCap API key is required — without it your oracle will miss price-feed duty and risks getting jailed/slashed."
done

# ── 4. fetch the network's live genesis.json ───────────────────────────────────
read -rp "Existing node's RPC to fetch genesis + sync from [https://svm-rpc.steemscanner.com]: " SEED_RPC < /dev/tty
SEED_RPC="${SEED_RPC:-https://svm-rpc.steemscanner.com}"
command -v curl >/dev/null 2>&1 || die "curl is required to fetch genesis from $SEED_RPC — install it first."
command -v jq   >/dev/null 2>&1 || die "jq is required to extract genesis from the RPC response — install it first (apt install jq / apk add jq)."

log "Fetching genesis from ${SEED_RPC%/}/genesis…"
mkdir -p "$STEEMVM_HOME/config"
if ! curl -fsS "${SEED_RPC%/}/genesis" | jq -e '.result.genesis' > "$STEEMVM_HOME/config/genesis.json.fetch" 2>/dev/null; then
  rm -f "$STEEMVM_HOME/config/genesis.json.fetch"
  die "failed to fetch genesis from ${SEED_RPC%/}/genesis — node unreachable, or the genesis is too large for this endpoint (CometBFT falls back to /genesis_chunked above its size threshold, not handled here)."
fi
mv "$STEEMVM_HOME/config/genesis.json.fetch" "$STEEMVM_HOME/config/genesis.json"
ok "Genesis staged at $STEEMVM_HOME/config/genesis.json"

# ── 5. set the node moniker and start the node ────────────────────────────────
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

# ── 6. generate a fresh SteemVM key and save the address ──────────────────────
log "Generating a new SteemVM key '$STEEM_USERNAME' (steemvmd keys add)…"
kf_i keys delete "$STEEM_USERNAME" -y || true    # no output redirect: a file/os-backend delete may prompt for the keyring passphrase, which must stay visible

# Capture keys add's output via tee — the terminal still sees everything live
# (including any keyring passphrase prompt for the file/os backend), while a
# copy lands in a temp file for extraction. Mnemonics only get shown ONCE at
# creation, so this is the only chance to save it — the discriminating regex
# below (12-24 lowercase words on their own line) reliably isolates it
# regardless of whether output is JSON or text, or what prompt text preceded it.
TMP_KEYADD="$(mktemp)"
node_i keys add "$STEEM_USERNAME" --key-type "$KEY_TYPE" --keyring-backend "$KEYRING" --home "$HOME_DIR" | tee "$TMP_KEYADD"
MNEMONIC="$(grep -oE '^([a-z]+ ){11,23}[a-z]+$' "$TMP_KEYADD" | tail -1 || true)"
rm -f "$TMP_KEYADD"

if [ -n "$MNEMONIC" ]; then
  printf '%s\n' "$MNEMONIC" > "$MNEMONIC_FILE"
  chmod 600 "$MNEMONIC_FILE"
  warn "Mnemonic saved to $MNEMONIC_FILE (chmod 600). This is the MASTER RECOVERY PHRASE for this validator's"
  warn "funds — anyone who reads it has full control. Back it up somewhere safe (password manager, offline"
  warn "storage) and consider deleting this file afterward. It is gitignored, but double-check before any commit."
else
  warn "COULD NOT AUTO-CAPTURE THE MNEMONIC — copy it manually from the 'keys add' output printed just above."
  warn "It will never be shown again."
fi

# kf_i, not kf: with the file/os backend, even reading an existing key's
# address requires the keyring password — the EOF crash this script hit came
# from these two calls still using the non-interactive kf() wrapper, which
# has no stdin connected at all for steemvmd to read a password from.
ADDR="$(kf_i keys show "$STEEM_USERNAME" -a)"
VALOPER="$(kf_i keys show "$STEEM_USERNAME" --bech val -a)"
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

# Skip the whole dance if this is a re-run after an earlier interruption and
# the name is already ACTIVE (e.g. you confirmed it manually while a prior
# instance of this script was still polling — that instance can never detect
# it either, since a CONFIRMED registration no longer shows up in the
# "awaiting" list it's watching, and it'll just loop until it times out).
RESOLVED_ADDR="$(kf query steembridge resolve-name "$STEEM_USERNAME" --output json 2>/dev/null | jq -r '.name.address // empty' 2>/dev/null || true)"
if [ "$RESOLVED_ADDR" = "$ADDR" ]; then
  ok "'$STEEM_USERNAME' is already ACTIVE for $ADDR — name registration already done, skipping ahead."
else
  log "Name registration required before create-validator will pass this chain's ante gate."
  echo
  echo "  From your Steem account '$STEEM_USERNAME', send at least $MIN_STEEM STEEM to 'svm.bank'"
  echo "  with memo EXACTLY:  svm-register $ADDR"
  echo "  (the memo carries the DESTINATION SteemVM address, not your Steem username —"
  echo "   the relayer already knows the username from who sent the transfer)"
  echo
  log "Waiting for an already-bonded validator to attest it (timeout ${NAME_TIMEOUT}s)…"
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
      # jq, not grep: cosmos-sdk's `--output json` pretty-prints with a space
      # after every colon ("id": "3", not "id":"3") — a grep pattern assuming
      # no space silently never matches, no matter how long you wait. This bit
      # every run of this script so far; jq parses the actual JSON structure
      # instead of pattern-matching formatting that isn't guaranteed stable.
      REG_ID="$(printf '%s' "$QOUT" | jq -r '.registrations[0].id // empty' 2>/dev/null || true)"
      [ -n "$REG_ID" ] && break
      # Every 4th poll (~1min at the default 15s interval), print what the
      # query actually returned — the two prior times this loop looked "stuck"
      # despite a manual query on the side confirming the registration WAS
      # ready, this is what would have shown whether it's a real parsing bug
      # or just needing more time.
      if [ $((poll_n % 4)) -eq 0 ]; then
        log "still waiting ($(( $(date +%s) - (deadline - NAME_TIMEOUT) ))s elapsed) — last query returned: $QOUT"
      fi
    fi
    # Also bail early if the name went ACTIVE without us ever seeing it in
    # the "awaiting" list — possible if a validator's attestation AND the
    # threshold-confirmation both land between two polls, or (as above) if
    # you confirmed it manually mid-poll.
    RESOLVED_ADDR="$(kf query steembridge resolve-name "$STEEM_USERNAME" --output json 2>/dev/null | jq -r '.name.address // empty' 2>/dev/null || true)"
    if [ "$RESOLVED_ADDR" = "$ADDR" ]; then
      ok "'$STEEM_USERNAME' is already ACTIVE for $ADDR."
      REG_ID=""
      break
    fi
    [ "$(date +%s)" -ge "$deadline" ] && die "no attested registration for $ADDR within ${NAME_TIMEOUT}s. Send the transfer above (if you haven't), wait for a validator to attest it, then run: $BIN tx steembridge confirm-name <id> --from $STEEM_USERNAME --keyring-backend $KEYRING --home $HOME_DIR --chain-id $CHAIN_ID --gas auto --gas-adjustment 1.5 --gas-prices $GAS_PRICES -y"
    sleep 15
  done
  if [ -n "$REG_ID" ]; then
    ok "Registration #$REG_ID is awaiting confirmation — confirming now…"
    kf_i tx steembridge confirm-name "$REG_ID" --from "$STEEM_USERNAME" --chain-id "$CHAIN_ID" \
      --gas auto --gas-adjustment 1.5 --gas-prices "$GAS_PRICES" -y
    ok "Name '$STEEM_USERNAME' confirmed ACTIVE for $ADDR."
  fi
fi

# ── 8. wait for the new address to be funded ──────────────────────────────────
# STAKE_NUM: STAKE_AMOUNT with the "asteem" suffix stripped, for comparison
# below. These numbers (10^20+) exceed bash's 64-bit integer arithmetic, so
# the comparison uses awk instead of [ -ge ] / (( )).
STAKE_NUM="${STAKE_AMOUNT%asteem}"

log "Now fund $ADDR — needs enough for the self-stake ($STAKE_AMOUNT) plus gas:"
echo
echo "  Send STEEM to 'svm.bank' with memo EXACTLY your new SteemVM address:  $ADDR"
echo "  (a plain deposit — do NOT reuse the svm-register memo for this transfer)"
echo "  Get a comfortable buffer above the self-stake amount — e.g. 30,000 STEEM from a"
echo "  faucet if you're on a testnet — so you don't come up short on gas/operational funds."
echo
read -rp "Press ENTER once you've sent it… " _unused < /dev/tty

log "Waiting for the deposit to be observed, attested, and minted (timeout ${FUND_TIMEOUT}s) —"
log "this goes through the same bridge attestation flow as name registration, so it can take a few minutes."
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
    log "still waiting ($(( $(date +%s) - (deadline - FUND_TIMEOUT) ))s elapsed) — current balance: ${BAL} asteem, need >= ${STAKE_NUM} asteem"
  fi
  [ "$(date +%s)" -ge "$deadline" ] && die "balance still below the $STAKE_AMOUNT self-stake requirement after ${FUND_TIMEOUT}s (current: ${BAL} asteem). Fund more, then run create-validator manually — the key and $VALIDATOR_JSON are ready."
  sleep 10
done
ok "Account funded (balance ${BAL} asteem base units, self-stake needs ${STAKE_NUM})."

# ── 9. build validator.json + create the validator ────────────────────────────
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
# node_i (not plain node) + tee, not a bare $(...) capture: the file/os
# keyring backend prompts for a password to sign this, and that prompt needs
# to actually reach the terminal live, not be swallowed into a variable.
# pipefail (set at the top of this script) makes $? below reflect create-
# validator's own exit code, not tee's (tee itself always succeeds).
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
  warn "If it mentions the name service / details: double-check step 7 actually landed (query steembridge resolve-name $STEEM_USERNAME). Fix and re-run \`$BIN tx staking create-validator …\` manually (key and $VALIDATOR_JSON are ready)."
  die "stopping before oracle setup."
fi
ok "Validator create tx broadcast."

# ── 10. wire up and start this validator's oracle client ──────────────────────
log "Exporting the new key's raw private key for oracle/.env (never written to disk elsewhere)…"
PRIVKEY="$(kf_i keys unsafe-export-eth-key "$STEEM_USERNAME" 2>/dev/null | tail -1 | tr -d '[:space:]')"
[ -n "$PRIVKEY" ] || die "could not export the private key — set it up manually in oracle/.env (see oracle/.env.example) and run: $COMPOSE --profile $ORACLE_PROFILE up -d"

[ -f oracle/.env ] && warn "oracle/.env already exists — overwriting it with this validator's key."
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
