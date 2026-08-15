#!/usr/bin/env bash
#
# One-command validator upgrade for SteemVM.
#
# Pulls the latest code and (re)starts the node under cosmovisor. The node keeps
# running its CURRENT binary; the newly-built one is staged, and cosmovisor swaps
# to it automatically at the on-chain governance upgrade height — no action at the
# block, ~no downtime. Safe to run any time before the height, and repeatable.
#
# Usage (from your checkout):
#   bash scripts/upgrade.sh            # pull + rebuild + restart, then tail logs
#   bash scripts/upgrade.sh --no-logs  # don't tail logs at the end
#
# NOTE: adopt cosmovisor while still on the CURRENT release (just run this once
# before the new version lands), so the running binary bootstraps correctly.
#
set -euo pipefail

FOLLOW=1
[ "${1:-}" = "--no-logs" ] && FOLLOW=0

command -v git >/dev/null    || { echo "ERROR: git not found on PATH." >&2; exit 1; }
command -v docker >/dev/null || { echo "ERROR: docker not found on PATH." >&2; exit 1; }

REPO_DIR="$(git -C "$(dirname "$0")" rev-parse --show-toplevel 2>/dev/null || true)"
[ -n "$REPO_DIR" ] || { echo "ERROR: run this from inside your SteemVM checkout." >&2; exit 1; }
cd "$REPO_DIR"

echo "==> git pull"
git pull --ff-only

echo "==> docker compose down && up -d --build (rebuilds steemvmd + stages the new binary for cosmovisor)"
docker compose down
docker compose up -d --build

echo
echo "Node is (re)starting under cosmovisor. It runs the current binary now and"
echo "auto-swaps to the staged upgrade binary at the governance upgrade height."
echo "Check the staged version any time:"
echo "    docker compose exec steemvm ls /root/.steemvm/cosmovisor/upgrades"
echo "    docker compose exec steemvm steemvmd version"

if [ "$FOLLOW" = "1" ]; then
  echo
  echo "==> tailing node logs (Ctrl-C to stop; the node keeps running)"
  docker compose logs -f steemvm
fi
