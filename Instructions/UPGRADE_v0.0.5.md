# Upgrade v0.0.5 — validator identity hardening + 1 gwei EVM gas floor

A **coordinated** upgrade: every validator switches to the new binary at the same block height,
set by the governance proposal that schedules it. Stage the binary ahead of time and cosmovisor
swaps to it automatically at that height — no action needed at the block itself.

**Your keys, stake and validator identity are untouched.** Nothing to re-register or re-stake.

## What it changes

- **Closes every route around the "no anonymous validators" rule.** Creating or editing a
  validator now always runs the Steem-identity check, whether it arrives as a normal tx, wrapped in
  authz, from an interchain account, or from MetaMask/Solidity through the staking precompile.
  Full detail in [`SECURITY.md`](../SECURITY.md).
- **Turns EVM staking back on.** The staking precompile (`0x…0800`) was switched off by governance
  because it skipped the identity check. v0.0.5 switches it back on, with the check built in:
  delegating, undelegating and redelegating from MetaMask work again, while creating or editing a
  validator from the EVM must pass the same Steem-identity rule as everywhere else.
- **Makes the 1 gwei EVM gas floor part of the chain.** EVM transactions previously paid zero
  fees, so the fee burn burned nothing. The upgrade enforces a 1 gwei minimum on-chain.
- IBC, including interchain accounts, stays fully enabled.

Nothing for oracle operators to do: the oracle images are unchanged and already pay 1 gwei.

## Every validator: one line

From your SteemVM folder:

```sh
git fetch && git checkout release/v0.0.5-validator-identity && ./update.sh
```

That's all. **No reset, no restart, no downtime.** `update.sh` works out how your node runs and
stages the v0.0.5 binary where cosmovisor expects it:

- **Docker node:** it pulls the published image and stages its binary. It prints a `sha256:` line;
  it must match **`5915bf669c390d3f6e08c81961d8aec2818145e1a9155e26a88b080a507d334f`**.
- **Bare-metal node (no Docker installed):** it builds v0.0.5 from source into a temporary folder,
  so your running binary is never overwritten, and stages it. Your node must run under cosmovisor.
- If it says it **can't tell how your node runs**, re-run with `--docker` or `--bare-metal`.

**Staged v0.0.5 before 29 September? Run the line again.** The image published on 24 September was
an earlier build of v0.0.5. It reports the same version but behaves differently at the upgrade, and
a node staging it would split off the network at the upgrade height. Running `update.sh` again
replaces it.

Check out the branch **before** running `update.sh`. The version on the older branch doesn't know
how to stage an upgrade and would reset your node instead.

⚠️ **Until the upgrade height, do not change your node's image and do not run
`docker compose pull && docker compose up -d` on the node.** The compose file deliberately keeps the
live node on `v0.0.4-1` until then. If your node joined after the v0.0.4 upgrade, running the v0.0.5
image early makes cosmovisor start it immediately. The chain then rejects it
(`BINARY UPDATED BEFORE TRIGGER`), your node halts, and you get jailed.

At the upgrade height, cosmovisor stops the old binary, switches to v0.0.5 and restarts on its own.
Once it has applied, Docker nodes can move the live service to the new image for future restarts
(the branch will make this the default):

```sh
docker exec steemvm-node /root/go/bin/steemvmd query upgrade applied v0.0.5 --home /root/.steemvm
STEEMVM_IMAGE=steemblazer/steemvmd:v0.0.5 docker compose up -d steemvm
```

## Doing it by hand instead

**Docker:**

```sh
git fetch && git checkout release/v0.0.5-validator-identity
docker compose --profile stage pull stage-v0.0.5
docker compose --profile stage run --rm stage-v0.0.5
# must print "staged: 0.0.5 …" and the sha256 above
```

**Bare metal (cosmovisor):** `make install` always installs to `$GOBIN`, whichever checkout you
build from, so point `GOBIN` at a scratch directory to avoid overwriting the binary you're running:

```sh
git fetch && git checkout release/v0.0.5-validator-identity
GOBIN="$HOME/steemvmd-v0.0.5" make install
"$HOME/steemvmd-v0.0.5/steemvmd" version            # must print 0.0.5
mkdir -p "$DAEMON_HOME/cosmovisor/upgrades/v0.0.5/bin"
cp "$HOME/steemvmd-v0.0.5/steemvmd" "$DAEMON_HOME/cosmovisor/upgrades/v0.0.5/bin/steemvmd"
```

Leave the node running; cosmovisor swaps at the height.

**Bare metal without cosmovisor:** build the same way. At the upgrade height the old binary halts
by itself with `UPGRADE "v0.0.5" NEEDED`. Replace the binary with the new one and restart with the
same `--home`.

## If you missed staging

Your node halts at the upgrade height with `UPGRADE "v0.0.5" NEEDED at height: …`. Nothing is
lost: stage the binary as above and restart. Every block you're down counts toward downtime
jailing, though, so stage ahead of time.

## Checks after the upgrade

```sh
steemvmd query upgrade applied v0.0.5            # shows the height it applied at
steemvmd status | grep catching_up              # false, height climbing
steemvmd query feemarket params                 # min_gas_price and base_fee = 1000000000
```

If you're jailed afterwards, unjail as usual (see `Instructions/README.md`, Troubleshooting).
