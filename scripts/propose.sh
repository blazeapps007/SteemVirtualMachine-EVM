#!/usr/bin/env bash
#
# Submit a coordinated software-upgrade governance proposal for SteemVM.
#
# Flow: propose (this script) -> validators vote yes -> the proposal passes and
# stores an on-chain upgrade Plan at height H. Each validator runs cosmovisor
# (see docker-compose.yml), so at H the node AUTO-SWAPS to the staged binary with
# ~no downtime and no manual action at the block. Validators only need to have
# pulled + restarted before H (scripts/upgrade.sh) so the new binary is staged.
#
# Usage (run from a machine with the proposer key in your keyring):
#   scripts/propose.sh --from <key> [--name v0.0.3]
#                      [--height H | --blocks-ahead N]
#                      [--node tcp://host:26657] [--chain-id steemvm]
#                      [--home /root/.steemvm] [--deposit 10000000000000000000asteem]
#                      [--gas-prices 1000000000asteem] [--bin steemvmd]
#
# Portable: bash + the steemvmd CLI. Works on Ubuntu / macOS / Git-Bash / WSL.
#
set -euo pipefail

NAME="v0.0.3"
NODE="tcp://localhost:26657"
CHAIN_ID="steemvm"
HOME_DIR="${STEEMVM_HOME:-$HOME/.steemvm}"
DEPOSIT="10000000000000000000asteem"   # 10 STEEM = gov min_deposit
GAS_PRICES="1000000000asteem"          # feemarket min_gas_price
BLOCKS_AHEAD="30000"                    # ~2 days at 6s (voting_period) + buffer
HEIGHT=""
FROM=""
BIN="${STEEMVMD:-steemvmd}"

while [ $# -gt 0 ]; do
  case "$1" in
    --from)         FROM="$2"; shift 2;;
    --name)         NAME="$2"; shift 2;;
    --height)       HEIGHT="$2"; shift 2;;
    --blocks-ahead) BLOCKS_AHEAD="$2"; shift 2;;
    --node)         NODE="$2"; shift 2;;
    --chain-id)     CHAIN_ID="$2"; shift 2;;
    --home)         HOME_DIR="$2"; shift 2;;
    --deposit)      DEPOSIT="$2"; shift 2;;
    --gas-prices)   GAS_PRICES="$2"; shift 2;;
    --bin)          BIN="$2"; shift 2;;
    -h|--help)      sed -n '2,20p' "$0"; exit 0;;
    *) echo "unknown arg: $1" >&2; exit 1;;
  esac
done

[ -n "$FROM" ] || { echo "ERROR: --from <key> is required (the proposer key)." >&2; exit 1; }
command -v "$BIN" >/dev/null || { echo "ERROR: '$BIN' not on PATH (set --bin or \$STEEMVMD)." >&2; exit 1; }

# --- current height ---------------------------------------------------------
LATEST="$("$BIN" status --node "$NODE" 2>/dev/null \
  | grep -oE '"latest_block_height":"[0-9]+"' | grep -oE '[0-9]+' | head -1)"
[ -n "$LATEST" ] || { echo "ERROR: could not read latest height from $NODE" >&2; exit 1; }
[ -n "$HEIGHT" ] || HEIGHT=$(( LATEST + BLOCKS_AHEAD ))
[ "$HEIGHT" -gt "$LATEST" ] || { echo "ERROR: --height $HEIGHT must be > current height $LATEST" >&2; exit 1; }

# --- gov module authority address ------------------------------------------
GOV_AUTH="$("$BIN" query auth module-account gov --node "$NODE" -o json 2>/dev/null \
  | grep -oE '"address":"[a-z0-9]+"' | head -1 | grep -oE 'steem1[a-z0-9]+')"
[ -n "$GOV_AUTH" ] || { echo "ERROR: could not resolve the gov module address from $NODE" >&2; exit 1; }

# --- proposal json ----------------------------------------------------------
PROP="$(mktemp)"
trap 'rm -f "$PROP"' EXIT
cat > "$PROP" <<EOF
{
  "messages": [
    {
      "@type": "/cosmos.upgrade.v1beta1.MsgSoftwareUpgrade",
      "authority": "$GOV_AUTH",
      "plan": { "name": "$NAME", "time": "0001-01-01T00:00:00Z", "height": "$HEIGHT", "info": "SteemVM $NAME coordinated upgrade", "upgraded_client_state": null }
    }
  ],
  "metadata": "",
  "deposit": "$DEPOSIT",
  "title": "Upgrade to $NAME",
  "summary": "Coordinated software upgrade to $NAME at height $HEIGHT. Validators: cosmovisor auto-swaps at the height; make sure the $NAME binary is staged (git pull + docker compose up, i.e. scripts/upgrade.sh) before then."
}
EOF

echo "==> proposing '$NAME' at height $HEIGHT  (current $LATEST, +$(( HEIGHT - LATEST )) blocks ~ $(( (HEIGHT-LATEST)*6/3600 ))h)"
echo "    gov authority : $GOV_AUTH"
echo "    node          : $NODE"
echo

"$BIN" tx gov submit-proposal "$PROP" \
  --from "$FROM" --chain-id "$CHAIN_ID" --home "$HOME_DIR" --node "$NODE" \
  --gas auto --gas-adjustment 1.4 --gas-prices "$GAS_PRICES" --yes

echo
echo "Proposal submitted. Validators now vote:"
echo "    $BIN tx gov vote <PROPOSAL_ID> yes --from <key> --chain-id $CHAIN_ID --node $NODE --gas auto --gas-adjustment 1.4 --gas-prices $GAS_PRICES --yes"
echo "Find the id / track status:"
echo "    $BIN query gov proposals --node $NODE"
echo
echo "Once it PASSES, each validator only needs (any time before height $HEIGHT):"
echo "    bash scripts/upgrade.sh        # git pull + docker compose up  -> cosmovisor swaps at $HEIGHT"
