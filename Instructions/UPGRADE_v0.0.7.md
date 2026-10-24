# v0.0.7 (applied)

**v0.0.7 applied on-chain at height 79664.** The network runs v0.0.7. Image
`steemblazer/steemvmd:v0.0.7` (also `:latest`), binary sha256:

```
dce7841719984b4c385dee7f4131b6d58f424562c1c7dcdd78dafb189b0936cc
```

## What it fixed

Bridge-outs through the EVM precompile (`0x…0900`, e.g. from MetaMask) failed
with `steem189zq… is not allowed to receive funds`. cosmos/evm copies a
precompile's bank transfers into the EVM state, skipping only blocked
accounts, and refuses to write a module account's balance. The bridge's
module accounts (`steemblackhole`, `bridge_reward`) were not blocked. v0.0.7
blocks every module account in x/bank, as upstream cosmos/evm does. It changed
no state and no parameters.

## Every validator: update your checkout and oracle

From your SteemVM folder:

```sh
git fetch origin && git checkout release/v0.0.6 && git pull --ff-only
docker compose --profile go pull oracle-go && docker compose --profile go up -d --no-deps oracle-go
```

Use `python` or `js` instead of `go` if that's the oracle you run. The new
oracle images (`v0.0.7`) fail over to the next Steem node on `Bad Cast` errors
and no longer hit `EOF` / `connection reset` errors from the node.

## If your node missed the upgrade

Your node stopped at height 79664. Its log says `UPGRADE "v0.0.7" NEEDED`, and
it may restart over and over. Nothing is lost. After the `git pull` above:

```sh
docker compose pull steemvm
docker compose up -d steemvm
docker compose logs -f steemvm
```

The compose file now runs the v0.0.7 image. Your cosmovisor still points at
`genesis`, so the node starts the v0.0.7 binary, applies the upgrade at 79664
itself and catches up. Wait until it's synced:

```sh
docker exec steemvm-node /root/go/bin/steemvmd status | jq '.sync_info.catching_up'   # false
docker exec steemvm-node /root/.steemvm/cosmovisor/current/bin/steemvmd version        # 0.0.7
```

**If that doesn't work** (for example the log shows `wrong Block.Header.AppHash`),
resync the node with state-sync instead. This keeps your keys, and it also
unjails and updates the oracle:

```sh
sudo ./update.sh
```

## Unjail

If your validator was jailed while the node was down, unjail it **once the
node has caught up** (`catching_up: false`):

```sh
docker exec -it steemvm-node /root/go/bin/steemvmd tx slashing unjail \
  --from <your-key> --home /root/.steemvm --chain-id steemvm-2 \
  --gas auto --gas-adjustment 1.5 --gas-prices 1000000000asteem -y
```

If your keyring has a passphrase, you'll be asked for it. Check that you're
bonded again:

```sh
docker exec steemvm-node /root/go/bin/steemvmd query staking validators --home /root/.steemvm --output json \
  | jq -r '.validators[] | "\(.description.moniker) \(.status) jailed=\(.jailed)"'
```

## Check the upgrade

```sh
docker exec steemvm-node /root/go/bin/steemvmd query upgrade applied v0.0.7 --home /root/.steemvm   # height 79664
```
