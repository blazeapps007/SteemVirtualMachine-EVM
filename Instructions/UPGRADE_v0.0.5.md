# Upgrade v0.0.5 — validator identity hardening + 1 gwei EVM gas floor

A **coordinated** upgrade: every validator switches to the new binary at the same block height,
set by the governance proposal that schedules it. Stage the binary ahead of time and cosmovisor
swaps to it automatically at that height — no action needed at the block itself.

**Your keys, stake and validator identity are untouched.** Nothing to re-register or re-stake.

## What it changes

- **Closes every route around the "no anonymous validators" rule.** Creating or editing a
  validator now always runs the Steem-identity check, whether it arrives as a normal tx, wrapped in
  authz, or from an interchain account. The EVM staking precompile (`0x…0800`) stays disabled.
  Full detail in [`SECURITY.md`](../SECURITY.md).
- **Makes the 1 gwei EVM gas floor part of the chain.** EVM transactions previously paid zero
  fees, so the fee burn burned nothing. The upgrade enforces a 1 gwei minimum on-chain.
- IBC, including interchain accounts, stays fully enabled.

Nothing for oracle operators to do: the oracle images are unchanged and already pay 1 gwei.

## Docker Compose validators

1. Get the new compose file **without restarting your node**:

   ```sh
   git fetch && git checkout release/v0.0.5-validator-identity   # or main once merged
   ```

2. Stage the v0.0.5 binary into cosmovisor's upgrade slot. This pulls the image, copies the
   binary into place and exits — it never touches the running node:

   ```sh
   docker compose --profile stage run --rm stage-v0.0.5
   # must print: staged: 0.0.5 -> cosmovisor/upgrades/v0.0.5/bin/steemvmd
   ```

3. **Do not** change the `steemvm` service's image and **do not** run
   `docker compose pull && docker compose up -d` before the upgrade height. The compose file
   deliberately keeps the live node on `v0.0.4-1` until then. If your node joined after the v0.0.4
   upgrade, running the v0.0.5 image early makes cosmovisor start it immediately. The chain then
   rejects it (`BINARY UPDATED BEFORE TRIGGER`), your node halts, and you get jailed.

4. At the upgrade height, cosmovisor stops the old binary, switches to v0.0.5 and restarts on its
   own. Watch it happen:

   ```sh
   docker compose logs -f steemvm
   ```

5. Once it has applied, move the live service to the v0.0.5 image for future restarts. The branch
   will be updated to make this the default; until then, set it explicitly:

   ```sh
   docker exec steemvm-node /root/go/bin/steemvmd query upgrade applied v0.0.5 --home /root/.steemvm
   STEEMVM_IMAGE=steemblazer/steemvmd:v0.0.5 docker compose up -d steemvm
   ```

## Bare-metal validators (cosmovisor)

`make install` always installs to `$GOBIN`, whichever checkout you build from. Point `GOBIN` at a
scratch directory so the build never overwrites the binary you're running:

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
