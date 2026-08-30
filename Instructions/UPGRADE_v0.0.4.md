# Upgrade v0.0.4 — security patch (cosmos/evm + cosmos-sdk) + withdrawal-payout hardening

## What this upgrade is and why it's urgent

Cosmos Labs disclosed that chains running `cosmos/evm` older than v0.6.2/v0.7.2 should immediately
upgrade. The bug is a balance **underflow** in the EVM `StateDB.SubBalance`: subtracting more than an
account's balance silently wraps around to ~2^256 instead of erroring. It has already been used to
drain MANTRA, TAC, and KiiChain (Aug 20–22, 2026) via a crafted vesting-account contract calling a
balance-modifying precompile (originally reported via the staking precompile, but the same
`cmn.BalanceHandler` code path is shared by every precompile that touches account balances —
staking, distribution, bank, ics20, gov, and this chain's own `steembridge` `BridgeOut`). Exploitable
by any user via a crafted transaction; no malicious validator required. This chain was pinned to the
vulnerable `cosmos/evm v0.7.1`.

This upgrade bundles three changes into one coordinated event:

1. **`cosmos/evm` v0.7.1 → v0.7.2** — the underflow fix (`SubBalance` now panics on underflow instead
   of wrapping), a `ParseAmount` fix (no-op for this chain — base/extended EVM denom are both
   `asteem`), a `feemarket` gas-overflow clamp, and a `ProcessProposal` handler addition mirrored into
   `app/evm.go`.
2. **`cosmos-sdk` v0.54.3 → v0.54.4** — a separate security release ("important security fixes...
   recommend all chains upgrade using a coordinated upgrade," no public advisory detail available at
   time of writing). Bundled here rather than as a second separate upgrade event.
3. **On-chain enforcement that a withdrawal payout's observed amount/asset match the withdrawal
   record** — `MsgAttestWithdrawalPayout` previously only cross-checked `(steem_txid, op_index)`
   across validators, never the actual amount/asset paid out on Steem. A validator manually relaying
   a payout who sent the wrong asset or amount could previously have that payout confirmed as valid
   anyway. Now the keeper independently compares the validator-reported `amount_millisteem`/`asset`
   against the withdrawal record and rejects a mismatch (benign no-op, no confirmation recorded, audit
   event emitted) rather than silently trusting it.

This is a **state-breaking, coordinated upgrade** — every validator must switch to the new binary at
the exact same block height. It is not safe to update ad hoc or at your own pace.

## What changed (for anyone reviewing the diff)

- `go.mod` / `go.sum` — the two version bumps above.
- `app/upgrades.go` — new `UpgradeNameV004 = "v0.0.4"` handler, additive alongside the existing
  (dormant, never-fired) `v0.0.3` one. No store migrations — neither dependency bump nor the payout
  change adds a new store key.
- `app/evm.go` — `SetProcessProposal` wired in alongside the existing `SetPrepareProposal`.
- `app/ante_steembridge.go` — re-diffed against upstream `ante/cosmos.go` at v0.7.2: confirmed no
  changes needed (that release's diff doesn't touch the ante chain).
- `Makefile` — `VERSION` changed from `v0.0.3-Beta-2` to `0.0.4` (dropped the leading `v`). This
  fixes a latent, previously-unexercised bug: `docker-entrypoint.sh` stages the cosmovisor upgrade
  binary at `cosmovisor/upgrades/v$(steemvmd version)/bin/`, prepending its own `v` — a `VERSION`
  value that already started with `v` produced a double-`v` directory that could never match an
  on-chain `Plan.Name`. Verified via a real throwaway-container devnet boot that `steemvmd version`
  now prints `0.0.4` and stages at `cosmovisor/upgrades/v0.0.4/bin/steemvmd`.
- `precompiles/steembridge/steembridge.go` — doc comment noting `BridgeOut`'s shared exposure to the
  patched code path.
- `proto/steemvm/steembridge/v1/tx.proto`, `x/oracle/bridge/types/tx.pb.go` — new
  `amount_millisteem` (field 7) / `asset` (field 8) fields on `MsgAttestWithdrawalPayout`.
- `x/oracle/bridge/keeper/msg_server_attest_withdrawal_payout.go` — the new amount/asset validation,
  mirrored into the fee-exemption ante-decorator acceptance check.
- `x/oracle/bridge/types/events.go`, `msg_server_bridge_out.go`, `msg_server_submit_steem_deposit.go`,
  `deposit.go` — a new `asset` attribute on withdrawal/deposit created/minted/burned events (so an
  event consumer can tell STEEM from SBD without a follow-up query), and a new
  `withdrawal_payout_asset_mismatch` audit event.
- `oracle/go/relayer/`, `oracle/python/src/steemvm_oracle/`, `oracle/js/src/` — all three oracle
  client implementations updated in lockstep: the payout-detection logic now parses the actually
  observed amount/asset from the Steem transfer operation and reports it (never echoes back the
  expected value — that would defeat the on-chain check). See `oracle/PROTOCOL.md` §5 for the spec.

Verified via throwaway Docker containers (not assumed): `go build`/`go vet`/`go test ./...` all clean;
a real devnet boot (init → gentx → collect-gentxs → start) produced blocks with zero panics; the Go,
Python, and JS oracle clients all build/type-check and pass their test suites.

**Not yet done, and blocking before this should be considered fully verified**: a live exploit-replay
test (deploy a vesting-account contract via EVM, trigger the underflow through a precompile, confirm
it's cleanly rejected post-upgrade without crashing the node) — this needs real EVM/Solidity tooling
that hasn't been set up yet.

## Validator upgrade instructions

### If you run via Docker Compose + cosmovisor (the default `docker compose up` path)

Cosmovisor auto-swaps the binary at the exact upgrade height — but only if the new binary is already
**staged** before that height arrives.

**Do NOT `docker compose pull && docker compose up -d` (or `--build`) onto your already-running
validator before the upgrade height actually arrives.** `docker-entrypoint.sh` refreshes
`cosmovisor/genesis/bin/` on every container start whenever `cosmovisor/current` still points at
`genesis` — true for every validator on this chain right now, since no coordinated upgrade has fired
yet. Restarting your live container on the v0.0.4 image early would silently promote v0.0.4 to
`genesis/bin`, and cosmovisor would start running it immediately, ahead of every other validator —
an AppHash-divergence risk, not a safe pre-stage.

Instead, pre-stage the binary with a **one-off service that never touches your running container**:

1. Pull this branch (to get the compose file's `stage-v0.0.4` service — you do NOT need to rebuild
   or restart your live node for this step):
   ```sh
   git pull
   git checkout release/v0.0.4-security-upgrade   # or whatever this lands as after merge/tag
   docker compose --profile stage run --rm stage-v0.0.4
   ```
   This pulls the published `steemblazer/steemvmd:v0.0.4` image, copies its binary straight into
   `cosmovisor/upgrades/v0.0.4/bin/steemvmd`, and exits — it never touches `cosmovisor/genesis/bin`
   and never runs cosmovisor, so your live node keeps running exactly what it's running now,
   completely undisturbed.
2. Confirm the binary staged correctly:
   ```sh
   docker exec <container> ls /root/.steemvm/cosmovisor/upgrades/v0.0.4/bin/
   # must show: steemvmd
   docker exec <container> /root/.steemvm/cosmovisor/upgrades/v0.0.4/bin/steemvmd version
   # must print: 0.0.4
   ```
3. Do this well before the target height — don't wait until the last minute. If the binary isn't
   staged when the chain reaches the upgrade height, your node will halt at that height (cosmovisor
   can't swap to a binary that isn't there) until you stage it and restart.
4. At the upgrade height, cosmovisor halts the old process, swaps `current` to point at
   `cosmovisor/upgrades/v0.0.4/`, and restarts automatically. Watch your logs
   (`docker compose logs -f steemvm`) around the target height to confirm the swap and continued
   block production.
5. **No automatic backup is taken** — `docker-compose.yml` sets `UNSAFE_SKIP_BACKUP: "true"`. If you
   want a rollback point, snapshot your node's data directory manually before the upgrade height.
6. **Only after** the swap is confirmed (`docker exec <container> /root/go/bin/steemvmd query
   upgrade applied v0.0.4 --home /root/.steemvm` succeeds) is it safe to also update your live
   `steemvm` service onto the published image for future restarts — at that point `cosmovisor/current`
   points at `upgrades/v0.0.4`, not `genesis`, so `docker-entrypoint.sh`'s genesis-refresh no longer
   applies and `docker compose pull && docker compose up -d` is safe again:
   ```sh
   docker compose pull steemvm
   docker compose up -d steemvm
   ```
   This isn't required for the upgrade itself to work (cosmovisor already swapped using the staged
   binary) — it just means your next ordinary restart won't rebuild from source.

### If you run the bare `steemvmd` binary directly (no Docker)

You don't need Docker to get cosmovisor's auto-swap — install cosmovisor as a sibling binary and run
under it instead of running `steemvmd` directly. This is the **recommended** path: build ahead of
time, cosmovisor swaps automatically at the exact height, same as the Docker Compose path, no manual
timing required. A **fully manual, precisely-timed** fallback is also given below for anyone who
genuinely doesn't want cosmovisor.

#### Recommended: install cosmovisor, then build prior and let it auto-swap

1. Install cosmovisor once (matches the exact version this repo's own Docker image uses — its
   dependency graph needs an older Go toolchain than this repo's own `steemvmd` build, hence the
   pinned `GOTOOLCHAIN`):
   ```sh
   GOTOOLCHAIN=go1.25.10 go install cosmossdk.io/tools/cosmovisor/cmd/cosmovisor@v1.7.0
   ```
2. Set the env vars cosmovisor needs (put these in your shell profile or systemd unit's
   `Environment=` lines — whatever currently launches `steemvmd`):
   ```sh
   export DAEMON_HOME=<your existing --home, e.g. $HOME/.steemvm>
   export DAEMON_NAME=steemvmd
   export DAEMON_RESTART_AFTER_UPGRADE=true
   export DAEMON_ALLOW_DOWNLOAD_BINARIES=false
   export UNSAFE_SKIP_BACKUP=true
   ```
3. **Point cosmovisor's `genesis/bin` at your CURRENT (already-running, pre-upgrade) binary — not
   the new one.** This is the same trap the Docker path had to be fixed for: if you initialize
   cosmovisor with the new v0.0.4 binary here, it becomes what cosmovisor runs on its very next
   start, ahead of the coordinated height.
   ```sh
   which steemvmd   # or wherever your currently-running binary actually is
   cosmovisor init /path/to/your/CURRENTLY-RUNNING/steemvmd
   ```
4. Build the new binary **into a separate checkout or temp location** so it doesn't overwrite the
   binary you just pointed `genesis/bin` at, then stage it into cosmovisor's upgrade slot directly —
   never run it yet:
   ```sh
   git pull && git checkout release/v0.0.4-security-upgrade   # or wherever this lands
   make install   # builds to $GOBIN (or $GOPATH/bin if GOBIN is unset), NOT your live binary's path
   NEWBIN="$(go env GOBIN)"; [ -z "$NEWBIN" ] && NEWBIN="$(go env GOPATH)/bin"
   "$NEWBIN/steemvmd" version   # confirm: 0.0.4
   mkdir -p "$DAEMON_HOME/cosmovisor/upgrades/v0.0.4/bin"
   cp "$NEWBIN/steemvmd" "$DAEMON_HOME/cosmovisor/upgrades/v0.0.4/bin/steemvmd"
   ```
5. Switch whatever supervises your node (systemd `ExecStart=`, a screen/tmux command, etc.) from
   `steemvmd start ...` to `cosmovisor run start ...` (same flags), then restart once. This restart
   is a no-op in terms of behavior — cosmovisor's `current` still points at `genesis`, so it launches
   the exact same binary you were already running, just supervised by cosmovisor now instead of
   directly.
6. At the coordinated height, cosmovisor halts the old process, swaps to the staged v0.0.4 binary,
   and restarts automatically — same as the Docker Compose path's step 4.

#### Alternative: fully manual swap, precisely timed (no cosmovisor)

1. Pull this branch and build ahead of time: `git pull && git checkout <branch> && make install`.
2. Confirm `steemvmd version` prints `0.0.4`.
3. Watch your node's height approaching the target height (see below). When it reaches that height,
   the old binary halts on its own (`x/upgrade`'s graceful stop, not a crash — see "will it keep the
   current binary running?" note earlier in this doc). Stop the old process, replace the binary with
   the new one, restart with the same `--home` you already use — timed as close to the halt as you
   can manage, since there's no automation here to do it for you.

### Sanity checks after the upgrade (all operators)

```sh
steemvmd status | grep catching_up   # should be false, node producing/following blocks
steemvmd query upgrade applied v0.0.4 --home <your-home>   # should show the height it applied at
```

## Governance proposal — how the upgrade is scheduled

Standard cosmos-sdk software-upgrade governance flow: a proposal embeds an `x/upgrade` `Plan` with
`name = "v0.0.4"` (must exactly match the `UpgradeNameV004` constant in `app/upgrades.go`, and the
`0.0.4` staged by every operator's build) and a target `height`. Once the proposal passes (this
chain's voting period is 48h; blazed007's bonded power alone is decisive but every operator should
still get advance notice — a security patch is not the moment to let speed mean "others find out
after the fact"), `x/upgrade`'s own logic halts every node at that exact height and, for
cosmovisor-managed nodes with the binary already staged, resumes automatically on the new binary.

See the main session record / operator channel for the exact `submit-proposal`/`vote` commands and
target height used for this specific rollout — the height is time-sensitive (computed from the live
chain's current block rate) and should not be treated as a fixed constant in this document.
