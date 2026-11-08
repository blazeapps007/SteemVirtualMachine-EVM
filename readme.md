# SteemVM

**SteemVM is an EVM-compatible Cosmos chain that brings the Steem blockchain's
STEEM token onto a smart-contract platform.**

Steem is a fast, fee-less social blockchain, but it has no virtual machine —
you cannot write contracts against STEEM. SteemVM fixes that. STEEM sent to a
gateway account on Steem is attested by SteemVM's validators and minted 1:1 as
`asteem`, the chain's native gas and staking token (SBD is bridged the same way,
as `asbd`). From there it behaves like any EVM-native asset: send it from
MetaMask, use it in Solidity contracts, stake it, or bridge it back out to Steem.

Mainnet runs release **v0.0.8**, launched from genesis (no earlier upgrades to
replay).

Two things make SteemVM more than a generic bridge:

- **Steem accounts are first-class.** A name service links a Steem username
  (`alice`) to a chain address, verified by validator attestation and confirmed
  by the address owner. Contracts can resolve `alice` on-chain.
- **Validators are accountable Steem participants.** Anonymous validators cannot
  join: every validator must prove ownership of a Steem account and publish that
  account's public keys on-chain (see [Validator identity](#validator-identity)).

Built on **Cosmos SDK v0.54** + **CometBFT**, with **[cosmos/evm](https://github.com/cosmos/evm)**
providing a full Ethereum execution layer (JSON-RPC, ERC-20, EIP-1559 fee market).

## Chain facts

| | |
|---|---|
| Cosmos chain ID | `SteemVM-Manaslu-81630` |
| EVM chain ID | `81630` |
| Address prefix | `steem` (e.g. `steem1…`, operators `steemvaloper1…`) |
| Native denom | `asteem` — 18 decimals, atto-denominated (`1 STEEM = 10^18 asteem`) |
| Bridged SBD | `asbd` — 18 decimals, also an ERC-20 at `0x…0901` |
| Key type | `eth_secp256k1` at BIP44 coin type 60 — **MetaMask-compatible** |
| Block time | ~6s (two Steem blocks) |
| Gas price floor | 1 gwei (`1000000000asteem`) |
| Inflation | none — every coin was bridged in from Steem |
| Bridge threshold | at least 2/3 of bonded voting power must attest |
| Bridge fee | 0.25% each way, paid 100% to stakers |
| Gateway on Steem | [`@svm.bank`](https://steemitwallet.com/@svm.bank), a validator multisig |

**Public endpoints**

| | |
|---|---|
| EVM JSON-RPC | `https://evm.steemvmscan.com` |
| CometBFT RPC | `https://rpc.steemvmscan.com` |
| Cosmos REST | `https://api.steemvmscan.com` |
| Explorer (Cosmos txs) | <https://steemvmscan.com> |
| Explorer (EVM txs) | <https://evmscan.steemvmscan.com> |

Because keys are `eth_secp256k1` at coin type 60, one mnemonic yields the same
account in the CLI and in MetaMask — the `steem1…` and `0x…` forms are two views
of the same 20 bytes. Bridged funds are visible in both without any ERC-20 wrapper.

## Features

**STEEM/SBD ↔ asteem/asbd bridge** (`x/oracle/bridge`, on-chain store key
`steembridge`) — bonded validators attest to transfers reaching `svm.bank` on
Steem. Once attestations reach the ⅔ threshold the chain mints `asteem` (or
`asbd`) to the address in the transfer memo (`svm-deposit <address>`, or just
the address). Voting power is recomputed live on every attestation, so the
threshold is immune to validator-set drift, and nothing from Steem blocks before
the chain's genesis anchor can be attested.

Bridging out burns the coins and records a withdrawal. The validators' oracle
clients pay it from `svm.bank` as one Steem transaction (the transfer with the
user's own memo, plus a `custom_json` naming the withdrawal), signed through the
validator multisig, and then attest the payout. A withdrawal nobody pays within
~3.5 days is refunded automatically. The chain never lets more of an asset be
bridged out than was bridged in.

**Name service** — a Steem user sends `0.001 STEEM` with the memo
`svm-register <address>` to link their username to that address. Validators
attest it; the address owner then confirms, proving control. Re-linking is
supported, and the registration fee is credited to the address so a brand-new
account has gas to confirm with.

**Validator identity** — whenever the name service is enabled, a validator's
`moniker` must be a registered, ACTIVE Steem username **owned by that
validator's own account**, and its `details` field must carry the account's
`owner`/`active`/`posting` public keys. This is enforced in the staking message
server (`app/stakinggate`), so it holds on every route: `create-validator` and
`edit-validator` are rejected otherwise, and the identity cannot be stripped after
the fact.

**Choice of oracle client** — a separate container (never the chain binary
itself) watches Steem and does every validator duty: bridge attestations, the
commit-reveal price feed (STEEM/USD, SBD/USD, STEEM/SBD and Steem's feed price),
and signing for the `svm.bank` multisig (payouts, and keeping its signer weights
equal to each validator's share of stake). When the validator is bonded it scans
Steem's last irreversible block (no fork risk) and broadcasts automatically.
Attestations from bonded validators are fee-exempt. Pick whichever language
you're comfortable operating — Go, Python, or JS, all functionally identical
(see [`oracle/README.md`](oracle/README.md)).

**EVM + precompiles** — full Ethereum JSON-RPC (HTTP + WebSocket) for MetaMask,
`cast`, ethers.js, and friends, plus a set of precompiled contracts exposing
Cosmos SDK and chain-specific functionality to Solidity — see
[Precompiles](#precompiles) below.

## Precompiles

Precompiles are contracts at fixed addresses backed by Go code instead of
EVM bytecode — they let Solidity call straight into Cosmos SDK modules (or
this chain's own state) without a relayer or wrapper token. Every address
below must be listed in `evm.params.active_static_precompiles` to be
callable; all of them are active by default on a fresh SteemVM genesis (see
`cmd/steemvmd/cmd/init_genesis_defaults.go`).

**Standard cosmos/evm precompiles** — shipped by the `cosmos/evm` dependency,
identical on any chain built on it:

| Address | Name | What it does |
|---|---|---|
| `0x…0100` | p256 | Verifies secp256r1 (P-256) signatures on-chain — e.g. passkey/WebAuthn-style auth |
| `0x…0400` | bech32 | Converts between `steem1…` (bech32) and `0x…` (hex) address forms |
| `0x…0800` | staking | Delegate, undelegate, redelegate, and query staking state from Solidity. `createValidator`/`editValidator` enforce the same Steem-identity rule as Cosmos txs |
| `0x…0801` | distribution | Claim/query staking rewards from Solidity; fund a validator's reward pool (`depositValidatorRewardsPool`) |
| `0x…0802` | ics20 | IBC fungible-token transfers initiated from Solidity |
| `0x…0804` | bank | Query and transfer any bank denom (not just `asteem`) from Solidity |
| `0x…0805` | gov | Submit governance proposals and vote from Solidity |
| `0x…0806` | slashing | Query slashing/signing-info and submit unjail from Solidity |
| `0x…0807` | ics02 | Query IBC client/consensus state from Solidity |

**SteemVM-specific precompiles:**

| Address | Name | What it does |
|---|---|---|
| `0x…0900` | steembridge | Bridge + name service: `confirmName`, `bridgeOut` (STEEM only; bridge SBD out with `steemvmd tx steembridge bridge-out`), `resolveName`, `namesOf`, `awaitingRegistrationIds`. Custom static precompile — see [`precompiles/steembridge/ISteemBridge.sol`](precompiles/steembridge/ISteemBridge.sol). |
| `0x…0901` | SBD (dynamic ERC-20) | Wraps the native `asbd` bank denom as a standard ERC-20 contract, so wallets/dApps can hold and transfer bridged SBD like any other token. Not hand-written — registered at genesis via cosmos/evm's `x/erc20` "single token representation" mechanism (see [`app/register_sbd.go`](app/register_sbd.go)), the same pattern any future native-coin-to-ERC20 mapping on this chain would use. |
| `0x…0902` | oracledata | Read-only price-feed queries (the commit-reveal exchange rates from `x/oracle/data`). See [`precompiles/oracledata/IOracleData.sol`](precompiles/oracledata/IOracleData.sol). |

`asteem` itself needs no precompile — as the chain's native EVM denom, `eth_getBalance`/native `transfer`/`value` already work on it directly, the same as ETH on Ethereum mainnet.

## Running a node / becoming a validator

[![Run a SteemVM validator on DigitalOcean](https://img.shields.io/badge/DigitalOcean-Run%20a%20validator-0080FF?style=for-the-badge&logo=digitalocean&logoColor=white)](https://m.do.co/c/ca4fa7f3ca18)

**One command on a fresh Ubuntu server** (e.g. a DigitalOcean Droplet; the
button above is a referral link that signs you up). It asks only for your
Steem username and Steem active key, then installs Docker and runs the whole
validator setup:

```sh
curl -fsSL https://raw.githubusercontent.com/blazeapps007/SteemVirtualMachine-EVM/release/v0.0.8/install.sh | sudo bash
```

Step by step: [One-click validator on DigitalOcean](Instructions/README.md#one-click-validator-on-digitalocean).

**→ See [`Instructions/README.md`](Instructions/README.md).**

That guide is the authoritative path and covers all three ways to run a node:
the automated `new-validator.sh` script (recommended for most people), a
manual Docker Compose walkthrough for anyone who wants control over each
step, and building/running `steemvmd` from source without Docker. All three
converge on the same validator-onboarding steps: creating a key, linking and
confirming your Steem name, bridging in your stake, building `validator.json`,
staking, and attesting transfers. The `Instructions/` directory also holds
the canonical node configuration (`app.toml.example`, `config.toml.example`,
`client.toml`, `genesis.json`) that the Docker setup copies into the node home
on every start.

The fastest way to get a node running is from the repository root. Compose
pulls the published `steemblazer/steemvmd:v0.0.8` image (and builds it from
the `Dockerfile` only if it can't be pulled):

```sh
docker compose up -d
docker compose logs -f steemvm
```

Bring up an oracle client alongside it — pick one language, never more than
one at once — with `docker compose --profile {go,python,js} up -d` (see
[`oracle/README.md`](oracle/README.md)).

## Building from source

**Requirements**

- **Go 1.26.5 or newer** (`go.mod` pins toolchain `go1.26.9`, which Go fetches
  automatically)
- **A C compiler, with `CGO_ENABLED=1`** — not optional. cosmos/evm's secp256k1
  bindings are cgo-based, so a pure-Go build will fail. This also means the
  binary cannot be cross-compiled; build on the target platform. (The Docker
  image handles this for you.)

**Build and install**

```sh
make install
```

This runs `go mod verify` and installs `steemvmd` to `$GOPATH/bin`, stamping the
version, commit, and app name via ldflags. The version comes from the
Makefile's `VERSION` (currently `0.0.8`), not from git, so every validator's
build reports the same string. Check it:

```sh
steemvmd version
steemvmd --help
```

## Development

```sh
make test          # go vet + govulncheck + unit tests
make test-unit     # go test ./...
make test-race     # go test -race ./...
make test-cover    # coverage report -> coverage.html
make bench         # benchmarks
make lint          # golangci-lint
make lint-fix      # golangci-lint --fix
```

**Regenerating protobuf code.** Edit the `.proto` files under `proto/`, then run
`make proto-gen` (needs `ignite`), or generate with buf and move the output into
place (generated files land in a temporary `steemvm/` tree):

```sh
go tool buf generate --template proto/buf.gen.gogo.yaml --path proto/steemvm/steembridge
cp steemvm/x/oracle/bridge/types/*.go x/oracle/bridge/types/
rm -rf steemvm
```

Never hand-edit `*.pb.go` — regenerate instead.

**Repository layout**

| Path | What |
|---|---|
| `app/` | App wiring: depinject app config, plus manual EVM/IBC registration and the custom ante handler |
| `app/stakinggate/` | The validator-identity gate wrapped around x/staking's message server |
| `cmd/steemvmd/` | Node + CLI binary |
| `x/oracle/bridge/` | The bridge, name service, and validator-identity module (store key `steembridge`) |
| `x/oracle/data/` | Price-feed module (commit-reveal, weighted median) |
| `x/oracle/` | Parent unified slashing engine |
| `x/steemvm/` | Placeholder module |
| `precompiles/steembridge/` | Bridge EVM precompile (`0x…0900`) and its Solidity interface |
| `precompiles/oracledata/` | Price read precompile (`0x…0902`) and its Solidity interface |
| `oracle/` | Off-chain validator oracle clients (Go/Python/JS — pick one), their shared protocol spec, and the `svm.bank` multisig guide and test vectors |
| `proto/` | Protobuf definitions (source of truth for `*.pb.go`) |
| `Instructions/` | Canonical node config and genesis, the validator guide, and the oracle CLI command reference |
| `docs/` | OpenAPI spec served by the node's API, plus CLI, gRPC/REST and EVM JSON-RPC references |
| `assets/logos/` | STEEM logos (SVG/PNG) used by wallet and chain registries |
| `install.sh`, `new-validator.sh` | One-command and interactive validator setup |
| `reset.sh`, `update.sh`, `update_peers.sh` | Resync, update and peer-refresh helpers for running nodes |
| `launch-genesis.sh` | Creates a new chain's genesis anchored to Steem (chain launches only) |

CI runs vet and unit tests (`go-unit.yml`, with a separate non-blocking
govulncheck job) and golangci-lint on every pull request, and lints PR titles.

## Release

Bump `VERSION` in the Makefile, then push a tag with a `v` prefix. CI builds
`steemvmd` with `make install` and publishes a prerelease with the tarball and
its checksum:

```sh
git tag v0.0.9
git push origin v0.0.9
```

Every push to the default branch also rebuilds the rolling `latest`
prerelease. Release builds target **linux/amd64 only** — the platform node
operators run. Because of the cgo requirement above, other targets need a
native runner rather than cross-compilation. Docker images
(`steemblazer/steemvmd`, `steemblazer/steemvm-oracle-{go,python,js}`) are
published separately, tagged with the same version.

## Learn more

- [Cosmos SDK docs](https://docs.cosmos.network)
- [cosmos/evm](https://github.com/cosmos/evm) — the EVM execution layer
- [CometBFT docs](https://docs.cometbft.com)
- [Steem developer docs](https://developers.steem.io)
