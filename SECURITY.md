# Security

Security posture of SteemVM as of release **v0.0.8** (mainnet `SteemVM-Manaslu-81630`, launched
from genesis on v0.0.8): what the chain enforces, what it trusts, what is known and not yet fixed,
and how the published images are built.

**Reporting a vulnerability.** Don't open a public issue. Contact the maintainers privately, for
example with a GitHub security advisory on this repository.

## Dependency set

| Component | Version |
|---|---|
| Go toolchain | 1.26.9 (`go.mod` toolchain; images build on `golang:1.26.9-trixie`) |
| Cosmos SDK | v0.54.4 |
| cosmos/evm | v0.7.3 (includes the upstream fix that makes EVM state commits atomic when precompiles run) |
| CometBFT | v0.39.4 |
| ibc-go | v11.2.0 |
| gRPC | v1.84.0 |
| cosmovisor | v1.7.3, rebuilt against current `x/crypto`, `x/net` and gRPC (see below) |

## What the chain enforces

**Validator identity.** Every validator's moniker must be an ACTIVE Steem name link owned by its
operator, and its `details` must carry the account's owner/active/posting public keys.
`app/stakinggate` wraps x/staking's `MsgServer`, so the rule holds on every route: direct txs,
authz, ICA, governance, and the EVM staking precompile `0x…0800`, which is built on the same gated
server. Only Ethereum txs go to cosmos/evm's ante handler; every Cosmos tx, including one with the
dynamic-fee extension, runs this chain's own ante chain. `MsgCreateValidator`/`MsgEditValidator`
also can't be granted or executed through authz. Re-check the staking provider on every SDK bump and
re-diff `app/ante_steembridge.go` against upstream on every cosmos/evm bump.

**Bridge attestations.** Deposits, name registrations and withdrawal payouts resolve once
validators holding at least 2/3 of bonded voting power attest the **same** facts. Attestations are
tallied per distinct fact-set, so a minority can't fix the facts by attesting first. Attestations
for Steem blocks below the chain's genesis anchor (`relayer_start_block`) are rejected, so a
previous chain's history can't be replayed here.

**Payouts.** A bridge-out is paid as one Steem transaction: the transfer, carrying the user's own
memo, and a `custom_json` naming the withdrawal that only `svm.bank`'s active authority can sign.
Memos mean nothing to the bridge. `MsgAttestWithdrawalPayout` carries the observed recipient, memo,
amount and asset, and both the chain and the relays compare them with the withdrawal. Unpaid
withdrawals are refunded automatically after `withdrawal_timeout_blocks` (~3.5 days).

**No unbacked supply.** Bridge-out is limited per asset to what was bridged in (minted includes the
genesis allocation, which `launch-genesis.sh` requires to be funded on Steem before the anchor).
Bridge-outs to the gateway itself, and memos over 256 bytes or with control characters, are
refused. A gateway-to-itself transfer is never a deposit.

**Module accounts.** Every module account is blocked in x/bank (v0.0.7). This also keeps cosmos/evm
from mirroring precompile transfers into module balances.

**Gas floor.** The feemarket `min_gas_price` is 1 gwei (`app.EVMGasFloor`), set in genesis and by the
upgrade handlers, so EVM fees are never zero. It must not go above 1 gwei: all three oracle clients
pay exactly 1 gwei for price-feed txs, which aren't fee-exempt. Bridge attestations and name
confirmations are fee-exempt (capped per validator per block).

**svm.bank signer keys.** The oracle's multisig signer uses only the validator's Steem **active** key.
It refuses to start if an owner key, posting key or master password is configured, if the active key
doesn't belong to the validator's verified Steem account, or if it is also an owner key.

## Trust assumptions and known risks

- **Validators holding 2/3 of bonded power are trusted for the bridge.** Together they can attest
  deposits that never happened, mint against them and bridge out, and the signers pay because the
  chain says the withdrawal is valid. On 2026-11-09 (22,266.6 STEEM bonded) that took about 44,533
  STEEM of new stake. The planned mitigation is client-side: signers check every minted deposit
  against Steem itself before approving any `svm.bank` transaction.
- **Governance can lower `bridge_confirmation_threshold`.** Params validation only requires
  `0 < threshold ≤ 1`. A proposal passed while nobody else votes (quorum 33.4%, about 11,167 STEEM
  today) could set it low enough for one validator to mint. Validators should watch every
  parameter-change proposal and vote NoWithVeto on one like this. A chain-side minimum of 2/3 is
  planned.
- **`svm.bank` direct control** needs 75% of signer weight, which follows stake.
- **A very late payout can race the automatic refund** and pay twice. Signers never propose a
  payout within `ORACLE_MULTISIG_PAYOUT_MARGIN_BLOCKS` (default 14,400) of the refund; an attestation
  after a refund is a no-op that emits `withdrawal_payout_attested_after_refund`.
- **Public JSON-RPC exposes `debug_*`.** The entrypoint enables it for explorers. Rate-limit or filter
  it at the proxy.

## govulncheck

CI runs `govulncheck` as a separate, non-blocking job. Open findings at v0.0.8:

| Finding | Status |
|---|---|
| `klauspost/compress` GO-2026-6865 | Fixed upstream in v1.20.2; pending a dependency bump |
| `crypto/x509` GO-2026-6809 | Fixed in Go 1.26.10; pending a toolchain bump |
| `x/crypto/openpgp` GO-2026-5932 | Newer `x/crypto` pseudo-version available; pending a bump |
| `pion/dtls` v2 GO-2026-4479 | No fix upstream |
| `cosmos/evm` GO-2025-3684 | Believed a false positive: v0.7.3 reverts precompile state atomically (`RunNativeAction`), and both custom precompiles use it |

Dependency bumps that touch the state machine ship as a coordinated release, not a patch rebuild.

## Image build

The last full image scan (`docker scout cves <image> --only-severity critical,high`) was done for
the v0.0.4-1 rebuild: no Critical findings in any of the four images. Re-run it for each release.

**cosmovisor is built inside a throwaway module.** `go install pkg@version` ignores local module
context, and cosmovisor v1.7.3 still pins `x/crypto` 0.32.0, `x/net` 0.34.0 and gRPC 1.70.0, which
were every remaining Critical in the node image. The `Dockerfile` creates a scratch module, adds
cosmovisor as a tool dependency, upgrades those three, runs `go mod tidy` and builds from there.
cosmovisor is only a process supervisor: not part of consensus, never touches app state, and its
binary-download path is disabled (`DAEMON_ALLOW_DOWNLOAD_BINARIES=false`). Collapse this back to a
plain `go install` once an upstream release ships current dependencies.

**The JS oracle drops npm and runs on trixie.** `node:22-trixie-slim` is Debian 13, and npm's own
vendored dependency tree, the largest source of findings, is removed from the runtime stage (the
runtime only runs `node dist/main.js`).

**Known unfixed in images:** `util-linux` Highs that Debian marks not fixed, present in every
Debian-based image; and cosmovisor's own pins of `cosmos-sdk` 0.50 / `cometbft` 0.38 / `go-getter`,
which can't be upgraded without patching cosmovisor (Highs, not Criticals; `go-getter` is the
disabled download path).

**Version string.** `steemvmd version` comes from the Makefile's `VERSION` and must match the
on-chain upgrade plan name: `docker-entrypoint.sh` stages the binary at
`cosmovisor/upgrades/v$(steemvmd version)/bin/`.
