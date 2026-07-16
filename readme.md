# SteemVM

**SteemVM is an EVM-compatible Cosmos chain that brings the Steem blockchain's
STEEM token onto a smart-contract platform.**

Steem is a fast, fee-less social blockchain, but it has no virtual machine —
you cannot write contracts against STEEM. SteemVM fixes that. STEEM sent to a
gateway account on Steem is attested by SteemVM's validators and minted 1:1 as
`asteem`, the chain's native gas and staking token. From there it behaves like
any EVM-native asset: send it from MetaMask, use it in Solidity contracts, stake
it, or bridge it back out to Steem.

Two things make SteemVM more than a generic bridge:

- **Steem accounts are first-class.** A name service links a Steem username
  (`alice`) to a chain address, verified by validator attestation and confirmed
  by the address owner. Contracts can resolve `alice` on-chain.
- **Validators are accountable Steem participants.** Anonymous validators cannot
  join: every validator must prove ownership of a Steem account and publish that
  account's public keys on-chain (see [Validator identity](#validator-identity)).

Built on **Cosmos SDK v0.53** + **CometBFT**, with **[cosmos/evm](https://github.com/cosmos/evm)**
providing a full Ethereum execution layer (JSON-RPC, ERC-20, EIP-1559 fee market).

## Chain facts

| | |
|---|---|
| Cosmos chain ID | `steemvm` |
| EVM chain ID | `8163` |
| Address prefix | `steem` (e.g. `steem1…`, operators `steemvaloper1…`) |
| Native denom | `asteem` — 18 decimals, atto-denominated (`1 STEEM = 10^18 asteem`) |
| Key type | `eth_secp256k1` at BIP44 coin type 60 — **MetaMask-compatible** |
| Block time | ~6s (two Steem blocks) |
| Bridge threshold | more than 2/3 of bonded stake must attest |

Because keys are `eth_secp256k1` at coin type 60, one mnemonic yields the same
account in the CLI and in MetaMask — the `steem1…` and `0x…` forms are two views
of the same 20 bytes. Bridged funds are visible in both without any ERC-20 wrapper.

## Features

**STEEM ↔ asteem bridge** (`x/steembridge`) — bonded validators attest to STEEM
transfers reaching the gateway account. Once attestations exceed the ⅔ threshold
the chain mints `asteem` to the address named in the transfer memo. Bridging out
burns `asteem` and records a withdrawal for validators to relay back to Steem.
Voting power is recomputed live on every attestation, so the threshold is immune
to validator-set drift.

**Name service** — a Steem user sends `0.001 STEEM` with the memo
`svm-register <address>` to link their username to that address. Validators
attest it; the address owner then confirms, proving control. Re-linking is
supported, and the registration fee is credited to the address so a brand-new
account has gas to confirm with.

**Validator identity** — whenever the name service is enabled, a validator's
`moniker` must be a registered, ACTIVE Steem username **owned by that
validator's own account**, and its `details` field must carry the account's
`owner`/`active`/`posting` public keys. This is enforced in the ante handler:
`create-validator` and `edit-validator` are rejected otherwise, and the identity
cannot be stripped after the fact.

**Built-in Steem relayer** — the node binary itself watches Steem. Point it at a
Steem RPC endpoint and give it a key; when the node's validator is bonded it
scans Steem's last irreversible block (no fork risk) and broadcasts attestations
automatically. Attestations from bonded validators are fee-exempt.

**EVM + precompile** — full Ethereum JSON-RPC (HTTP + WebSocket) for MetaMask,
`cast`, ethers.js, and friends. A native precompile at
`0x…0900` exposes the bridge and name service to contracts and wallets:
`confirmName`, `bridgeOut`, `resolveName`, `namesOf`, `awaitingRegistrationIds`.
See [`precompiles/steembridge/ISteemBridge.sol`](precompiles/steembridge/ISteemBridge.sol).

## Running a node / becoming a validator

**→ See [`Instructions/README.md`](Instructions/README.md).**

That guide is the authoritative path: Docker prerequisites, starting the node,
creating a key, linking and confirming your Steem name, getting faucet coins,
building `validator.json`, staking, and attesting transfers. The
`Instructions/` directory also holds the canonical node configuration
(`app.toml`, `config.toml`, `client.toml`, `genesis.json`) that the Docker setup
copies into the node home on every start.

The fastest way to get a node running is from the repository root:

```sh
docker compose up -d
docker compose logs -f
```

## Building from source

**Requirements**

- **Go 1.25.10 or newer**
- **A C compiler, with `CGO_ENABLED=1`** — not optional. cosmos/evm's secp256k1
  bindings are cgo-based, so a pure-Go build will fail. This also means the
  binary cannot be cross-compiled; build on the target platform. (The Docker
  image handles this for you.)

**Build and install**

```sh
make install
```

This runs `go mod verify` and installs `steemvmd` to `$GOPATH/bin`, stamping the
version, commit, and app name via ldflags. Check it:

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

**Regenerating protobuf code.** Edit the `.proto` files under `proto/`, then
generate and move the output into place (generated files land in a temporary
`steemvm/` tree):

```sh
go tool buf generate --template proto/buf.gen.gogo.yaml --path proto/steemvm/steembridge
cp steemvm/x/steembridge/types/*.go x/steembridge/types/
rm -rf steemvm
```

Never hand-edit `*.pb.go` — regenerate instead.

**Repository layout**

| Path | What |
|---|---|
| `app/` | App wiring: depinject app config, plus manual EVM/IBC registration and the custom ante handler |
| `cmd/steemvmd/` | Node + CLI binary |
| `x/steembridge/` | The bridge, name service, and validator-identity module |
| `x/steemvm/` | Placeholder module |
| `precompiles/steembridge/` | EVM precompile (`0x…0900`) and its Solidity interface |
| `relayer/` | The in-binary Steem relayer |
| `proto/` | Protobuf definitions (source of truth for `*.pb.go`) |
| `Instructions/` | Canonical node config + the validator guide |
| `docs/` | OpenAPI spec served by the node's API |

CI runs unit tests and golangci-lint on every push, and lints PR titles.

## Release

Push a tag with a `v` prefix; CI builds the release assets and creates a draft
release:

```sh
git tag v0.1.0
git push origin v0.1.0
```

Release builds target **linux/amd64 only** — the platform node operators run.
Because of the cgo requirement above, other targets need a native runner rather
than cross-compilation.

## Learn more

- [Cosmos SDK docs](https://docs.cosmos.network)
- [cosmos/evm](https://github.com/cosmos/evm) — the EVM execution layer
- [CometBFT docs](https://docs.cometbft.com)
- [Steem developer docs](https://developers.steem.io)
