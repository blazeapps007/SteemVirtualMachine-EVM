#!/usr/bin/env bash
#
# update.sh — update a validator to the checked-out release, cosmovisor-aware.
#
# Usage: ./update.sh [branch-or-tag] [--reset] [--docker | --bare-metal]
#   branch-or-tag   git ref to check out before pulling (default: stay on the
#                   current branch and just pull it)
#   --reset         Docker only: when a coordinated upgrade is pending, also run
#                   the full reset flow below instead of just staging
#   --docker        force Docker mode
#   --bare-metal    force bare-metal mode: the node runs on the host, not in
#                   Docker — build + stage only, see below
#
# Without --docker/--bare-metal the mode is detected: no docker installed ->
# bare metal; a running '$CONTAINER' container -> Docker; no such container
# but cosmovisor/steemvmd running directly on the host -> bare metal; anything
# else is refused (never guessed), with a message saying which flag to add.
# Either way the node home is read from the running node itself (the
# container's mount, or cosmovisor's DAEMON_HOME), not guessed from $HOME.
#
# What it does depends on the checked-out release:
#
#   Docker, coordinated upgrade PENDING (the branch's docker-compose.yml has a
#   `stage-vX.Y.Z` service): stages the new binary into cosmovisor's upgrade
#   slot and exits. No reset, no restart, no downtime — cosmovisor switches to
#   it by itself at the upgrade height. Safe to run any time before or after
#   the upgrade proposal passes. If the node already halted at the upgrade
#   height for lack of the binary, it picks the staged one up on its next
#   automatic restart.
#
#   Docker, no upgrade pending: the full flow —
#     stop node + oracle -> reset chain data (keys untouched) -> refresh
#     persistent_peers -> state-sync from a live peer -> start -> wait for
#     sync -> unjail if needed -> rebuild + restart the oracle.
#
#   Bare metal (--bare-metal): builds the checked-out code into a scratch GOBIN
#   (NEVER overwrites the binary you are running). Under cosmovisor it stages
#   it into $DAEMON_HOME/cosmovisor/upgrades/v<version>/bin. If steemvmd runs
#   directly (no cosmovisor), it saves it to $HOME/steemvmd-v<version>/ and
#   prints the steps for swapping it in by hand at the upgrade height. Never
#   stops or restarts anything. Needs go + make; no Docker, curl or jq.
#
# Overridable via env: COMPOSE, NODE_IPS, SELF_IP, P2P_PORT, RPC_PORT,
# STEEMVM_HOME, CONTAINER, BIN, HOME_DIR, CHAIN_ID, KEYRING, GAS_PRICES,
# VALIDATOR_KEY (keyring key name used for the unjail check — auto-detected
# if you have exactly one key), ORACLE_PROFILE (go|python|js — auto-detected
# from whatever's currently running if not set), START_TIMEOUT,
# DAEMON_HOME (bare metal; default $HOME/.steemvm)

if [ -z "${BASH_VERSION:-}" ]; then
  if command -v bash >/dev/null 2>&1; then
    exec bash "$0" "$@"
  else
    echo "ERROR: this script requires bash." >&2
    exit 1
  fi
fi

set -euo pipefail

COMPOSE="${COMPOSE:-docker compose}"
CONTAINER="${CONTAINER:-steemvm-node}"
BIN="${BIN:-/root/go/bin/steemvmd}"
HOME_DIR="${HOME_DIR:-/root/.steemvm}"
STEEMVM_HOME_FROM_ENV="${STEEMVM_HOME:-}"   # set explicitly by the operator?
STEEMVM_HOME="${STEEMVM_HOME:-$HOME/.steemvm}"
export STEEMVM_HOME
CHAIN_ID="${CHAIN_ID:-steemvm}"
KEYRING="${KEYRING:-test}"
GAS_PRICES="${GAS_PRICES:-1000000000asteem}"
VALIDATOR_KEY="${VALIDATOR_KEY:-}"
ORACLE_PROFILE="${ORACLE_PROFILE:-}"

NODE_IPS="${NODE_IPS:-95.217.44.178 62.169.19.142 57.131.13.43 167.235.9.31}"
export NODE_IPS
P2P_PORT="${P2P_PORT:-26656}"
RPC_PORT="${RPC_PORT:-26657}"
export P2P_PORT RPC_PORT
MIN_STATESYNC_HEIGHT=1500
START_TIMEOUT="${START_TIMEOUT:-1800}"

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m✔\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

