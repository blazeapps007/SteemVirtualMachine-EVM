#!/usr/bin/env bash
#
# install.sh — one-command SteemVM validator setup on a fresh Ubuntu/Debian
# server, e.g. a DigitalOcean Droplet. Paste this into the server's console:
#
#   curl -fsSL https://raw.githubusercontent.com/blazeapps007/SteemVirtualMachine-EVM/release/v0.0.6/install.sh | sudo bash
#
# It installs Docker (with the compose plugin), git, curl and jq, opens the p2p
# port if ufw is active, clones this repository to /opt/steemvm, asks for your
# Steem username and Steem ACTIVE private key, and then runs new-validator.sh
# with those answers (CoinGecko prices, the public steemvm-2 RPC). The key is
# typed into this terminal only: it is never put on a command line and goes
# nowhere but this server's oracle/.env (mode 600).
#
# Env: STEEMVM_REPO, STEEMVM_REF (branch or tag), STEEMVM_DIR, and anything
# new-validator.sh accepts (STAKE_AMOUNT, ORACLE_PROFILE, PRICE_SOURCE, …).

if [ -z "${BASH_VERSION:-}" ]; then
  echo "ERROR: run this with bash: curl -fsSL <url> | sudo bash" >&2
  exit 1
fi

set -euo pipefail

STEEMVM_REPO="${STEEMVM_REPO:-https://github.com/blazeapps007/SteemVirtualMachine-EVM.git}"
STEEMVM_REF="${STEEMVM_REF:-release/v0.0.6}"
STEEMVM_DIR="${STEEMVM_DIR:-/opt/steemvm}"
STEEM_RPC="${STEEM_RPC:-https://api.steemit.com}"

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m✔\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

valid_steem_name() {
  local seg segs
  [ "${#1}" -ge 3 ] && [ "${#1}" -le 16 ] || return 1
  [ "${1: -1}" != "." ] || return 1
  IFS=. read -ra segs <<< "$1"
  for seg in "${segs[@]}"; do
    [[ "$seg" =~ ^[a-z][a-z0-9-]+[a-z0-9]$ ]] && [[ "$seg" != *--* ]] || return 1
  done
}

[ "$(id -u)" = "0" ] || die "run as root: curl -fsSL <url> | sudo bash"
# The prompts (here and in new-validator.sh) read the terminal, not the pipe.
{ : < /dev/tty; } 2>/dev/null || die "no terminal — run this in an interactive shell (SSH, or the Droplet Console)."
command -v apt-get >/dev/null 2>&1 || die "this installer supports Ubuntu/Debian (apt). On other systems follow Instructions/README.md."

if [ -f /root/.steemvm/config/priv_validator_key.json ]; then
  die "this server already has a SteemVM node (/root/.steemvm). To finish a setup that stopped part-way: cd $STEEMVM_DIR && ./reset.sh --rejoin-only"
fi

# ── 1. the two answers ───────────────────────────────────────────────────────
echo
log "SteemVM validator setup"
read -rp "Steem username (becomes your validator name): " STEEM_USERNAME < /dev/tty
STEEM_USERNAME="$(printf '%s' "$STEEM_USERNAME" | tr -d '[:space:]@' | tr 'A-Z' 'a-z')"
valid_steem_name "$STEEM_USERNAME" || die "'$STEEM_USERNAME' is not a valid Steem account name."
command -v curl >/dev/null 2>&1 || { apt-get update -qq && apt-get install -y -qq curl >/dev/null; }
curl -fsS -m 30 "$STEEM_RPC" -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"condenser_api.get_accounts\",\"params\":[[\"$STEEM_USERNAME\"]]}" \
  | grep -q "\"name\":\"$STEEM_USERNAME\"" || die "Steem account @$STEEM_USERNAME not found."
ok "Steem account @$STEEM_USERNAME found."

echo
log "Your Steem ACTIVE private key for @$STEEM_USERNAME (starts with 5)."
log "It pays the 0.001 STEEM name registration and runs your oracle's svm.bank signer."
log "Never the owner key, posting key or master password."
read -rp "Steem active key: " ACTIVE_WIF < /dev/tty
ACTIVE_WIF="$(printf '%s' "$ACTIVE_WIF" | tr -d '[:space:]')"
[[ "$ACTIVE_WIF" =~ ^5[1-9A-HJ-NP-Za-km-z]{50}$ ]] || die "that is not a Steem private key (51 base58 characters starting with 5)."
ok "Key format OK — it is checked against @$STEEM_USERNAME's active authority once Docker is installed."

# ── 2. packages ──────────────────────────────────────────────────────────────
export DEBIAN_FRONTEND=noninteractive
log "Installing git, curl, jq…"
apt-get update -qq
apt-get install -y -qq ca-certificates curl git jq >/dev/null
if ! docker compose version >/dev/null 2>&1; then
  log "Installing Docker (get.docker.com)…"
  curl -fsSL https://get.docker.com | sh >/dev/null
fi
systemctl enable --now docker >/dev/null 2>&1 || true
docker compose version >/dev/null 2>&1 || die "Docker's compose plugin is not available after installing Docker."
ok "$(docker --version | cut -d, -f1), $(docker compose version --short 2>/dev/null || echo compose)."

MEM_MB="$(awk '/MemTotal/ {print int($2/1024)}' /proc/meminfo)"
DISK_GB="$(df -BG --output=avail / | tail -1 | tr -dc '0-9')"
[ "$MEM_MB" -ge 3500 ] || warn "only ${MEM_MB} MB of RAM — a validator wants at least 4 GB (8 GB recommended)."
[ "$DISK_GB" -ge 40 ] || warn "only ${DISK_GB} GB of free disk — a validator wants at least 80 GB."

# p2p must be reachable from other validators.
if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q '^Status: active'; then
  ufw allow OpenSSH >/dev/null
  ufw allow 26656/tcp >/dev/null
  ok "ufw: opened 26656/tcp (p2p)."
fi

# ── 3. the repository ────────────────────────────────────────────────────────
if [ -d "$STEEMVM_DIR/.git" ]; then
  log "Updating $STEEMVM_DIR ($STEEMVM_REF)…"
  git -C "$STEEMVM_DIR" fetch -q origin
  git -C "$STEEMVM_DIR" checkout -q "$STEEMVM_REF"
  git -C "$STEEMVM_DIR" pull -q --ff-only || warn "could not fast-forward $STEEMVM_DIR — using it as it is."
else
  log "Cloning $STEEMVM_REPO ($STEEMVM_REF) to $STEEMVM_DIR…"
  git clone -q --branch "$STEEMVM_REF" "$STEEMVM_REPO" "$STEEMVM_DIR"
fi
ok "At $(git -C "$STEEMVM_DIR" rev-parse --short HEAD)."

# ── 4. new-validator.sh with the answers preset ─────────────────────────────
cd "$STEEMVM_DIR"
log "Starting the validator setup (new-validator.sh)…"
STEEM_USERNAME="$STEEM_USERNAME" \
ORACLE_STEEM_ACTIVE_WIF="$ACTIVE_WIF" \
PRICE_SOURCE="${PRICE_SOURCE:-coingecko}" \
SEED_RPC="${SEED_RPC:-https://rpc.steemvmscan.com}" \
STEEM_RPC="$STEEM_RPC" \
  exec bash ./new-validator.sh
