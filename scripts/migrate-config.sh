#!/usr/bin/env bash
#
# One-time migration for EXISTING SteemVM validators.
#
# It adopts the untracked-config layout — Instructions/app.toml and config.toml
# become gitignored and are seeded from tracked *.example templates — WITHOUT
# losing your local edits (moniker, keys, gas price, Steem RPC). After this,
# `git pull` never breaks on your config again.
#
# Run it from inside your checkout:
#   cd /home/SteemVirtualMachine-EVM
#   curl -fsSL https://raw.githubusercontent.com/blazeapps007/SteemVirtualMachine-EVM/main/scripts/migrate-config.sh | bash
#
# ...or point it at the checkout without cd'ing:
#   curl -fsSL https://raw.githubusercontent.com/blazeapps007/SteemVirtualMachine-EVM/main/scripts/migrate-config.sh \
#     | REPO_DIR=/home/SteemVirtualMachine-EVM bash
#
# Safe & idempotent: backs up your live config first, only ever touches those two
# files (never your chain data, keys, genesis, or validator.json), and re-running
# after it's done is a harmless no-op. Works on Ubuntu / macOS / Git-Bash — any
# POSIX bash with git.
#
set -euo pipefail

REMOTE="${GIT_REMOTE:-origin}"
BRANCH="${GIT_BRANCH:-main}"
FILES="Instructions/app.toml Instructions/config.toml"

# --- locate the repo (from your current dir; never a hardcoded path) --------
command -v git >/dev/null || { echo "ERROR: git not found on PATH." >&2; exit 1; }
# Priority: explicit REPO_DIR env > the git checkout that contains your current
# directory (works from the repo root OR any subdirectory) > the current dir.
if [ -z "${REPO_DIR:-}" ]; then
  REPO_DIR="$(git -C "$PWD" rev-parse --show-toplevel 2>/dev/null || true)"
  [ -n "$REPO_DIR" ] || REPO_DIR="$PWD"
fi
if [ ! -f "$REPO_DIR/docker-compose.yml" ] || [ ! -d "$REPO_DIR/Instructions" ]; then
  echo "ERROR: couldn't find your SteemVM checkout from '$PWD'." >&2
  echo "       Run this from inside the repo (any subdirectory is fine)," >&2
  echo "       or pass REPO_DIR=/path/to/SteemVirtualMachine-EVM." >&2
  exit 1
fi
cd "$REPO_DIR"
echo "==> repo: $REPO_DIR   remote: $REMOTE   branch: $BRANCH"

# --- 0. stop git from tracking the executable bit ---------------------------
# A stray `chmod +x` on a tracked file (e.g. the scripts) otherwise counts as an
# unstaged change and blocks `git pull`. Disabling fileMode makes pulls robust.
git config core.fileMode false
echo "    set core.fileMode=false (chmod no longer blocks pulls)"

# --- 1. drop any skip-worktree flags from an earlier manual workaround -------
for f in $FILES; do
  git update-index --no-skip-worktree "$f" 2>/dev/null || true
done

# --- 2. back up your live config --------------------------------------------
BK="$(mktemp -d 2>/dev/null || echo /tmp/svm-config-backup.$$)"
mkdir -p "$BK"
for f in $FILES; do
  if [ -f "$f" ]; then cp "$f" "$BK/$(basename "$f")"; echo "    backed up  $f"; fi
done

# --- 3. clean ONLY those two files so the pull can fast-forward --------------
# If they are still tracked (pre-migration), this restores them to HEAD; if they
# are already untracked (migration done), checkout is a no-op — either is fine.
git checkout -- $FILES 2>/dev/null || true

# --- 4. pull the new layout (.example templates + gitignore) ----------------
if ! git pull --ff-only "$REMOTE" "$BRANCH"; then
  echo >&2
  echo "ERROR: 'git pull' failed. Your config backups are safe in: $BK" >&2
  echo "       Resolve the git issue, then re-run this script (it is idempotent)." >&2
  exit 1
fi

# --- 5. restore your edits (now gitignored) ---------------------------------
for f in $FILES; do
  b="$BK/$(basename "$f")"
  if [ -f "$b" ]; then cp "$b" "$f"; echo "    restored   $f"; fi
done

# --- 6. verify --------------------------------------------------------------
echo
echo "==> check-ignore (both files SHOULD be listed = now ignored):"
git check-ignore $FILES || echo "    (warning: not ignored — is the pull's .gitignore in place?)"
echo
echo "==> git status (Instructions/app.toml & config.toml should NOT appear):"
git status --short
echo
echo "DONE. Your config is preserved and gitignored; future 'git pull' won't break on it."
echo "Backups kept at: $BK"
