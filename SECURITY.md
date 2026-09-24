# Security posture of the published images

Record of the container-image vulnerability pass done for the `v0.0.4-1` rebuild, and of what
is deliberately left unfixed. Regenerate any of these numbers with:

```sh
docker scout cves <image> --only-severity critical,high --format only-packages
```

## What v0.0.4-1 is

A **dependency-only security rebuild of v0.0.4**. No application code, no consensus logic, and
no state-machine dependency changed — so it is *not* a coordinated upgrade, needs no governance
proposal and no upgrade height. Validators pull it whenever convenient.

| | v0.0.4 | v0.0.4-1 |
|---|---|---|
| Go toolchain | 1.26.5 | **1.26.8** |
| cosmovisor | v1.7.0 (pinned build on go1.25.10) | **v1.7.3** (no toolchain pin) |
| cosmovisor's x/crypto | 0.29.0 | **0.56.0** |
| cosmovisor's x/net / grpc | 0.30.0 / 1.68.0 | **0.57.0 / 1.82.1** |
| steemvmd's x/crypto | 0.53.0 | **0.56.0** |

Both images report `version 0.0.4` **on purpose** — the string must keep matching the on-chain
upgrade plan name, and `docker-entrypoint.sh` stages the binary at
`cosmovisor/upgrades/v$(steemvmd version)/bin/`, which is the path `cosmovisor/current` points at
post-upgrade. Bumping it to `0.0.5` with no matching on-chain plan would stage the binary
somewhere cosmovisor never looks and nodes would silently keep running the old one.

**To tell them apart:** `steemvmd version --long` → `go1.26.8` is patched, `go1.26.5` is not.
(The `commit` ldflag is empty in Docker builds because `.git` is not in the build context.)

## Result

| Image | Before | After |
|---|---|---|
| `steemvmd` | 12 Critical / 35 High | **0 Critical / 13 High** |
| `steemvm-oracle-go` | not previously scanned | **0 Critical / 6 High** |
| `steemvm-oracle-python` | not previously scanned | **0 Critical / 3 High** |
| `steemvm-oracle-js` | 2 Critical / 17 High | **0 Critical / 3 High** |

Every Critical is gone fleet-wide. Verified additionally by `go test ./...` (all packages pass)
and a throwaway devnet boot on the patched binary (init → gentx → collect-gentxs → start,
8 blocks committed, no panics).

## Two non-obvious build decisions

**cosmovisor is built inside a throwaway module.** `go install pkg@version` deliberately ignores
local module context, so it always builds against the versions cosmovisor's own `go.mod` pins —
and v1.7.3 still pins x/crypto 0.32.0, x/net 0.34.0 and grpc 1.70.0, which between them were
*every remaining Critical in the node image*. The Dockerfile therefore creates a scratch module,
adds cosmovisor as a tool dependency, upgrades those three, runs `go mod tidy` (required — the
grpc bump pulls spiffe packages that otherwise lack go.sum entries) and builds from there.
cosmovisor is only a process supervisor: not part of consensus, never touches app state.
Collapse this back to a plain `go install` once an upstream release ships current deps.

**The JS oracle drops npm and runs on trixie.** `node:22-slim` is still Debian 12, whose `perl`
carried 2 Criticals; `node:22-trixie-slim` is Debian 13. Separately, npm's own vendored
dependency tree (`tar`, `pacote`, `sigstore`, …) was the single largest source of findings in
that image despite never executing — the runtime only ever runs `node dist/main.js` — so npm is
removed from the runtime stage. Together these took it from 2C/17H to 0C/3H.

## Known-unfixed, and why

**`util-linux` (3 High, all four images).** Debian marks these **not fixed** — there is no
patched package to move to. Present in every Debian-based image on the registry today. Revisit
when Debian ships a fix.

**`cosmos-sdk` 0.50.11 / `cometbft` 0.38.17 / `go-getter` 1.7.7 (inside cosmovisor).** These are
cosmovisor's own pins, not upgradable without patching cosmovisor itself, and they are Highs
rather than Criticals. `go-getter` is the binary-download path, which this deployment disables
outright via `DAEMON_ALLOW_DOWNLOAD_BINARIES: "false"` in `docker-compose.yml`.

