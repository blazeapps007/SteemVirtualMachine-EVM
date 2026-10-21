# Upgrade to v0.0.7

**Upgrade height: 79664** (planned for about 2026-10-24 00:00 UTC; the exact
time depends on block times). Proposal: `proposal.json` in the repository root.

## What it fixes

Bridge-outs through the EVM precompile (`0x…0900`, e.g. from MetaMask) failed
with `steem189zq… is not allowed to receive funds`. cosmos/evm copies a
precompile's bank transfers into the EVM state, skipping only blocked
accounts, and refuses to write a module account's balance. The bridge's
module accounts (`steemblackhole`, `bridge_reward`) were not blocked. v0.0.7
blocks every module account in x/bank, as upstream cosmos/evm does. It changes
no state and no parameters; the upgrade handler only runs module migrations.

Image `steemblazer/steemvmd:v0.0.7`, binary sha256:

```
dce7841719984b4c385dee7f4131b6d58f424562c1c7dcdd78dafb189b0936cc
```

## Stage it (any time before the height)

From your SteemVM folder:

```sh
git fetch origin && git checkout release/v0.0.6 && git pull --ff-only && ./update.sh
```

`update.sh` copies the v0.0.7 binary into cosmovisor's upgrade slot and prints
a `sha256:` line, which must match the value above. The node is **not**
restarted: cosmovisor switches to v0.0.7 by itself at height 79664.

⚠️ Before the height, don't change your node's image or run
`docker compose pull && docker compose up -d` on the node. Starting v0.0.7
early halts the node.

## Vote

```sh
docker exec -it steemvm-node /root/go/bin/steemvmd tx gov vote <proposal-id> yes \
  --from <your-key> --home /root/.steemvm --chain-id steemvm-2 \
  --gas auto --gas-adjustment 1.5 --gas-prices 1000000000asteem -y
```

## If you missed staging

Your node halts at the height with `UPGRADE "v0.0.7" NEEDED`. Nothing is lost:
run the same `./update.sh` line. It stages v0.0.7, points cosmovisor at it and
restarts the node once. If the node was jailed for the downtime, unjail it
after it has caught up.

## After it applies

Check with:

```sh
docker exec steemvm-node /root/go/bin/steemvmd query upgrade applied v0.0.7 --home /root/.steemvm
docker exec steemvm-node /root/.steemvm/cosmovisor/current/bin/steemvmd version   # 0.0.7
```
