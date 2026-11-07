#!/usr/bin/env bash
#
# unpin_images.sh — comment out image pins in the repository-root .env.
#
# docker-compose.yml names this release's images as defaults
# (${STEEMVM_IMAGE:-steemblazer/steemvmd:vX.Y.Z} and the oracles'), but a
# value in .env overrides them, so a pin written by an earlier release keeps
# a node on that version after `git pull`. This comments every such pin out
# (STEEMVM_IMAGE, STEEMVM_STAGE_IMAGE, ORACLE_GO_IMAGE, ORACLE_PYTHON_IMAGE,
# ORACLE_JS_IMAGE); the rest of .env (e.g. STEEMVM_HOME) is left as it is.
# To run a different image once, set it on the command line instead:
#   STEEMVM_IMAGE=you/steemvmd:tag docker compose up -d steemvm
#
# Usage: ./unpin_images.sh [--check]
#   --check   only list the pins, change nothing
#
# reset.sh, update.sh and new-validator.sh run it.

if [ -z "${BASH_VERSION:-}" ]; then exec bash "$0" "$@"; fi
set -euo pipefail

ENV_FILE=".env"
VARS="STEEMVM_IMAGE STEEMVM_STAGE_IMAGE ORACLE_GO_IMAGE ORACLE_PYTHON_IMAGE ORACLE_JS_IMAGE"
CHECK=0
case "${1:-}" in
  --check) CHECK=1 ;;
  "") ;;
  *) echo "usage: ./unpin_images.sh [--check]" >&2; exit 2 ;;
esac

[ -f docker-compose.yml ] || { echo "ERROR: run this from the repository root." >&2; exit 1; }
[ -f "$ENV_FILE" ] || exit 0

for v in $VARS; do
  line="$(grep -E "^[[:space:]]*(export[[:space:]]+)?${v}[[:space:]]*=" "$ENV_FILE" | tail -1 || true)"
  [ -n "$line" ] || continue
  default="$(sed -n "s/.*\${$v:-\([^}]*\)}.*/\1/p" docker-compose.yml | head -1)"
  echo "$v pinned in .env (${line#*=}); docker-compose.yml default: ${default:-none}"
  [ "$CHECK" = "1" ] && continue
  sed -i.bak -E "s/^([[:space:]]*(export[[:space:]]+)?${v}[[:space:]]*=)/# (unpin_images.sh: use docker-compose.yml's default) \1/" "$ENV_FILE"
  rm -f "$ENV_FILE.bak"
  echo "  -> commented out"
done
