#!/usr/bin/env bash
#
# launch-genesis.sh — create a brand-new SteemVM chain and start its FIRST node
# (the genesis validator) on a fresh Linux VPS.
#
# It installs Docker if needed, builds the node and oracle images from this
# checkout, then:
#   1. records the Steem anchor: Steem's current last irreversible block and
#      its block ID. The chain never accepts Steem history from before it, and
#      every oracle checks its Steem node against the ID before attesting;
#   2. imports the genesis validator's account key from its mnemonic (typed,
#      never echoed or stored in shell history);
#   3. builds genesis.json: parameters from Instructions/genesis-params.json,
#      the anchor, the validator's Steem name link, the allocation, the gentx;
#   4. starts the node and the bridge oracle, and verifies blocks, the EVM
#      chain-id and the anchor;
#   5. writes the final genesis to Instructions/genesis.json and prints the
#      genesis sha256, the peer string and what to back up.
#
# Run it ONCE, from the repository root, on a machine with no SteemVM node:
#
#   git clone https://github.com/blazeapps007/SteemVirtualMachine-EVM.git
#   cd SteemVirtualMachine-EVM && git checkout <release branch>
#   ./launch-genesis.sh
#
# Usage: ./launch-genesis.sh [--skip-install] [--pull] [--no-oracle] [--yes]
#   --skip-install  don't install Docker/jq/curl (they must already be there)
#   --pull          pull the published images instead of building them here
#   --no-oracle     don't configure or start the bridge oracle
#   --yes           don't ask for confirmation before creating the chain
#
# Overridable via env (defaults in brackets):
#   CHAIN_ID [steemvm-2]          MONIKER [blazed007] (= the Steem account)
#   EXPECTED_ADDRESS [blazed007's steem1am0j95… address; "" skips the check]
#   ALLOC_STEEM [1000000]  ALLOC_SBD [1000000]  SELF_STAKE_STEEM [100000]
#   COMMISSION_RATE [0.10]  COMMISSION_MAX_RATE [0.20]  COMMISSION_MAX_CHANGE_RATE [0.01]
#   STEEM_RPC [https://api.steemit.com]   PUBLIC_IP [detected]
#   STEEMVM_HOME [$HOME/.steemvm]         STEEMVM_IMAGE [steemblazer/steemvmd:v<VERSION>]
#   ORACLE_PROFILE [go]  ORACLE_PRICE_SOURCE [coingecko]  ORACLE_CMC_API_KEY []
#   START_TIMEOUT [300]

if [ -z "${BASH_VERSION:-}" ]; then
  exec bash "$0" "$@"
fi
set -euo pipefail

# ── config ───────────────────────────────────────────────────────────────────
CHAIN_ID="${CHAIN_ID:-steemvm-2}"
MONIKER="${MONIKER:-blazed007}"
EXPECTED_ADDRESS="${EXPECTED_ADDRESS-steem1am0j95eum7tezy8n9fwjrj0nldkwaskg9xphh8}"
ALLOC_STEEM="${ALLOC_STEEM:-1000000}"
ALLOC_SBD="${ALLOC_SBD:-1000000}"
SELF_STAKE_STEEM="${SELF_STAKE_STEEM:-100000}"
COMMISSION_RATE="${COMMISSION_RATE:-0.10}"
COMMISSION_MAX_RATE="${COMMISSION_MAX_RATE:-0.20}"
COMMISSION_MAX_CHANGE_RATE="${COMMISSION_MAX_CHANGE_RATE:-0.01}"
STEEM_RPC="${STEEM_RPC:-https://api.steemit.com}"
STEEMVM_HOME="${STEEMVM_HOME:-$HOME/.steemvm}"
ORACLE_PROFILE="${ORACLE_PROFILE:-go}"
ORACLE_PRICE_SOURCE="${ORACLE_PRICE_SOURCE:-coingecko}"
START_TIMEOUT="${START_TIMEOUT:-300}"
CONTAINER=steemvm-node
BIN=/root/go/bin/steemvmd
KEYRING="test"
GATEWAY=svm.bank
GENTX_GAS=400000
GENTX_FEE="400000000000000asteem"   # 400000 gas at the 1 gwei minimum gas price