node() { docker exec "$CONTAINER" "$BIN" "$@"; }
node_i() { docker exec -it "$CONTAINER" "$BIN" "$@"; }
kf() { node "$@" --keyring-backend "$KEYRING" --home "$HOME_DIR"; }
kf_i() { node_i "$@" --keyring-backend "$KEYRING" --home "$HOME_DIR"; }

# ── args ──────────────────────────────────────────────────────────────────────
TARGET_REF="" FORCE_RESET=0 MODE=auto
for arg in "$@"; do
  case "$arg" in
    --reset)      FORCE_RESET=1 ;;
    --docker)     MODE=docker ;;
    --bare-metal) MODE=bare-metal ;;
    -h|--help)    sed -n '2,/^$/p' "$0"; exit 0 ;;
    -*)           die "unknown flag: $arg (see ./update.sh --help)" ;;
    *)            [ -z "$TARGET_REF" ] || die "only one branch-or-tag may be given"; TARGET_REF="$arg" ;;
  esac
done

[ -d .git ] || die "not a git checkout — run this from the repository root."
[ -f docker-compose.yml ] || die "run this from the repository root."

# Node processes running directly on THIS host. Processes inside containers
# are visible from the host too, so they are excluded by their cgroup.
on_host() { [ -r "/proc/$1/cgroup" ] && ! grep -qiE 'docker|containerd|kubepods|libpod|lxc' "/proc/$1/cgroup"; }
host_cosmovisor_pid() { local p; for p in $(pgrep -x cosmovisor 2>/dev/null); do on_host "$p" && { echo "$p"; return 0; }; done; return 1; }
host_steemvmd_pid()   { local p; for p in $(pgrep -f 'steemvmd start' 2>/dev/null); do on_host "$p" && { echo "$p"; return 0; }; done; return 1; }

# ── Docker or bare metal? — decided before anything changes ─────────────────
# Deliberately conservative: staging the Docker image's binary into a
# bare-metal node's cosmovisor (it may not even run on the host's OS) or a
# host-built binary into a container would leave a node that can't start at
# the upgrade height. So only decide when it's unambiguous; otherwise ask.
if [ "$MODE" = "auto" ]; then
  if ! command -v docker >/dev/null 2>&1; then
    MODE=bare-metal
  elif docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "$CONTAINER"; then
    MODE=docker   # a RUNNING node container — a stopped leftover proves nothing
  elif host_cosmovisor_pid >/dev/null || host_steemvmd_pid >/dev/null; then
    MODE=bare-metal   # the node binary runs directly on this host (docker is used for other things)
  elif docker ps -a --format '{{.Names}}' 2>/dev/null | grep -qx "$CONTAINER"; then
    die "the '$CONTAINER' container exists but isn't running, so this can't tell how the node runs. If it runs in Docker, re-run with --docker; if it runs steemvmd directly, use --bare-metal. Nothing was changed."
  else
    die "can't tell how this node runs: docker is installed, there is no '$CONTAINER' container, and no cosmovisor/steemvmd process is running on this host. Re-run with --docker or --bare-metal. Nothing was changed."
  fi
  log "Detected a $MODE node."
fi
BARE_METAL=0
[ "$MODE" = "bare-metal" ] && BARE_METAL=1
[ "$BARE_METAL" = "1" ] && [ "$FORCE_RESET" = "1" ] && die "--reset is Docker-only; bare metal only ever builds + stages."

# ── 1. git checkout + pull ───────────────────────────────────────────────────
[ -z "$(git status --porcelain)" ] || die "you have local changes (git status is not clean) — commit, stash, or discard them first."

