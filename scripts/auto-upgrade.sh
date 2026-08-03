#!/usr/bin/env bash
#
# Automatic, HEIGHT-triggered upgrade runner for SteemVM validators.
#
# Run once, ahead of the upgrade:   UPGRADE_HEIGHT=254132 bash auto-upgrade.sh install
# It installs a per-minute cron that watches THIS node's height and, the instant
# the chain halts at the scheduled upgrade block, performs:
#     git pull  ->  docker compose down (NO -v)  ->  docker compose up -d
# which rebuilds the new binary and lets it apply the upgrade. Then it stops.
#
# SAFE BY DESIGN — it swaps ONLY when the node has reached UPGRADE_HEIGHT-1 (the
# old binary cannot commit past that) OR has already panicked with the
# `UPGRADE "<name>" NEEDED` halt. It never pulls the new code or restarts before
# the height, so it cannot fork the node early. It triggers on the block height,
# not the clock, so slow blocks / drift just make it wait longer — no rescheduling.
#
# Commands:
#   install     install the cron watcher (needs UPGRADE_HEIGHT)
#   uninstall   remove the cron watcher
#   status      show height / progress
#   run         one watch tick (invoked by cron; safe to run by hand to test)
#
# Requires: bash, git, jq, flock, curl, docker (run with `bash`, not `sh`).
#
set -euo pipefail

# ---- config (override via env) ---------------------------------------------
UPGRADE_HEIGHT="${UPGRADE_HEIGHT:-254132}"      # the scheduled upgrade block
UPGRADE_NAME="${UPGRADE_NAME:-v0.0.2-Beta1}"    # matches the on-chain plan name
RPC="${RPC:-http://localhost:26657}"            # local CometBFT RPC
CONTAINER="${CONTAINER:-steemvm-node}"          # node container (for halt-log check)
GIT_REMOTE="${GIT_REMOTE:-origin}"
GIT_BRANCH="${GIT_BRANCH:-main}"
NOT_BEFORE="${NOT_BEFORE:-}"                     # optional 'YYYY-MM-DD HH:MM' UTC gate

SCRIPT="$(readlink -f "$0")"

# Repo (must contain docker-compose.yml). Auto-detected if the script lives in
# <repo>/scripts; otherwise set REPO_DIR explicitly (e.g. when curled standalone).
REPO_DIR="${REPO_DIR:-}"
if [ -z "$REPO_DIR" ]; then
  cand="$(cd "$(dirname "$SCRIPT")/.." 2>/dev/null && pwd || true)"
  [ -f "$cand/docker-compose.yml" ] && REPO_DIR="$cand"
fi

WORKDIR="${SVM_UPGRADE_DIR:-$HOME/.svm-upgrade}"
mkdir -p "$WORKDIR"
LOG="$WORKDIR/watch.log"
LOCK="$WORKDIR/watch.lock"
DONE="$WORKDIR/done-${UPGRADE_NAME}"
STATE="$WORKDIR/last-height"

log() { echo "$(date -u +%FT%TZ) $*" | tee -a "$LOG" >&2; }

require_repo() {
  [ -n "$REPO_DIR" ] && [ -f "$REPO_DIR/docker-compose.yml" ] || {
    echo "ERROR: set REPO_DIR to your SteemVM checkout (the dir with docker-compose.yml)." >&2
    exit 1
  }
}

local_height() {
  # committed height of THIS node; empty if the RPC is unreachable (halted/down)
  curl -sf --max-time 5 "$RPC/status" 2>/dev/null \
    | jq -r '.result.sync_info.latest_block_height // empty' 2>/dev/null || true
}

halt_panic_seen() {
  # definitive: the old binary panicked with the scheduled-upgrade halt
  docker logs --tail 200 "$CONTAINER" 2>&1 | grep -q "UPGRADE \"${UPGRADE_NAME}\" NEEDED"
}

