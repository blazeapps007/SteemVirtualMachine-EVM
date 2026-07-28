#!/usr/bin/env bash
#
# Submit (and optionally vote on) the SteemVM v0.0.2-Beta1 governance proposal.
#
# ONE gov proposal carrying TWO messages:
#
#   1. MsgSoftwareUpgrade  — schedule the v0.0.2-Beta1 binary upgrade at the
#      block height closest to UPGRADE_DATE. This message just RECORDS the plan
#      when the proposal passes; the chain actually halts later, at that height.
#
#   2. MsgUpdateParams (steembridge) — set the bridge gateway/deposit account to
#      GATEWAY. This takes effect the MOMENT the proposal passes (NOT at the
#      upgrade height). The full params object is fetched live and only
#      gateway_account is changed (MsgUpdateParams replaces the whole object).
#
# PREREQUISITES (see upgradeGuide.md):
#   - The v0.0.2-Beta1 code is built, `go test` green, committed, tagged
#     `v0.0.2-Beta1`, and distributed to every validator.
#   - UPGRADE_NAME below MUST equal UpgradeName in app/upgrades.go.
#   - UPGRADE_DATE must be far enough out to clear the ~2-day voting period.
#
# Requires GNU date (Linux/Debian — your Ubuntu host and the golang:trixie
# container both have it) and jq.
#
# Usage:
#   KEY=blaze.apps ./scripts/propose-v0.0.2-beta1.sh
#   UPGRADE_DATE=2026-07-31T00:00:00Z GATEWAY=svm.bank KEY=mykey VOTE=yes \
#     ./scripts/propose-v0.0.2-beta1.sh
#
set -euo pipefail

# ---- config (override any of these via env) --------------------------------
STEEMD="${STEEMD:-steemvmd}"                      # binary (or full path)
UPGRADE_NAME="${UPGRADE_NAME:-v0.0.2-Beta1}"      # MUST match app/upgrades.go
UPGRADE_DATE="${UPGRADE_DATE:-2026-07-31T00:00:00Z}"
GATEWAY="${GATEWAY:-svm.bank}"
BLOCK_SECONDS="${BLOCK_SECONDS:-6}"               # avg block time (timeout_commit)
KEY="${KEY:-blaze.apps}"                          # signing key (proposer)
CHAIN_ID="${CHAIN_ID:-steemvm}"
DEPOSIT="${DEPOSIT:-10000000000000000000asteem}"  # >= gov min_deposit (10 STEEM)
GAS_PRICES="${GAS_PRICES:-1000000000asteem}"
VOTE="${VOTE:-yes}"                               # set VOTE="" to skip auto-vote
OUT="${OUT:-proposal-v0.0.2-beta1.json}"

# ---- derive gov authority + target height ----------------------------------
GOV="$("$STEEMD" query auth module-account gov --output json \
        | jq -r '.account.value.address // .account.address')"
CUR="$("$STEEMD" status 2>/dev/null \
        | jq -r '.sync_info.latest_block_height // .SyncInfo.latest_block_height')"
NOW="$(date -u +%s)"
TARGET="$(date -u -d "$UPGRADE_DATE" +%s)"

if [ "$TARGET" -le "$NOW" ]; then
  echo "ERROR: UPGRADE_DATE ($UPGRADE_DATE) is in the past." >&2
  exit 1
fi
HEIGHT=$(( CUR + (TARGET - NOW) / BLOCK_SECONDS ))

# The upgrade height must land AFTER the ~2-day voting period, or the plan can't
# be applied. ~2 days at 6s ≈ 28800 blocks; require a comfortable margin.
MIN_MARGIN=$(( 40000 ))
if [ "$HEIGHT" -le $(( CUR + MIN_MARGIN )) ]; then
  echo "ERROR: computed height $HEIGHT is too close to current $CUR — it may not" >&2
  echo "       clear the voting period. Pick a later UPGRADE_DATE." >&2
  exit 1
fi

echo "gov authority : $GOV"
echo "current height: $CUR"
echo "upgrade name  : $UPGRADE_NAME"
echo "upgrade target: $UPGRADE_DATE  ->  height $HEIGHT  (~$(( (TARGET-NOW)/86400 )) days out)"
echo "gateway       : $GATEWAY"
echo

# ---- build the two-message proposal from live steembridge params -----------
"$STEEMD" query steembridge params --output json \
| jq \
    --arg gov "$GOV" \
    --arg name "$UPGRADE_NAME" \
    --arg height "$HEIGHT" \
    --arg gateway "$GATEWAY" \
    --arg deposit "$DEPOSIT" \
    --arg date "$UPGRADE_DATE" '
    .params.gateway_account = $gateway
    | { messages: [
          { "@type": "/cosmos.upgrade.v1beta1.MsgSoftwareUpgrade",
            authority: $gov,
            plan: { name: $name, height: $height, info: "" } },
          { "@type": "/steemvm.steembridge.v1.MsgUpdateParams",
            authority: $gov,
            params: .params }
        ],
        metadata: "ipfs://none",
        deposit: $deposit,
        title: ("SteemVM " + $name + " + gateway " + $gateway),
        summary: ("Schedule the " + $name + " binary upgrade at height " + $height
                  + " (target " + $date + "), and set the bridge gateway/deposit "
                  + "account to " + $gateway + " when this proposal passes. "
                  + "Validators at the halt: git checkout " + $name
                  + " + docker compose down (NO -v) + up.") }' \
> "$OUT"

echo "wrote $OUT:"
cat "$OUT"
echo

# ---- submit ----------------------------------------------------------------
"$STEEMD" tx gov submit-proposal "$OUT" \
  --from "$KEY" --chain-id "$CHAIN_ID" \
  --gas auto --gas-adjustment 1.5 --gas-prices "$GAS_PRICES" -y

# ---- vote (optional; the proposer's yes) -----------------------------------
# NOTE: on a multi-validator chain, EVERY validator must also cast its own vote.
if [ -n "$VOTE" ]; then
  echo "waiting one block for the proposal to commit..."
  sleep "$BLOCK_SECONDS"
  PID="$("$STEEMD" query gov proposals --output json | jq -r '.proposals[-1].id')"
  echo "voting $VOTE on proposal $PID"
  "$STEEMD" tx gov vote "$PID" "$VOTE" \
    --from "$KEY" --chain-id "$CHAIN_ID" \
    --gas auto --gas-adjustment 1.5 --gas-prices "$GAS_PRICES" -y
fi

echo
echo "done. Track it:  $STEEMD query gov proposals"
echo "after it PASSES: $STEEMD query upgrade plan   # shows $UPGRADE_NAME @ $HEIGHT"