log "Fetching…"
# Plain fetch, errors visible. NOT --tags: the repo has a moving "latest"
# tag, and `git fetch --tags` refuses ("would clobber existing tag") on any
# clone holding an older copy of it — which failed silently under --quiet.
git fetch origin || die "git fetch failed (see the error above). Nothing was changed."
if [ -n "$TARGET_REF" ]; then
  # A tag we don't have yet? Fetch just that one (forced — tags can move).
  if ! git rev-parse -q --verify "$TARGET_REF^{commit}" >/dev/null \
     && ! git rev-parse -q --verify "origin/$TARGET_REF^{commit}" >/dev/null; then
    git fetch origin "+refs/tags/$TARGET_REF:refs/tags/$TARGET_REF" 2>/dev/null \
      || die "'$TARGET_REF' is neither a branch nor a tag on origin. Nothing was changed."
  fi
  log "Checking out $TARGET_REF…"
  git checkout "$TARGET_REF"
fi
if git symbolic-ref -q HEAD >/dev/null; then
  log "Pulling latest…"
  git pull
else
  warn "detached HEAD (on a tag, not a branch) — skipping pull, already at $(git rev-parse --short HEAD)."
fi
ok "At $(git rev-parse --short HEAD) ($(git describe --tags --always 2>/dev/null))."

# ══ Bare metal: build + stage into cosmovisor, nothing else ════════════════════
if [ "$BARE_METAL" = "1" ]; then
  command -v go   >/dev/null 2>&1 || die "go not found on PATH."
  command -v make >/dev/null 2>&1 || die "make not found on PATH."

  # Read how the RUNNING node actually runs instead of guessing from $HOME:
  # running this as root on a node that runs as another user (e.g. ubuntu)
  # would otherwise stage into a home cosmovisor never looks at.
  DIRECT=0 SDPID=""
  if [ -z "${DAEMON_HOME:-}" ]; then
    if cvpid="$(host_cosmovisor_pid)"; then
      DAEMON_HOME="$(tr '\0' '\n' < "/proc/$cvpid/environ" 2>/dev/null | sed -n 's/^DAEMON_HOME=//p' | head -1 || true)"
      if [ -n "$DAEMON_HOME" ]; then
        log "Node home (from the running cosmovisor, pid $cvpid): $DAEMON_HOME"
      else
        warn "cosmovisor is running (pid $cvpid) but its DAEMON_HOME can't be read (run as root, or set DAEMON_HOME) — assuming $HOME/.steemvm."
      fi
    fi
  fi
  DAEMON_HOME="${DAEMON_HOME:-$HOME/.steemvm}"
  CV="$DAEMON_HOME/cosmovisor"

  if [ ! -e "$CV/current/bin/steemvmd" ]; then
    # No cosmovisor home. If steemvmd runs directly (and no cosmovisor does),
    # it can't switch binaries by itself — prepare a manual swap instead.
    if SDPID="$(host_steemvmd_pid)" && ! host_cosmovisor_pid >/dev/null; then
      DIRECT=1
    else
      die "$CV/current/bin/steemvmd not found, and no steemvmd process is running on this host — DAEMON_HOME is probably wrong (set it to the node's --home). Nothing was changed."
    fi
  fi

  if [ "$DIRECT" = "1" ]; then
    LIVE_BIN="$(readlink -f "/proc/$SDPID/exe" 2>/dev/null || true)"
    [ -n "$LIVE_BIN" ] || die "steemvmd runs (pid $SDPID) but its binary path can't be read — run this as root. Nothing was changed."
    # The node's --home, from its own command line (either --home X or --home=X).
    NODE_HOME="$(tr '\0' '\n' < "/proc/$SDPID/cmdline" | awk 'p=="--home"{print; exit} /^--home=/{sub(/^--home=/,""); print; exit} {p=$0}')"
    NODE_HOME="${NODE_HOME:-$HOME/.steemvm}"
    RUNNING_VER="$("$LIVE_BIN" version 2>/dev/null || echo unknown)"
    warn "steemvmd runs directly (pid $SDPID: $LIVE_BIN, version $RUNNING_VER, home $NODE_HOME) — NOT under cosmovisor, so it can't switch binaries by itself."
  else
    RUNNING_VER="$("$CV/current/bin/steemvmd" version 2>/dev/null || echo unknown)"
    log "cosmovisor currently runs $RUNNING_VER ($(readlink "$CV/current"))."
  fi

  # `make install` always installs to $GOBIN — point it at a scratch dir so the
  # build can never overwrite the binary the node is running.
  BUILD_DIR="$(mktemp -d)"
  trap 'rm -rf "$BUILD_DIR"' EXIT
  log "Building the checked-out code into $BUILD_DIR…"
  GOBIN="$BUILD_DIR" make install
  NEW_VER="$("$BUILD_DIR/steemvmd" version)"
  [ -n "$NEW_VER" ] || die "the freshly built steemvmd reports no version."

  if [ "$NEW_VER" = "$RUNNING_VER" ]; then
    ok "the node already runs $NEW_VER — nothing to do."
    exit 0
  fi

  if [ "$DIRECT" = "1" ]; then
    OUT_DIR="$HOME/steemvmd-v$NEW_VER"
    mkdir -p "$OUT_DIR"
    cp "$BUILD_DIR/steemvmd" "$OUT_DIR/steemvmd"
    chmod +x "$OUT_DIR/steemvmd"
    [ "$("$OUT_DIR/steemvmd" version)" = "$NEW_VER" ] || die "built binary does not report $NEW_VER."
    ok "Built v$NEW_VER -> $OUT_DIR/steemvmd (your running $LIVE_BIN is untouched)."
    log "Upgrade plan on chain:"
    "$LIVE_BIN" query upgrade plan --home "$NODE_HOME" 2>&1 | sed 's/^/    /' || true
    echo
    warn "This node runs steemvmd directly, so YOU must switch the binary at the upgrade height."
    echo "   When the node halts there (its log says: UPGRADE \"v$NEW_VER\" NEEDED at height: …):"
    echo "     1. stop it the way you normally do (systemctl stop <service>, or kill $SDPID)"
    echo "     2. cp $OUT_DIR/steemvmd $LIVE_BIN"
    echo "     3. start it again exactly as before (same command, same --home $NODE_HOME)"
    echo "   Every minute between the halt and the restart counts toward downtime jailing."
    echo
    echo "   Recommended instead: move this node under cosmovisor now (one restart today,"
    echo "   none at the height), then re-run ./update.sh and it stages automatically."
    echo "   See Instructions/README.md, 'Run under cosmovisor'."
    exit 0
  fi

  DEST="$CV/upgrades/v$NEW_VER/bin"
  mkdir -p "$DEST"
  cp "$BUILD_DIR/steemvmd" "$DEST/steemvmd"
  chmod +x "$DEST/steemvmd"
  [ "$("$DEST/steemvmd" version)" = "$NEW_VER" ] || die "staged binary does not report $NEW_VER — do not rely on this stage."
  ok "Staged $NEW_VER -> $DEST/steemvmd"

  log "Upgrade plan on chain:"
  "$CV/current/bin/steemvmd" query upgrade plan --home "$DAEMON_HOME" 2>&1 | sed 's/^/    /' || true
  echo
  ok "Done. Leave the node running: at the upgrade height the old binary halts and"
  echo "   cosmovisor switches to v$NEW_VER by itself. Its environment must include"
  echo "   DAEMON_NAME=steemvmd DAEMON_HOME=$DAEMON_HOME DAEMON_RESTART_AFTER_UPGRADE=true."
  exit 0
