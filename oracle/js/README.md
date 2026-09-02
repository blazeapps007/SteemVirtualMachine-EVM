# SteemVM oracle — JS / TypeScript client

A functionally-equivalent port of [`oracle/go`](../go/): watches Steem for gateway transfers and
payouts and attests them to SteemVM, and (once `ORACLE_GAS_PRICES` is set) runs the commit-reveal
price feed against `x/oracle/data`. Reads the same shared config as the other two clients —
[`oracle/.env.example`](../.env.example) — so a validator can switch language without losing
progress (state-file schema parity, see [`../PROTOCOL.md`](../PROTOCOL.md) §8).

Read [`../PROTOCOL.md`](../PROTOCOL.md) first — it is the normative spec (key derivation, signing,
hashing, decimal formatting, memo routing) this client implements. This README is just "how to run
it."

## Layout

```
src/
  main.ts            entrypoint
  config.ts           env-var config + key loading (hex or BIP39 mnemonic)
  signer.ts             hand-written eth_secp256k1 OfflineDirectSigner (the novel part — see below)
  broadcast.ts            REST account/sequence lookup, tx assembly + signing, broadcast + delivery poll
  steemClient.ts             condenser_api JSON-RPC client + transfer/payout extraction
  router.ts                    memo classification -> attestation message
  relayer.ts                     main scan/attest + price-feeder loop
  priceFeeder.ts                   commit-reveal state machine
  priceSources/{cmc,coingecko,steemPrices}.ts  price sourcing
  decimalFmt.ts                       LegacyDec.String()-compatible decimal formatter (highest-risk piece)
  state.ts                              JSON state-file load/save (atomic writes)
  proto/                                   ts-proto generated types (committed, not gitignored)
test/
  decimalFmt.test.ts        unit tests against PROTOCOL.md §7's worked examples
  signingVectors.test.ts      offline signing/encoding vectors (address derivation, 65-byte
                                sig shape, low-S, pubkey Any encoding) — reproducible without a
                                live chain, built from a well-known test private key (value 1)
```

## The signing stack

No existing JS library signs Cosmos SDK `SIGN_MODE_DIRECT` tx docs with this chain's
`eth_secp256k1` key type, so `signer.ts` hand-rolls it: `@noble/curves` (secp256k1 sign / pubkey)
+ `@noble/hashes` (Keccak256), matching `oracle/PROTOCOL.md` §1–2 exactly (33-byte compressed
pubkey, `Keccak256(uncompressed[1:65])[12:32]` address derivation, 65-byte `R‖S‖V` signature,
canonical low-S).

Everything else in `@cosmjs/proto-signing`'s assembly path (`Registry`, `TxBody`/`AuthInfo`/
`SignDoc` construction) is used unmodified — the one integration nuance: `encodePubkey()` doesn't
know this chain's `/cosmos.evm.crypto.v1.ethsecp256k1.PubKey` type, so `signer.ts`'s `pubkeyAny()`
builds that `Any` manually from the `ts-proto`-generated `PubKey` type and hands it straight to
`AuthInfo`'s `SignerInfo.publicKey` and to `makeAuthInfoBytes`, which already accepts a pre-built
`Any` per signer and never calls `encodePubkey()` itself.

## Protobuf codegen

Only the two SteemVM-custom modules (`steembridge`, `oracledata`) plus `cosmos.evm.crypto.v1.
ethsecp256k1` need generated types — everything else (`TxBody`, `AuthInfo`, `Coin`, ...) comes from
`cosmjs-types`. Regenerate with:

```sh
npm run proto:gen
```

which runs `buf generate` (via the `@bufbuild/buf` / `ts-proto` npm devDependencies — no local Go
or protoc toolchain needed) against this repo's `buf.yaml`/`buf.lock` dependency graph, scoped via
`buf generate --type ...` to just the five duty message types (see `package.json`'s `proto:gen`
script and `buf.gen.yaml`). The `ethsecp256k1.PubKey` type isn't in this repo's own `proto/` tree —
it comes from the `buf.build/cosmos/evm` remote module and needs its own separate `buf generate`
invocation (see the plan / delivery notes for the exact second command).

## Running

```sh
cp ../.env.example ../.env    # then edit oracle/.env with your validator key
docker compose --profile js up -d   # from the repo root
```

## Local development (no local Node/Go needed — everything runs via Docker)

```sh
# install deps
docker run --rm -v "$PWD:/workspace" -w /workspace/oracle/js node:22-slim npm install

# type-check
docker run --rm -v "$PWD:/workspace" -w /workspace/oracle/js node:22-slim npm run build

# tests
docker run --rm -v "$PWD:/workspace" -w /workspace/oracle/js node:22-slim node --import tsx --test test/*.test.ts
```
