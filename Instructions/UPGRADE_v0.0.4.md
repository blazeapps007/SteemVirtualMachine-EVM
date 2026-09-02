# Upgrade v0.0.4 — security patch (cosmos/evm + cosmos-sdk) + withdrawal-payout hardening

> **Historical record.** This upgrade has already been coordinated and applied live
> on-chain. This document is kept as an audit trail of why it happened and what
> changed — the operational rollout instructions that used to follow have been
> removed since they described a one-time event that is now over. For how to run a
> validator today (including how to stay current for the *next* upgrade), see
> [`Instructions/README.md`](README.md).

## What this upgrade is and why it was urgent

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

This was a **state-breaking, coordinated upgrade** — every validator had to switch to the new binary
at the exact same block height; it was not safe to update ad hoc or at each operator's own pace.

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

**Not fully verified before shipping**: a live exploit-replay test (deploy a vesting-account
contract via EVM, trigger the underflow through a precompile, confirm it's cleanly rejected
post-upgrade without crashing the node) was never run — it needed real EVM/Solidity tooling that
wasn't set up at the time. The coordinated upgrade proceeded on the strength of the vendored
`cosmos/evm` fix itself plus clean `go build`/`go vet`/`go test ./...` and a real devnet boot, not
an exploit-replay test.

The coordinated upgrade applied successfully live: validators moved to `0.0.4` at the target
height, cosmovisor auto-swapped the staged binary for operators who pre-staged it in time, and the
chain has continued producing blocks since. A minority of validators did not complete the upgrade
in time and were jailed as a result — recovering a jailed validator is an ordinary unjail (see
[`Instructions/README.md`](README.md)'s Troubleshooting section), not a re-run of this upgrade.