SKIP_INSTALL=0 PULL=0 NO_ORACLE=0 ASSUME_YES=0
for arg in "$@"; do
  case "$arg" in
    --skip-install) SKIP_INSTALL=1 ;;
    --pull) PULL=1 ;;
    --no-oracle) NO_ORACLE=1 ;;
    --yes) ASSUME_YES=1 ;;
    *) echo "unknown argument: $arg (see the header for usage)" >&2; exit 1 ;;
  esac
done

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m✔\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(uname -s)" = "Linux" ] || die "run this on the Linux VPS that will host the genesis node."
[ -f docker-compose.yml ] && [ -f Instructions/genesis-params.json ] || die "run this from the repository root."
# The node containers write the home as root; the script edits it on the host.
[ "$(id -u)" -eq 0 ] || die "run as root: sudo ./launch-genesis.sh"

# Whole-STEEM amounts → 18-decimal base units, as exact strings (no floats).
whole() { [[ "$1" =~ ^[1-9][0-9]*$ ]] || die "$2 must be a whole number, got '$1'"; printf '%s000000000000000000' "$1"; }
ALLOC_ASTEEM="$(whole "$ALLOC_STEEM" ALLOC_STEEM)"
ALLOC_ASBD="$(whole "$ALLOC_SBD" ALLOC_SBD)"
STAKE_ASTEEM="$(whole "$SELF_STAKE_STEEM" SELF_STAKE_STEEM)"
[ "$SELF_STAKE_STEEM" -lt "$ALLOC_STEEM" ] || die "SELF_STAKE_STEEM must be less than ALLOC_STEEM (the gentx fee comes from the same balance)."

# ── 1. prerequisites ─────────────────────────────────────────────────────────
if [ "$SKIP_INSTALL" = "0" ]; then
  if ! command -v jq >/dev/null || ! command -v curl >/dev/null || ! command -v git >/dev/null; then
    log "Installing curl, jq and git…"
    apt-get update -qq && apt-get install -y -qq ca-certificates curl jq git >/dev/null
  fi
  if ! command -v docker >/dev/null; then
    log "Installing Docker (get.docker.com)…"
    curl -fsSL https://get.docker.com | sh
  fi
fi
for c in docker jq curl; do command -v "$c" >/dev/null || die "$c is not installed (drop --skip-install to install it)."; done
DOCKER="docker"
$DOCKER info >/dev/null 2>&1 || die "cannot talk to Docker (is the daemon running? systemctl start docker)."
$DOCKER compose version >/dev/null 2>&1 || die "the Docker Compose plugin is missing (docker compose version fails)."
COMPOSE="$DOCKER compose"

# ── 2. refuse to touch an existing node ──────────────────────────────────────
[ ! -e "$STEEMVM_HOME/config/priv_validator_key.json" ] \
  || die "$STEEMVM_HOME already holds a node (priv_validator_key.json). This script only creates a NEW chain on a fresh machine; move that home away first if you really mean it."
# Empty or absent, so a failed run can remove it without losing anything.
if [ -e "$STEEMVM_HOME" ] && [ -n "$(ls -A "$STEEMVM_HOME" 2>/dev/null)" ]; then
  die "$STEEMVM_HOME is not empty. Use an empty or new directory (STEEMVM_HOME=…)."
fi
if $DOCKER ps -a --format '{{.Names}}' | grep -qx "$CONTAINER"; then
  die "a '$CONTAINER' container already exists on this machine. Remove it first if this really is meant to be the new chain's first node."
fi

# ── 3. Steem: the validator's public keys and the anchor ─────────────────────
steem() { curl -fsS -m 30 "$STEEM_RPC" -H 'Content-Type: application/json' \
  -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":$2}"; }