fi

# ══ Docker ═════════════════════════════════════════════════════════════════════
command -v docker >/dev/null || die "docker not found on PATH (for a node that runs steemvmd directly, use --bare-metal)"

# Use the node home the RUNNING container actually mounts, not a guess from
# $HOME: running this as root on a node whose compose was started by another
# user would otherwise stage the binary into the wrong home.
LIVE_HOME="$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "'"$HOME_DIR"'"}}{{.Source}}{{end}}{{end}}' "$CONTAINER" 2>/dev/null || true)"
if [ -n "$LIVE_HOME" ]; then
  if [ -n "$STEEMVM_HOME_FROM_ENV" ] && [ "$STEEMVM_HOME_FROM_ENV" != "$LIVE_HOME" ]; then
    die "STEEMVM_HOME=$STEEMVM_HOME_FROM_ENV, but the running '$CONTAINER' uses $LIVE_HOME. Unset STEEMVM_HOME (or fix it). Nothing was changed."
  fi
  STEEMVM_HOME="$LIVE_HOME"
  export STEEMVM_HOME
  log "Node home (from the running container): $STEEMVM_HOME"
fi
$COMPOSE version >/dev/null 2>&1 || die "'$COMPOSE' not available (set COMPOSE=docker-compose ?)"

CONFIG_TOML="$STEEMVM_HOME/config/config.toml"
KEY_FILE="$STEEMVM_HOME/config/priv_validator_key.json"
[ -f "$KEY_FILE" ] || die "$KEY_FILE not found — this isn't an existing validator home. Use new-validator.sh for a first-time setup instead."

