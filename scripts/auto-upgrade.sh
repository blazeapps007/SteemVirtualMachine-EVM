#!/usr/bin/env bash
#
# Automatic, HEIGHT-triggered upgrade watcher for SteemVM validators.
#
# Just run it — no install, no cron, no root scheduler:
#
#     bash scripts/auto-upgrade.sh
#
# It polls THIS node's height every few seconds and, the instant the chain
# reaches the scheduled upgrade block (or the node halts with the upgrade
# panic), it performs:
#
#     git pull  ->  docker compose down (NO -v)  ->  docker compose up -d
#
# which rebuilds the new binary and lets it apply the upgrade. Then it exits.
#
# Cross-platform: Ubuntu / Debian / macOS / Windows-Git-Bash / WSL — anything
# with bash + git + curl + docker. No flock, no crontab, no GNU-only flags.
#
# Because it triggers on the block HEIGHT (not the clock), slow blocks or drift
# just make it wait longer — nothing to reschedule. It only swaps once the node
# has actually reached the upgrade block, so it will not restart the node early.
#
# Keep it running until the upgrade fires. To survive a closed terminal:
#     Linux/macOS:  nohup bash scripts/auto-upgrade.sh > upgrade.log 2>&1 &
#     any / simple: run it inside tmux or screen, or just leave the window open
#
# Optional one-shot status check (prints once and exits):
#     bash scripts/auto-upgrade.sh status
#
# Override anything via env, e.g.:  UPGRADE_HEIGHT=254132 INTERVAL=10 bash scripts/auto-upgrade.sh
#
set -euo pipefail

# ---- config (override via env) ---------------------------------------------
UPGRADE_HEIGHT="${UPGRADE_HEIGHT:-254132}"      # the scheduled upgrade block
UPGRADE_NAME="${UPGRADE_NAME:-v0.0.2-Beta1}"    # matches the on-chain plan name
RPC="${RPC:-http://localhost:26657}"            # local CometBFT RPC
CONTAINER="${CONTAINER:-steemvm-node}"          # node container (halt-log fallback)
GIT_REMOTE="${GIT_REMOTE:-origin}"
GIT_BRANCH="${GIT_BRANCH:-main}"
INTERVAL="${INTERVAL:-15}"                       # seconds between height checks

# ---- repo dir (portable; auto-detected from this script's location) --------
REPO_DIR="${REPO_DIR:-$(cd "$(dirname "$0")/.." 2>/dev/null && pwd || true)}"

log() { echo "$(date -u +%FT%TZ) $*"; }
need() { command -v "$1" >/dev/null 2>&1 || { echo "ERROR: '$1' not found on PATH." >&2; exit 1; }; }

need git; need curl; need docker
[ -n "$REPO_DIR" ] && [ -f "$REPO_DIR/docker-compose.yml" ] || {
  echo "ERROR: could not find your checkout. Run from the repo, or set REPO_DIR=/path/to/SteemVirtualMachine-EVM." >&2
  exit 1
}

# `docker compose` (v2) or legacy `docker-compose` (v1) — whichever exists.
DC="docker compose"
docker compose version >/dev/null 2>&1 || DC="docker-compose"

height() {
  # committed height of THIS node; empty if the RPC is unreachable (halted/down)
  curl -fsS --max-time 5 "$RPC/status" 2>/dev/null \
    | tr ',' '\n' | grep -m1 '"latest_block_height"' \
    | grep -o '[0-9][0-9]*' | head -n1 || true
}

panic_seen() {
  # the old binary panicked with the scheduled-upgrade halt
  docker logs --tail 200 "$CONTAINER" 2>&1 | grep -q "UPGRADE \"${UPGRADE_NAME}\" NEEDED"
}

swap() {
  log "TRIGGER: swapping to $UPGRADE_NAME (block $UPGRADE_HEIGHT reached)"
  cd "$REPO_DIR"
  # --autostash keeps any stray local edits out of the way; with the per-validator
  # config now gitignored, this pull is clean. --ff-only refuses a surprise merge.
  if ! git pull --autostash --ff-only "$GIT_REMOTE" "$GIT_BRANCH"; then
    log "ERROR: git pull failed. NOT restarting (node stays put, no fork). Fix git, then:"
    log "       (cd $REPO_DIR && $DC down && $DC up -d)"
    return 1
  fi
  $DC down       # NO -v — keeps chain data
  $DC up -d      # 'make install' rebuilds the new binary; the upgrade applies
  log "SWAP DONE. Node rebuilding $UPGRADE_NAME and applying the upgrade."
  log "Follow it with:  $DC logs -f"
}

# ---- one-shot status -------------------------------------------------------
if [ "${1:-}" = "status" ]; then
  h="$(height)"
  echo "upgrade        : $UPGRADE_NAME @ height $UPGRADE_HEIGHT"
  echo "repo           : $REPO_DIR"
  echo "compose        : $DC"
  echo "current height : ${h:-(RPC unreachable — node down/halted?)}"
  [ -n "$h" ] && echo "blocks to go   : $(( UPGRADE_HEIGHT - h ))"
  exit 0
fi

# ---- watch loop ------------------------------------------------------------
log "watching for $UPGRADE_NAME @ height $UPGRADE_HEIGHT  (repo=$REPO_DIR, every ${INTERVAL}s)"
log "leave this running until the upgrade fires (Ctrl-C to stop)"
while true; do
  h="$(height)"
  if [ -n "$h" ]; then
    if [ "$h" -ge "$(( UPGRADE_HEIGHT - 1 ))" ]; then
      log "height $h — at the upgrade block"
      swap && exit 0 || exit 1
    fi
    log "height $h  ($(( UPGRADE_HEIGHT - h )) to go)"
  elif panic_seen; then
    log "node halted with the upgrade panic (RPC down)"
    swap && exit 0 || exit 1
  else
    log "node RPC unreachable — waiting (node down or still starting?)"
  fi
  sleep "$INTERVAL"
done