log "Reading @$MONIKER's public keys from Steem ($STEEM_RPC)…"
ACCOUNT_JSON="$(steem condenser_api.get_accounts "[[\"$MONIKER\"]]")" || die "Steem RPC unreachable: $STEEM_RPC"
key_of() { printf '%s' "$ACCOUNT_JSON" | jq -r ".result[0].$1.key_auths[0][0] // empty"; }
OWNER_KEY="$(key_of owner)" ACTIVE_KEY="$(key_of active)" POSTING_KEY="$(key_of posting)"
[ -n "$OWNER_KEY" ] && [ -n "$ACTIVE_KEY" ] && [ -n "$POSTING_KEY" ] || die "Steem account @$MONIKER not found, or it has no owner/active/posting key."
DETAILS="owner=$OWNER_KEY;active=$ACTIVE_KEY;posting=$POSTING_KEY"
ok "Steem keys: $DETAILS"

log "Recording the Steem anchor (the last irreversible block)…"
ANCHOR_BLOCK="$(steem condenser_api.get_dynamic_global_properties '[]' | jq -r '.result.last_irreversible_block_num // empty')"
[[ "$ANCHOR_BLOCK" =~ ^[0-9]+$ ]] || die "could not read Steem's last irreversible block."
ANCHOR_ID="$(steem condenser_api.get_block "[$ANCHOR_BLOCK]" | jq -r '.result.block_id // empty')"
[[ "$ANCHOR_ID" =~ ^[0-9a-f]{40}$ ]] || die "could not read the ID of Steem block $ANCHOR_BLOCK."
[ "${ANCHOR_ID:0:8}" = "$(printf '%08x' "$ANCHOR_BLOCK")" ] || die "Steem block ID $ANCHOR_ID does not start with block $ANCHOR_BLOCK — refusing a bad anchor."
ok "Steem anchor: block $ANCHOR_BLOCK, ID $ANCHOR_ID"

PUBLIC_IP="${PUBLIC_IP:-$(curl -fsS -m 10 https://api.ipify.org 2>/dev/null || true)}"
[[ "$PUBLIC_IP" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "could not detect this VPS's public IPv4 — set PUBLIC_IP=x.x.x.x"

# The Makefile sets VERSION inside an ifeq block (indented).
VERSION="$(sed -n 's/^[[:space:]]*VERSION[[:space:]]*:=[[:space:]]*\([0-9][^[:space:]]*\).*/\1/p' Makefile | head -1)"
[ -n "$VERSION" ] || die "could not read VERSION from the Makefile."
STEEMVM_IMAGE="${STEEMVM_IMAGE:-steemblazer/steemvmd:v$VERSION}"
ORACLE_IMAGE="steemblazer/steemvm-oracle-$ORACLE_PROFILE:v$VERSION"

cat <<EOF

  New chain        $CHAIN_ID (EVM chain-id from the binary)
  Genesis validator @$MONIKER  self-stake $SELF_STAKE_STEEM STEEM
  Allocation        $ALLOC_STEEM STEEM + $ALLOC_SBD SBD to @$MONIKER's account
  Steem anchor      block $ANCHOR_BLOCK  $ANCHOR_ID
  Public address    $PUBLIC_IP:26656
  Node home         $STEEMVM_HOME
  Images            $STEEMVM_IMAGE$([ "$NO_ORACLE" = "1" ] || echo ", $ORACLE_IMAGE")

EOF
if [ "$ASSUME_YES" = "0" ]; then
  read -rp "Create this chain? Type YES: " CONFIRM < /dev/tty
  [ "$CONFIRM" = "YES" ] || die "cancelled — nothing was created."
fi

# ── 4. images ────────────────────────────────────────────────────────────────
if [ "$PULL" = "1" ]; then
  if ! $DOCKER image inspect "$STEEMVM_IMAGE" >/dev/null 2>&1; then
    log "Pulling $STEEMVM_IMAGE…"
    $DOCKER pull "$STEEMVM_IMAGE"
  fi
else
  log "Building $STEEMVM_IMAGE from this checkout (a few minutes)…"
  $DOCKER build -t "$STEEMVM_IMAGE" .
fi
# steemvmd against the node home. Only sv_stdin attaches stdin (the key
# import): any other `docker run -i` would swallow input meant for this script.
sv() { $DOCKER run --rm -v "$STEEMVM_HOME:/root/.steemvm" --entrypoint "$BIN" "$STEEMVM_IMAGE" "$@" --home /root/.steemvm; }
sv_stdin() { $DOCKER run --rm -i -v "$STEEMVM_HOME:/root/.steemvm" --entrypoint "$BIN" "$STEEMVM_IMAGE" "$@" --home /root/.steemvm; }
ok "steemvmd $($DOCKER run --rm --entrypoint "$BIN" "$STEEMVM_IMAGE" version)"

# Compose reads the image names from the repo-root .env.
set_env() { touch "$2"; if grep -q "^$1=" "$2"; then sed -i "s|^$1=.*|$1=$3|" "$2"; else printf '%s=%s\n' "$1" "$3" >> "$2"; fi; }
set_env STEEMVM_IMAGE .env "$STEEMVM_IMAGE"
set_env STEEMVM_HOME .env "$STEEMVM_HOME"   # compose mounts exactly this home

# ── 5. node home + validator key ─────────────────────────────────────────────
case "$STEEMVM_HOME" in ""|/) die "refusing STEEMVM_HOME='$STEEMVM_HOME'." ;; esac
# Until the node starts, a failure removes the half-built home (it was empty
# or absent, checked above) so the script can simply be re-run.
NODE_STARTED=0
cleanup() {
  local rc=$?
  if [ "$rc" -ne 0 ] && [ "$NODE_STARTED" = "0" ] && [ -d "$STEEMVM_HOME" ]; then
    rm -rf "$STEEMVM_HOME"
    warn "removed the half-built $STEEMVM_HOME — fix the problem above and re-run."
  fi
}
trap cleanup EXIT
mkdir -p "$STEEMVM_HOME"
log "Initialising the node home…"
sv init "$MONIKER" --chain-id "$CHAIN_ID" --default-denom asteem >/dev/null 2>&1 || die "steemvmd init failed."

if [ -t 0 ]; then
  echo
  echo "Enter @$MONIKER's SteemVM account mnemonic (24 words). It is not shown, logged or saved"
  read -rsp "anywhere except this node's keyring: " MNEMONIC || die "no mnemonic entered."
  echo
else
  # piped in (automation): ./launch-genesis.sh --yes < mnemonic.txt
  read -r MNEMONIC || die "no mnemonic on stdin."
fi
[ "$(printf '%s' "$MNEMONIC" | wc -w)" -ge 12 ] || die "that doesn't look like a mnemonic."
printf '%s\n' "$MNEMONIC" | sv_stdin keys add "$MONIKER" --recover --key-type eth_secp256k1 --keyring-backend "$KEYRING" >/dev/null 2>&1 \
  || { unset MNEMONIC; die "importing the key failed (is the mnemonic correct?)."; }
unset MNEMONIC
ADDRESS="$(sv keys show "$MONIKER" -a --keyring-backend "$KEYRING" | tr -d '\r')"
if [ -n "$EXPECTED_ADDRESS" ] && [ "$ADDRESS" != "$EXPECTED_ADDRESS" ]; then
  die "that mnemonic gives $ADDRESS, not @$MONIKER's $EXPECTED_ADDRESS. Check the mnemonic (or set EXPECTED_ADDRESS)."
fi
ok "Validator account: $ADDRESS"

# ── 6. genesis ───────────────────────────────────────────────────────────────
G="$STEEMVM_HOME/config/genesis.json"
log "Writing genesis parameters, the Steem anchor and @$MONIKER's name link…"
TMP="$(mktemp)"
jq --slurpfile p Instructions/genesis-params.json \
   --arg acct "$MONIKER" --arg addr "$ADDRESS" --arg gw "$GATEWAY" \
   --arg blk "$ANCHOR_BLOCK" --arg bid "$ANCHOR_ID" '
  .app_state = (.app_state * $p[0].app_state)
  | .consensus = (.consensus * $p[0].consensus)
  | .app_state.steembridge.params.relayer_start_block = $blk
  | .app_state.steembridge.params.relayer_start_block_id = $bid
  | .app_state.steembridge.name_registration_count = "1"
  | .app_state.steembridge.name_registration_list = [{
      id: "0", txid: ("genesis-" + $acct), op_index: 0, steem_block: "0",
      steem_timestamp: "1970-01-01T00:00:00", steem_account: $acct, gateway_account: $gw,
      amount_millisteem: "1", memo: "genesis", derived_destination: $addr,
      destination_type: "DESTINATION_TYPE_COSMOS", status: "NAME_REGISTRATION_STATUS_ACTIVE",
      created_at: "0", awaiting_since: "0", confirmed_at: "0", confirm_tx_hash: "",
      validator_confirmations: []
    }]
  | .app_state.steembridge.active_name_list = [{steem_account: $acct, address: $addr, registration_id: "0", linked_at: "0"}]
' "$G" > "$TMP" && mv "$TMP" "$G"

log "Funding @$MONIKER and creating the gentx…"
sv genesis add-genesis-account "$ADDRESS" "${ALLOC_ASBD}asbd,${ALLOC_ASTEEM}asteem" --keyring-backend "$KEYRING" >/dev/null
sv genesis gentx "$MONIKER" "${STAKE_ASTEEM}asteem" --chain-id "$CHAIN_ID" \
  --moniker "$MONIKER" --details "$DETAILS" --ip "$PUBLIC_IP" \
  --commission-rate "$COMMISSION_RATE" --commission-max-rate "$COMMISSION_MAX_RATE" \
  --commission-max-change-rate "$COMMISSION_MAX_CHANGE_RATE" --min-self-delegation 1 \
  --gas "$GENTX_GAS" --fees "$GENTX_FEE" --keyring-backend "$KEYRING" >/dev/null 2>&1 || die "gentx failed."
sv genesis collect-gentxs >/dev/null 2>&1 || die "collect-gentxs failed."
sv genesis validate >/dev/null || die "the genesis does not validate (see above)."
ok "Genesis built and validated."

# ── 7. node config ───────────────────────────────────────────────────────────
[ -f Instructions/app.toml ] && cp Instructions/app.toml "Instructions/app.toml.bak.$(date +%s)"
cp Instructions/app.toml.example Instructions/app.toml
cp Instructions/config.toml.example Instructions/config.toml
sed -i \
  -e "s|^moniker = .*|moniker = \"$MONIKER\"|" \
  -e "s|^persistent_peers = .*|persistent_peers = \"\"|" \
  -e "s|^external_address = .*|external_address = \"$PUBLIC_IP:26656\"|" \
  -e '/^\[statesync\]/,/^\[/{s|^enable = .*|enable = false|}' \
  Instructions/config.toml
for f in app.toml config.toml client.toml; do cp "Instructions/$f" "$STEEMVM_HOME/config/$f"; done
cp "$G" Instructions/genesis.json
GENESIS_SHA="$(sha256sum Instructions/genesis.json | cut -d' ' -f1)"

if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
  ufw allow 26656/tcp >/dev/null && ok "ufw: opened 26656/tcp (p2p)."
fi

# ── 8. start the node ────────────────────────────────────────────────────────
log "Starting the node…"
NODE_STARTED=1   # from here on the home is a live chain: never remove it
$COMPOSE up -d steemvm
node() { $DOCKER exec "$CONTAINER" "$BIN" "$@" --home /root/.steemvm; }
deadline=$(( $(date +%s) + START_TIMEOUT ))
height=0
while [ "$height" -lt 3 ]; do
  [ "$(date +%s)" -lt "$deadline" ] || die "no blocks after ${START_TIMEOUT}s — check: $COMPOSE logs -f steemvm"
  sleep 5
  height="$(node status 2>/dev/null | jq -r '.sync_info.latest_block_height // 0' 2>/dev/null || echo 0)"
  [[ "$height" =~ ^[0-9]+$ ]] || height=0
done
ok "Producing blocks (height $height)."

EVM_CHAIN_ID="$(curl -fsS -m 10 -X POST http://127.0.0.1:8545 -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]}' | jq -r '.result')"
ok "EVM chain-id: $((EVM_CHAIN_ID)) ($EVM_CHAIN_ID)"
ONCHAIN_ANCHOR="$(curl -fsS -m 10 http://127.0.0.1:1317/steemvm/steembridge/v1/params | jq -r '.params.relayer_start_block_id')"
[ "$ONCHAIN_ANCHOR" = "$ANCHOR_ID" ] || die "the chain reports Steem anchor '$ONCHAIN_ANCHOR', expected $ANCHOR_ID."
ok "Steem anchor is on-chain."
NODE_ID="$(node comet show-node-id | tr -d '\r')"

# ── 9. the bridge oracle ─────────────────────────────────────────────────────
if [ "$NO_ORACLE" = "0" ]; then
  if [ "$PULL" = "1" ]; then
    $DOCKER image inspect "$ORACLE_IMAGE" >/dev/null 2>&1 || $DOCKER pull "$ORACLE_IMAGE"
  else
    log "Building $ORACLE_IMAGE…"
    case "$ORACLE_PROFILE" in
      go) $DOCKER build -f oracle/go/Dockerfile -t "$ORACLE_IMAGE" . ;;
      python|js) $DOCKER build -f "oracle/$ORACLE_PROFILE/Dockerfile" -t "$ORACLE_IMAGE" oracle ;;
      *) die "ORACLE_PROFILE must be go, python or js" ;;
    esac
  fi
  set_env "ORACLE_$(printf '%s' "$ORACLE_PROFILE" | tr a-z A-Z)_IMAGE" .env "$ORACLE_IMAGE"
  [ -f oracle/.env ] && mv oracle/.env "oracle/.env.bak.$(date +%s)"
  umask 077
  {
    echo "# Written by launch-genesis.sh for the genesis validator. NEVER commit this file."
    printf 'ORACLE_PRIVATE_KEY=%s\n' "$(node keys unsafe-export-eth-key "$MONIKER" --keyring-backend "$KEYRING" | tail -1 | tr -d '[:space:]')"
    echo "ORACLE_STEEM_RPC=$STEEM_RPC"
    echo "ORACLE_PRICE_SOURCE=$ORACLE_PRICE_SOURCE"
    [ -n "${ORACLE_CMC_API_KEY:-}" ] && echo "ORACLE_CMC_API_KEY=$ORACLE_CMC_API_KEY"
    echo "# No ORACLE_START_BLOCK: the oracle starts at the chain's Steem anchor."
  } > oracle/.env
  umask 022
  log "Starting the $ORACLE_PROFILE oracle…"
  $COMPOSE --profile "$ORACLE_PROFILE" up -d "oracle-$ORACLE_PROFILE"
  ok "Oracle started (logs: $COMPOSE logs -f oracle-$ORACLE_PROFILE)."
fi

# ── 10. summary ──────────────────────────────────────────────────────────────
cat <<EOF

$(ok "Chain $CHAIN_ID is live.")

  Chain-id           $CHAIN_ID
  EVM chain-id       $((EVM_CHAIN_ID))
  Genesis sha256     $GENESIS_SHA
  Steem anchor       block $ANCHOR_BLOCK  $ANCHOR_ID
  Peer               $NODE_ID@$PUBLIC_IP:26656
  Validator          @$MONIKER  $ADDRESS

  BACK UP NOW (off this machine), and never run a second node with these:
    $STEEMVM_HOME/config/priv_validator_key.json   (consensus key: double-signing = slashing)
    $STEEMVM_HOME/config/node_key.json
    $STEEMVM_HOME/keyring-test/                    (the account key, unencrypted)

  Next:
    1. Publish Instructions/genesis.json (sha256 above) to the repository: every
       other validator must use this exact file.
    2. Put the peer string above in Instructions/config.toml.example's
       persistent_peers (and the scripts' peer/seed lists) for joining validators.
EOF