# ── 2. stage any pending coordinated upgrade into cosmovisor ────────────────
# A release branch declares a pending upgrade by carrying a `stage-vX.Y.Z`
# service (profile "stage") in docker-compose.yml.
SERVICES="$($COMPOSE --profile stage config --services)" || die "docker-compose.yml does not parse — fix it before updating."
mapfile -t PENDING < <(printf '%s\n' "$SERVICES" | grep '^stage-v' | sort -V || true)

# Which binary cosmovisor should run after a reset: "genesis" means the image's
# own binary (docker-entrypoint.sh refreshes genesis/bin from the image
# whenever `current` points at genesis). Overridden below if a pending upgrade
# has in fact already applied on-chain.
CURRENT_TARGET="genesis"

if [ "${#PENDING[@]}" -gt 0 ]; then
  for svc in "${PENDING[@]}"; do
    log "Coordinated upgrade pending: staging ${svc#stage-} into cosmovisor…"
    # Always pull first: a copy of the image cached from an earlier pull may be
    # an older build that reports the SAME version string. Staging that would
    # split this node off the network at the upgrade height.
    $COMPOSE --profile stage pull "$svc" || die "could not pull the ${svc#stage-} image — nothing was touched. Check your connection to Docker Hub."
    $COMPOSE --profile stage run --rm "$svc" || die "staging ${svc#stage-} failed — nothing else was touched."
  done
  ok "Staged: ${PENDING[*]#stage-}"

  if docker exec "$CONTAINER" true 2>/dev/null; then
    log "cosmovisor currently runs: $(docker exec "$CONTAINER" readlink /root/.steemvm/cosmovisor/current 2>/dev/null || echo '?')"
    log "Upgrade plan on chain:"
    node query upgrade plan --home "$HOME_DIR" 2>&1 | sed 's/^/    /' || true
  else
    warn "node container '$CONTAINER' is not running — can't show the on-chain plan."
  fi

  if [ "$FORCE_RESET" != "1" ]; then
    echo
    ok "Done — no reset, no restart. Leave the node running: at the upgrade height"
    echo "   cosmovisor switches to the staged binary by itself."
    echo "   Do NOT change the steemvm image or 'docker compose pull && up' before then."
    exit 0
  fi

  # --reset with an upgrade pending: the right binary after a state-sync depends
  # on whether that upgrade has already applied. Ask the node before stopping it.
  docker exec "$CONTAINER" true 2>/dev/null || die "--reset during a pending upgrade needs the node running (to check whether it already applied). Start it, or run without --reset."
  for svc in "${PENDING[@]}"; do
    name="${svc#stage-}"
    # Not applied prints `{}` with exit 0 (height 0 is omitted), applied prints
    # {"height":"N"} — so parse the height, never trust the exit code alone.
    out="$(node query upgrade applied "$name" --home "$HOME_DIR" -o json 2>&1)" \
      || die "could not ask the node whether $name has applied ($out) — refusing to reset. Run without --reset."
    applied_h="$(printf '%s' "$out" | grep -o '"height": *"[0-9]*"' | grep -o '[0-9][0-9]*' || true)"
    if [ -n "$applied_h" ] && [ "$applied_h" -gt 0 ]; then
      CURRENT_TARGET="upgrades/$name"
      ok "$name applied on-chain at height $applied_h — the reset node will run $name."
    elif [ "$(printf '%s' "$out" | tr -d ' \n')" = "{}" ]; then
      ok "$name has not applied yet — the reset node runs the current binary until the upgrade height."
    else
      die "unexpected answer about $name ($out) — refusing to reset. Run without --reset."
    fi
  done
fi

# Only the full flow needs these (staging above doesn't).
command -v curl >/dev/null 2>&1 || die "curl is required."
command -v jq   >/dev/null 2>&1 || die "jq is required."
[ -f update_peers.sh ] || die "update_peers.sh not found next to this script."