do_swap() {
  require_repo
  log "TRIGGER upgrade swap for $UPGRADE_NAME @ $UPGRADE_HEIGHT"
  cd "$REPO_DIR"
  # Bring the new code exactly now. --autostash preserves local app.toml config
  # (key-name / rpc) across the pull; --ff-only refuses a surprise merge.
  if ! git pull --autostash --ff-only "$GIT_REMOTE" "$GIT_BRANCH" >>"$LOG" 2>&1; then
    log "ERROR: git pull failed. NOT restarting (node stays halted, no fork). Fix git, then: (cd $REPO_DIR && docker compose down && docker compose up -d)"
    return 1
  fi
  docker compose down    >>"$LOG" 2>&1     # NO -v — keeps chain data
  docker compose up -d   >>"$LOG" 2>&1     # make install rebuilds the new binary; upgrade applies
  touch "$DONE"
  log "SWAP DONE. Node rebuilding $UPGRADE_NAME and applying the upgrade. Follow: docker compose logs -f"
  "$SCRIPT" uninstall >>"$LOG" 2>&1 || true
}

cmd_run() {
  [ -f "$DONE" ] && exit 0
  exec 9>"$LOCK"; flock -n 9 || exit 0          # single instance
  if [ -n "$NOT_BEFORE" ] && [ "$(date -u +%s)" -lt "$(date -u -d "$NOT_BEFORE" +%s)" ]; then
    exit 0
  fi

  h="$(local_height)"
  if [ -n "$h" ]; then
    echo "$h" > "$STATE"
    if [ "$h" -ge "$((UPGRADE_HEIGHT - 1))" ]; then
      do_swap || exit 1
    fi
    exit 0
  fi

  # RPC unreachable: act ONLY on the confirmed upgrade-halt panic (never on an
  # ambiguous crash — that could restart the new binary early and fork).
  if halt_panic_seen; then
    do_swap || exit 1
  fi
  exit 0
}

cmd_install() {
  require_repo
  for t in git jq flock curl docker; do
    command -v "$t" >/dev/null || { echo "ERROR: '$t' not found (install it first; e.g. apt-get install -y jq)"; exit 1; }
  done
  rm -f "$DONE"
  local line="* * * * * REPO_DIR='$REPO_DIR' UPGRADE_HEIGHT=$UPGRADE_HEIGHT UPGRADE_NAME='$UPGRADE_NAME' CONTAINER='$CONTAINER' RPC='$RPC' NOT_BEFORE='$NOT_BEFORE' '$SCRIPT' run"
  ( crontab -l 2>/dev/null | grep -vF "$SCRIPT run"; echo "$line" ) | crontab -
  log "INSTALLED watcher: $UPGRADE_NAME @ height $UPGRADE_HEIGHT  repo=$REPO_DIR"
  echo "Installed. It will auto-upgrade at height $UPGRADE_HEIGHT."
  echo "Follow:    tail -f $LOG"
  echo "Cancel:    bash $SCRIPT uninstall"
}

cmd_uninstall() {
  crontab -l 2>/dev/null | grep -vF "$SCRIPT run" | crontab - 2>/dev/null || true
  log "UNINSTALLED watcher"
}

cmd_status() {
  echo "upgrade        : $UPGRADE_NAME @ height $UPGRADE_HEIGHT"
  echo "repo           : ${REPO_DIR:-(unset — pass REPO_DIR)}"
  echo "current height : $(local_height || true)"
  echo "last seen      : $(cat "$STATE" 2>/dev/null || echo n/a)"
  echo "already done   : $([ -f "$DONE" ] && echo yes || echo no)"
  echo -n "cron           : "; crontab -l 2>/dev/null | grep -F "$SCRIPT run" || echo "(none)"
}

case "${1:-}" in
  install)   cmd_install ;;
  uninstall) cmd_uninstall ;;
  run)       cmd_run ;;
  status)    cmd_status ;;
  *) echo "usage: bash $0 {install|uninstall|status|run}   (set UPGRADE_HEIGHT)"; exit 1 ;;
esac
