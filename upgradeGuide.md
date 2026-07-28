# SteemVM Upgrade Guide — v0.0.2-Beta1

## What this upgrade does
- **Fixed version string** — stamps `0.0.2-Beta1` so every node reports the same `steemvmd version`.
- **erc20 IBC middleware** — wires it into the transfer stack so IBC tokens (e.g. bridged USDC)
  auto-register as ERC20 and become MetaMask-usable.
- **No-fail redundant attestations** — when several validators attest the same Steem deposit /
  name registration in one block, the ones that land after the 2/3 threshold is crossed (and any
  duplicate confirmation) are now benign no-op successes instead of failing with
  `insufficient fee` (`code 13`). A fact *mismatch* still costs a fee, preserving anti-poisoning.
- **Relayer memo filter** — the built-in relayer now only attests gateway transfers whose memo
  resolves to a supported destination (`svm-deposit <addr>` / bare `<addr>` or `svm-register <addr>`,
  a `steem1…` or `0x…` address). Transfers with an unparseable memo (a human note, a typo) are
  ignored — **no on-chain record is created for them** (a deliberate change from the previous
  UNCLAIMABLE audit-trail behavior).

This is a **consensus-breaking** upgrade (the attestation and IBC changes alter state transitions).
Every validator MUST switch at the same block height via the on-chain `x/upgrade` mechanism. Do not
upgrade ad-hoc.

## ⚠️ Critical rule
Do NOT run the new binary before the upgrade height. The IBC routing change activates the instant the
new binary runs — if one node rebuilds early, it forks. Keep the OLD code in your workspace until the
chain halts at the upgrade height, then swap.

## 1. Prepare (all validators)
Fetch the release but keep your live node on the current (old) commit:

```sh
git fetch --all --tags   # do NOT check out v0.0.2-Beta1 yet on a live node
```

## 2. Submit the software-upgrade proposal (one proposer)
Pick a height `H` after the 2-day voting period (~28,800 blocks at 6s) plus margin:

```sh
CUR=$(steemvmd status | jq -r .sync_info.latest_block_height)
HEIGHT=$((CUR + 35000))
steemvmd tx upgrade software-upgrade v0.0.2-Beta1 \
  --title "Upgrade to v0.0.2-Beta1" \
  --summary "Fixed version string + erc20 IBC middleware (IBC tokens usable as ERC20)" \
  --upgrade-height $HEIGHT --no-validate \
  --deposit 10000000000000000000asteem \
  --from <yourkey> --gas auto --gas-adjustment 1.5 --gas-prices 1000000000asteem -y
```

The plan name `v0.0.2-Beta1` MUST match `UpgradeName` in the binary (`app/upgrades.go`).

## 3. Vote (all validators)

```sh
steemvmd query gov proposals            # find the id
steemvmd tx gov vote <id> yes --from <yourkey> --gas auto --gas-adjustment 1.5 --gas-prices 1000000000asteem -y
steemvmd query gov proposal <id>        # -> PROPOSAL_STATUS_PASSED
```

## 4. Wait for the halt
Keep running the OLD binary. At height `H` every node stops with a panic:
`UPGRADE "v0.0.2-Beta1" NEEDED at height: <H>`. This is expected — block production pauses.

## 5. Swap to the new binary (all validators, at the halt)

```sh
cd /path/to/SteemVM              # repo bind-mounted at /workspace
git checkout v0.0.2-Beta1        # the tag/branch with the new code
docker compose up -d --build     # runs make install -> new binary applies the upgrade and resumes
```

## 6. Verify (on every node)

```sh
docker exec steemvm-node /root/go/bin/steemvmd version   # -> 0.0.2-Beta1 (identical everywhere)
steemvmd status | jq .sync_info.latest_block_height       # advancing past H
steemvmd query gov proposal <id>                          # done/applied
```

## 7. Post-upgrade — register an IBC token as ERC20
A new incoming IBC transfer now auto-registers the pair:

```sh
steemvmd query erc20 token-pairs        # your ibc/... denom appears
```

The ERC20 address is deterministic (last 20 bytes of the ibc denom hash). Add it in MetaMask (set
symbol/decimals manually if the denom has no bank metadata). Balances are a live 1:1 view of the bank
coin — no minting, no conversion.

## Rollback / abort
- **Before H:** cancel with a `cancel-software-upgrade` gov proposal, or let the proposal be voted down.
- **After H:** a node that won't start is not a fork risk as long as it runs the correct new binary —
  check logs, ensure `/workspace` is on `v0.0.2-Beta1`, rebuild. Do not revert a node to the old
  binary past H (it cannot progress on old code).

## Notes
- No new store keys are added, so there is no store migration / `StoreLoader`.
- cosmovisor auto-swaps at H but needs both binaries pre-staged; the manual halt-then-rebuild flow
  above matches this repo's build-from-source docker setup.