**`cometbft` 0.39.4 / `btcd` 0.24.2 / `grpc` 1.82.1 (steemvmd's own).** Deliberately deferred.
cometbft and the cosmos-sdk line are state-machine-relevant: bumping them is a **coordinated,
state-breaking upgrade** of the shape `Instructions/UPGRADE_v0.0.4.md` records, not a patch
rebuild. They are tracked for the next coordinated upgrade rather than smuggled into one that
validators are told they can apply at their own pace.

## What Docker Scout's `recommendations` said, and why it was ignored

`docker scout recommendations` proposed changing the base image. That is a no-op here:
`debian:trixie-slim` is already current, and every alternative tag it listed carries an identical
vulnerability count, with `13`/`latest` being 49 MB against 30 MB. The real exposure was in the
Go layer, which that subcommand does not summarize — use `docker scout cves` instead.

---

# Chain-level fixes in v0.0.5

## Validator identity gate: three bypass routes

Every validator must own an ACTIVE Steem name matching its moniker and publish its Steem public
keys in `details`. That rule lived only in `steembridgeValidatorGateDecorator`
(`app/ante_steembridge.go`), which sees a Cosmos tx's **top-level** messages and nothing else.
Three routes reached x/staking without passing it:

| Route | Why the gate missed it | Fix | Needs a halt? |
|---|---|---|---|
| EVM staking precompile `0x…0800` | EVM txs use cosmos/evm's own ante handler; the precompile calls x/staking's msg server directly | Precompile removed from `active_static_precompiles` (governance vote, enforced again by the v0.0.5 upgrade handler, and absent from fresh-chain defaults) | No |
| authz | `MsgExec{MsgCreateValidator}` is a top-level `MsgExec` | `MsgCreateValidator`/`MsgEditValidator` added to the authz limiter, which also rejects `MsgGrant` for them and recurses into nested `MsgExec` | Yes (ante change) |
| ICA host | The host executes packet messages through the router, never the ante handler; `allow_messages` is `["*"]` | `icaIdentityRouter` (`app/ica_identity_router.go`) runs the gate's own two keeper checks on create/edit; ICA stays fully on | Yes (execution change) |

Gov-executed messages are not a route: `MsgCreateValidator` must be signed by the operator, which
the gov module account cannot be.

**Not exploited on the live chain.** All 12 validators were checked on 2026-09-24: each owns its
moniker's Steem name and carries valid keys. The authz route was proven exploitable on a devnet
running the live v0.0.4-1 binary: a grant plus `MsgExec` created a validator with no identity at
all, while the same attempt after the v0.0.5 upgrade — including one reusing a grant made before
it — is rejected.

**Do not re-activate the staking precompile** without wrapping `createValidator`/`editValidator`
in the same identity check. See `app.StakingPrecompileAddress`.

## EVM gas floor: 1 gwei

Before this, EVM transactions paid **zero** fees, so the 25% fee burn burned nothing (live
`baseFeePerGas` and `eth_gasPrice` were both `0x0`). The feemarket `min_gas_price` floor was 0,
and with unlimited block gas (`max_gas -1`) the EIP-1559 target is unreachable, so the base fee
fell 12.5% every block until it hit that floor. The nodes' `app.toml` 1 gwei minimum did not
help: cosmos/evm skips it for EVM txs once London is active.

The floor is now 1 gwei (`app.EVMGasFloor`), set in three places so it cannot silently regress:
the governance vote (live chain), the v0.0.5 upgrade handler (idempotent, never lowers a higher
value), and fresh-chain genesis defaults.

**It must not exceed 1 gwei**: all three oracle clients pay exactly 1 gwei for price-feed txs
(which are not fee-exempt). A higher floor rejects them and gets validators slashed for missed
duty. Bridge attestations and name confirmations are fee-exempt and unaffected.