# ── 3. state-sync trust anchor — BEFORE touching anything ───────────────────
# Replaying from genesis is no longer an option once a chain has been through a
# coordinated upgrade: no single binary can process both sides of an upgrade
# height (x/upgrade halts the old one at it and refuses the new one before
# it). So without a trust anchor there is no way to resync — find out now,
# while the node's data is still intact.
fetch_statesync_trust_from_nodes() {
  TRUST_HEIGHT="" TRUST_HASH="" TRUST_SEED=""
  local ip latest h hash
  for ip in $NODE_IPS; do
    latest="$(curl -fsS --max-time 5 "http://${ip}:${RPC_PORT}/status" 2>/dev/null | jq -r '.result.sync_info.latest_block_height // empty' 2>/dev/null)"
    [ -n "$latest" ] || continue
    [ "$latest" -ge "$MIN_STATESYNC_HEIGHT" ] || continue
    h=$((latest - 1000)); [ "$h" -lt 1 ] && h=1
    hash="$(curl -fsS --max-time 5 "http://${ip}:${RPC_PORT}/commit?height=$h" 2>/dev/null | jq -r '.result.signed_header.commit.block_id.hash // empty' 2>/dev/null)"
    [ -n "$hash" ] || continue
    TRUST_HEIGHT="$h" TRUST_HASH="$hash" TRUST_SEED="$ip"
    return 0
  done
  return 1
}

fetch_statesync_trust_from_nodes || die "no state-sync trust anchor from any of: $NODE_IPS — refusing to wipe chain data that could not be resynced. Nothing was touched. (Needs at least one of them serving CometBFT RPC on :$RPC_PORT; set NODE_IPS/RPC_PORT to reachable peers.)"
ok "State-sync trust anchor from $TRUST_SEED: height $TRUST_HEIGHT."

# ── 4. detect a currently-running oracle profile (before we stop anything) ──
if [ -z "$ORACLE_PROFILE" ]; then
  RUNNING="$(docker ps --format '{{.Names}}' 2>/dev/null || true)"
  if printf '%s' "$RUNNING" | grep -qx 'steemvm-oracle-go'; then ORACLE_PROFILE=go
  elif printf '%s' "$RUNNING" | grep -qx 'steemvm-oracle-python'; then ORACLE_PROFILE=python
  elif printf '%s' "$RUNNING" | grep -qx 'steemvm-oracle-js'; then ORACLE_PROFILE=js
  fi
fi
if [ -n "$ORACLE_PROFILE" ]; then
  ok "Oracle profile currently running: $ORACLE_PROFILE (will rebuild + restart it)."
else
  warn "no oracle container currently running — none will be started. Set ORACLE_PROFILE=go|python|js to force one."
fi

# ── 5. stop everything (node + oracle, whichever is up) ─────────────────────
log "Stopping the node (and oracle, if running)…"
$COMPOSE down

# ── 6. reset chain data — keys untouched — and point cosmovisor at the right binary
log "Resetting chain data (keys are never touched)…"
# shellcheck disable=SC2086
$COMPOSE run --rm -T --entrypoint "$BIN" steemvm comet unsafe-reset-all --home "$HOME_DIR" --keep-addr-book
[ -f "$KEY_FILE" ] || die "priv_validator_key.json is gone after reset — STOP and investigate before starting the node."
ok "Chain data reset. priv_validator_key.json still present."

