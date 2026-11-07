# Upgrade v0.0.6 — dependency/security refresh + bridge hardening

A **coordinated** upgrade: every validator switches to the new binary at the same block height,
set by the governance proposal that schedules it. Stage the binary ahead of time and cosmovisor
swaps to it automatically at that height — no action needed at the block itself.

**Your keys, stake and validator identity are untouched.** Nothing to re-register or re-stake.

## What it changes

- **Latest stable dependencies.** cosmos/evm `v0.7.3` (an upstream state-breaking security release:
  EVM state commits are atomic when precompiles run, and a balance-overflow path is closed),
  ibc-go `v11.2.0`, the cosmos go-ethereum fork `v1.17.2-cosmos-1` (EIP-712 fix), gRPC `v1.84.0`,
  and Go `1.26.9` (12 standard-library fixes). Cosmos SDK stays `v0.54.4` and CometBFT `v0.39.4` —
  those are the newest releases cosmos/evm supports today.
- **Bridge attestation hardening** (from the October security review):
  - Deposits, name registrations and withdrawal payouts are tallied **per distinct observed
    fact-set**, and resolve the fact-set that reaches the ⅔ threshold. One validator can no longer
    fix the facts by attesting first — a minority reporting a bogus payout txid or wrong deposit
    facts cannot block or redirect the honest majority. This closes a withdrawal double-payout and
    a deposit/name griefing vector.
  - Memo and timestamp fields are size-bounded; the bridge-out message server self-guards against a
    huge-amount panic and enforces the minimum.
  - End-of-block expiry work is bounded per block.
  - A validator that attests correctly but a moment after the threshold crossing is now credited,
    not recorded as a miss.

Nothing for oracle operators to do: the oracle protocol is unchanged and the oracle images already
pay 1 gwei. `update.sh` moves a running oracle to its latest published image as part of the update.

## Every validator: one line

From your SteemVM folder:

```sh
git fetch && git checkout release/v0.0.6 && ./update.sh
```

That's all. **No reset, no restart, no downtime.** `update.sh` works out how your node runs and
stages the v0.0.6 binary where cosmovisor expects it:

- **Docker node:** it pulls the published image and stages its binary. It prints a `sha256:` line;
  it must match **`cf0148411dd28b9c3116735b504dd425dbc9aee9e421160cfce82caf5b13b3eb`**.
- **Bare-metal node under cosmovisor:** it builds v0.0.6 from source into a temporary folder, so
  your running binary is never overwritten, and stages it.
- **Bare-metal node running `steemvmd` directly (no cosmovisor):** it builds v0.0.6 into
  `~/steemvmd-v0.0.6/` and prints the swap steps for the upgrade height. Your running binary is
  never touched.
- If it says it **can't tell how your node runs**, re-run with `--docker` or `--bare-metal`.

⚠️ **Until the upgrade height, do not change your node's image and do not run
`docker compose pull && docker compose up -d` on the node.** The compose file deliberately keeps the
live node on `v0.0.5` (the currently applied version) until then. If your node joined after the
v0.0.5 upgrade, running the v0.0.6 image early makes cosmovisor start it immediately. The chain then
rejects it (`BINARY UPDATED BEFORE TRIGGER`), your node halts, and you get jailed.

At the upgrade height, cosmovisor stops the old binary, switches to v0.0.6 and restarts on its own.
Once it has applied, Docker nodes can move the live service to the new image for future restarts
(the branch will make this the default):

```sh
docker exec steemvm-node /root/go/bin/steemvmd query upgrade applied v0.0.6 --home /root/.steemvm
STEEMVM_IMAGE=steemblazer/steemvmd:v0.0.6 docker compose up -d steemvm
```

## Doing it by hand instead

**Docker:**

```sh
git fetch && git checkout release/v0.0.6
docker compose --profile stage pull stage-v0.0.6
docker compose --profile stage run --rm stage-v0.0.6
# must print "staged: 0.0.6 …" and the sha256 above
```

**Bare metal (cosmovisor):** `make install` always installs to `$GOBIN`, whichever checkout you
build from, so point `GOBIN` at a scratch directory to avoid overwriting the binary you're running:

```sh
git fetch && git checkout release/v0.0.6
GOBIN="$HOME/steemvmd-v0.0.6" make install
"$HOME/steemvmd-v0.0.6/steemvmd" version            # must print 0.0.6
mkdir -p "$DAEMON_HOME/cosmovisor/upgrades/v0.0.6/bin"
cp "$HOME/steemvmd-v0.0.6/steemvmd" "$DAEMON_HOME/cosmovisor/upgrades/v0.0.6/bin/steemvmd"
```

Leave the node running; cosmovisor swaps at the height.

**Bare metal without cosmovisor:** build the same way. At the upgrade height the old binary halts
by itself with `UPGRADE "v0.0.6" NEEDED`. Replace the binary with the new one and restart with the
same `--home`.

## If you missed staging

Your node halts at the upgrade height with `UPGRADE "v0.0.6" NEEDED at height: …` and keeps
restarting on the old binary. Nothing is lost. **Run the same one line as above.** `update.sh`
notices the node already halted at the upgrade height, stages v0.0.6, switches cosmovisor to it and
restarts the node once. Then unjail if you were jailed for the downtime. (Staging by hand is not
enough at this point — see `UPGRADE_v0.0.5.md`, "If you missed staging", for the manual recovery.)

## Checks after the upgrade

```sh
steemvmd query upgrade applied v0.0.6            # shows the height it applied at
steemvmd status | grep catching_up               # false, height climbing
steemvmd version                                  # via cosmovisor/current: 0.0.6
```

If you're jailed afterwards, unjail as usual (see `Instructions/README.md`, Troubleshooting).
