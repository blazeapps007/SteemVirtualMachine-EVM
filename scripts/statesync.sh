#!/usr/bin/env bash
#
# One-command state-sync setup for a NEW SteemVM validator.
#
# The chain has already gone through the v0.0.2-Beta1 upgrade, so a fresh node
# CANNOT replay from genesis — it must bootstrap from a recent snapshot. This
# script reads a recent trusted height + hash from a public RPC and writes the
# [statesync] settings into Instructions/config.toml for you. Run it ONCE,
# before your first `docker compose up` (state-sync only runs on a node that has
# no local data yet).
#
# Usage (from your checkout):
#   bash scripts/statesync.sh          # configure state-sync
#   bash scripts/statesync.sh up       # configure, then `docker compose up -d`
#
# Env overrides:
#   SNAP_RPC      public RPC to read the trusted height/hash from
#                 (default https://steemvmd.steemscanner.com)
#   RPC_SERVERS   comma-separated rpc_servers for light-client verification
#                 (default: SNAP_RPC twice — replace one with a 2nd real RPC)
#   TRUST_OFFSET  how far back from the tip to trust (default 2000 blocks)
#
# Portable: bash + curl + git. Works on Ubuntu / macOS / Git-Bash / WSL.
#
set -euo pipefail

SNAP_RPC="${SNAP_RPC:-https://steemvmd.steemscanner.com}"
SNAP_RPC="${SNAP_RPC%/}"
TRUST_OFFSET="${TRUST_OFFSET:-2000}"
RPC_SERVERS="${RPC_SERVERS:-$SNAP_RPC:443,$SNAP_RPC:443}"

command -v curl >/dev/null || { echo "ERROR: curl not found on PATH." >&2; exit 1; }
command -v git  >/dev/null || { echo "ERROR: git not found on PATH." >&2; exit 1; }

# --- locate the repo (from your current dir; no hardcoded path) -------------
if [ -z "${REPO_DIR:-}" ]; then
  REPO_DIR="$(git -C "$PWD" rev-parse --show-toplevel 2>/dev/null || true)"
  [ -n "$REPO_DIR" ] || REPO_DIR="$PWD"
fi
CFG="$REPO_DIR/Instructions/config.toml"
EXAMPLE="$REPO_DIR/Instructions/config.toml.example"
[ -f "$EXAMPLE" ] || {
  echo "ERROR: $EXAMPLE not found — run this from your SteemVM checkout." >&2
  exit 1
}

# Seed config.toml from the template if you haven't created it yet (your moniker
# edits, if any, are preserved when it already exists).
if [ ! -f "$CFG" ]; then
  cp "$EXAMPLE" "$CFG"
  echo "seeded Instructions/config.toml from the template"
fi

# --- fetch a recent trusted checkpoint --------------------------------------
echo "==> reading a trusted checkpoint from $SNAP_RPC"
LATEST="$(curl -fsS --max-time 10 "$SNAP_RPC/status" \
  | grep -oE '"latest_block_height":"[0-9]+"' | grep -oE '[0-9]+' | head -1)"
[ -n "${LATEST:-}" ] || { echo "ERROR: could not read latest height from $SNAP_RPC/status" >&2; exit 1; }

TRUST_HEIGHT=$(( LATEST - TRUST_OFFSET ))
[ "$TRUST_HEIGHT" -gt 0 ] || { echo "ERROR: computed trust_height $TRUST_HEIGHT <= 0 (chain too young?)" >&2; exit 1; }

TRUST_HASH="$(curl -fsS --max-time 10 "$SNAP_RPC/block?height=$TRUST_HEIGHT" \
  | grep -oE '"hash":"[0-9A-Fa-f]{64}"' | head -1 | grep -oE '[0-9A-Fa-f]{64}')"
[ -n "${TRUST_HASH:-}" ] || { echo "ERROR: could not read block hash at height $TRUST_HEIGHT" >&2; exit 1; }

echo "    latest height : $LATEST"
echo "    trust_height  : $TRUST_HEIGHT"
echo "    trust_hash    : $TRUST_HASH"
echo "    rpc_servers   : $RPC_SERVERS"

# --- patch ONLY the [statesync] section of config.toml ----------------------
awk -v rpc="$RPC_SERVERS" -v th="$TRUST_HEIGHT" -v hh="$TRUST_HASH" '
  /^\[/ { insync = ($0 ~ /^\[statesync\]/) }
  insync && $1=="enable"       { print "enable = true"; next }
  insync && $1=="rpc_servers"  { print "rpc_servers = \"" rpc "\""; next }
  insync && $1=="trust_height" { print "trust_height = " th; next }
  insync && $1=="trust_hash"   { print "trust_hash = \"" hh "\""; next }
  { print }
' "$CFG" > "$CFG.tmp" && mv "$CFG.tmp" "$CFG"

echo "==> wrote [statesync] into Instructions/config.toml (enable=true)"
echo
echo "Start the node — it will fast-sync from a snapshot instead of replaying history:"
echo "    docker compose up -d && docker compose logs -f steemvm"
echo
echo "NOTE: state-sync only runs on a node with NO existing data. If this node has"
echo "      already synced, wipe just its chain volume first (never touch other"
echo "      volumes): docker compose down && docker volume rm \"\$(docker volume ls -q | grep 'steemvm-home$')\""

if [ "${1:-}" = "up" ]; then
  echo
  echo "==> docker compose up -d"
  ( cd "$REPO_DIR" && docker compose up -d )
fi