# The node is about to state-sync to the chain tip, so cosmovisor must run the
# binary for the chain's CURRENT version — not whatever `current` pointed at
# before (e.g. an old binary on a node that was offline through an upgrade).
log "Pointing cosmovisor at $CURRENT_TARGET…"
$COMPOSE run --rm -T --entrypoint sh steemvm -c "
  set -e
  C=$HOME_DIR/cosmovisor
  [ -d \"\$C\" ] || exit 0
  [ '$CURRENT_TARGET' = genesis ] || [ -x \"\$C/$CURRENT_TARGET/bin/steemvmd\" ] || { echo \"missing \$C/$CURRENT_TARGET/bin/steemvmd\" >&2; exit 1; }
  ln -sfn '$CURRENT_TARGET' \"\$C/current\"
" || die "could not point cosmovisor at $CURRENT_TARGET."
ok "cosmovisor will run $CURRENT_TARGET."

# ── 7. refresh persistent_peers ──────────────────────────────────────────────
log "Refreshing persistent_peers…"
./update_peers.sh --no-restart-hint

# ── 8. write the state-sync trust anchor into config.toml ───────────────────
[ -f "$CONFIG_TOML" ] || die "$CONFIG_TOML not found."
sed -i.bak \
  -e '/^\[statesync\]/,/^\[/{s/^enable = false/enable = true/}' \
  -e "/^\[statesync\]/,/^\[/{s/^trust_height = .*/trust_height = $TRUST_HEIGHT/}" \
  -e "/^\[statesync\]/,/^\[/{s/^trust_hash = .*/trust_hash = \"$TRUST_HASH\"/}" \
  "$CONFIG_TOML"
rm -f "$CONFIG_TOML.bak"
ok "State-sync enabled in $CONFIG_TOML."

# ── 9. pull the latest published image and start the node ──────────────────
log "Pulling the latest node image…"
$COMPOSE pull steemvm || warn "image pull failed — continuing with whatever is cached locally."
log "Starting the node…"
$COMPOSE up -d

log "Waiting for the node to catch up (timeout ${START_TIMEOUT}s)…"
deadline=$(( $(date +%s) + START_TIMEOUT ))
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
ok "Node is caught up (height $height)."

# ── 10. unjail if needed ──────────────────────────────────────────────────────
if [ -z "$VALIDATOR_KEY" ]; then
  VALIDATOR_KEY="$(kf keys list --output json 2>/dev/null | jq -r 'if length==1 then .[0].name else empty end' 2>/dev/null || true)"
fi
if [ -n "$VALIDATOR_KEY" ]; then
  VALOPER="$(kf keys show "$VALIDATOR_KEY" --bech val -a 2>/dev/null | tr -d '\r' || true)"
  if [ -n "$VALOPER" ]; then
    JAILED="$(kf query staking validator "$VALOPER" --output json 2>/dev/null | jq -r '.jailed // empty' 2>/dev/null || true)"
    if [ "$JAILED" = "true" ]; then
      log "$VALOPER is jailed — unjailing…"
      set +e
      kf_i tx slashing unjail --from "$VALIDATOR_KEY" --chain-id "$CHAIN_ID" \
        --gas auto --gas-adjustment 1.5 --gas-prices "$GAS_PRICES" -y
      rc=$?
      set -e
      if [ $rc -eq 0 ]; then ok "Unjail tx broadcast."; else warn "unjail tx failed — check manually: kf query slashing signing-info <consensus-pubkey>"; fi
    else
      ok "$VALOPER is not jailed."
    fi
  else
    warn "could not resolve a valoper address for key '$VALIDATOR_KEY' — skipping the unjail check. Run it manually if needed."
  fi
else
  warn "could not auto-detect your validator's keyring key (found none, or more than one) — set VALIDATOR_KEY=<name> to enable the automatic unjail check. Skipping."
fi

# ── 11. rebuild + restart the oracle, if one was running ────────────────────
if [ -n "$ORACLE_PROFILE" ]; then
  log "Rebuilding and restarting the $ORACLE_PROFILE oracle (picks up any oracle code changes)…"
  $COMPOSE --profile "$ORACLE_PROFILE" up -d --build
  ok "Oracle restarted."
fi

# ── 12. verification checklist ────────────────────────────────────────────────
echo
ok "Update complete. Verify:"
echo "  version:        docker exec $CONTAINER $BIN version"
echo "  cosmovisor:     docker exec $CONTAINER readlink /root/.steemvm/cosmovisor/current"
echo "  sync status:    docker exec $CONTAINER $BIN status | jq '.sync_info'"
echo "  peer count:     curl -s http://localhost:26657/net_info | jq '.result.n_peers'"
echo "  bonded/jailed:  docker exec $CONTAINER $BIN query staking validator \$($COMPOSE exec -T steemvm $BIN keys show ${VALIDATOR_KEY:-<your-key>} --bech val -a --keyring-backend $KEYRING --home $HOME_DIR)"
if [ -n "$ORACLE_PROFILE" ]; then
  echo "  oracle logs:    $COMPOSE logs -f oracle-$ORACLE_PROFILE"
fi
echo "  node logs:      $COMPOSE logs -f steemvm"
