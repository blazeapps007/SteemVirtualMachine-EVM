# SteemVM oracle client protocol

Normative spec for anyone implementing a SteemVM validator oracle client (currently: `oracle/go`,
`oracle/python`, `oracle/js`). The `.proto` files under `proto/steemvm/` are the normative **wire
format**; this document is the normative **procedure** — key derivation, signing, hashing, and
formatting rules that must match byte-for-byte across implementations, since the chain verifies a
committed hash against a later-revealed string, and a signature against an exact digest.

All three clients read the same `oracle/.env` (see `oracle/.env.example`).

## 1. Keys and addresses

- Chain key type: `eth_secp256k1`. Private key: 32 raw bytes.
- Public key: **compressed** secp256k1 point, 33 bytes. This is the wire form of
  `ethsecp256k1.PubKey.Key` and what goes inside the signed tx's `Any`.
- Account address: `Keccak256(uncompressed_pubkey[1:65])[12:32]` — i.e. drop the pubkey's `0x04`
  prefix byte, Keccak256-hash the remaining 64 bytes (X||Y), take the last 20 bytes. This is
  **standard Ethereum address derivation** — no chain-specific twist.
- String form: bech32-encode those 20 bytes with HRP `steem` (standard bech32, not bech32m).
  This is both the tx signer address AND the literal string every duty message's
  `validator`/`Validator` field must carry.
- **`validator` in every one of the five duty messages below is this ACCOUNT address
  (`steem1...`), never the operator address (`steemvaloper1...`).** Easy mistake: these are
  validator-only messages, but they're signed and identified by the bonded account key, not the
  valoper string (valoper is only used in `x/staking` queries).
- Mnemonic-derived keys: BIP39 → BIP32 path `m/44'/60'/0'/0/0` — standard Ethereum HD derivation
  (`ChainCoinType = 60`), identical to MetaMask/`eth-account`/any stock Ethereum HD wallet. No
  custom derivation logic needed.
- Pubkey `Any.type_url`: `/cosmos.evm.crypto.v1.ethsecp256k1.PubKey`; `Any.value` is that type's
  protobuf encoding of `{key: <33-byte compressed pubkey>}` (field 1, `bytes`).
- Bech32 HRPs on this chain (from `app/config.go`): account `steem`, validator operator
  `steemvaloper`, valoper pubkey `steemvaloperpub`, consensus `steemvalcons`. Oracle clients only
  ever need the account HRP.

## 2. Signing (SIGN_MODE_DIRECT)

1. Build `TxBody{messages: [Any{type_url, value}, ...], memo: "", timeout_height: 0}`.
2. Build `AuthInfo{signer_infos: [{public_key: Any(ethsecp256k1.PubKey), mode_info: {single:
   {mode: SIGN_MODE_DIRECT}}, sequence}], fee: {amount, gas_limit, payer: "", granter: ""}}`.
3. Serialize both to protobuf bytes: `body_bytes`, `auth_info_bytes`.
4. Build `SignDoc{body_bytes, auth_info_bytes, chain_id: "steemvm", account_number}` and serialize
   it to protobuf bytes.
5. Digest = `Keccak256(serialized SignDoc)`.
6. Signature = ECDSA-sign the digest with the secp256k1 private key, go-ethereum style
   (`crypto.Sign`): **65 bytes, `R(32) || S(32) || V(1)`**, where `V` is the raw recovery id
   (0 or 1) — NOT Ethereum's 27/28 convention, and NOT a bare 64-byte `R||S` (a 64-byte signature
   happens to also verify on this chain, since `ethsecp256k1.PubKey.verifySignatureECDSA` strips a
   trailing 65th byte if present before checking, but the reference client always produces and
   stores 65 bytes — **implementations must produce 65 bytes for full parity**, not "whichever
   verifies"). Use a canonical **low-S** signature (go-ethereum's `crypto.Sign` always returns
   low-S; libraries like `@noble/curves` and `eth-keys`/`coincurve` do too by default — verify this
   during each implementation's signing spike, don't assume).
7. `TxRaw{body_bytes, auth_info_bytes, signatures: [<65-byte sig>]}`.
   **Critical**: `body_bytes`/`auth_info_bytes` in the final `TxRaw` MUST be the exact same
   serialized bytes used to build the `SignDoc` in step 4 — never re-marshal after signing.
   Protobuf encoding is not guaranteed byte-stable across two marshal calls of the same logical
   message (field order, varint padding can differ), so re-marshaling can silently produce a
   `TxRaw` whose embedded bytes don't match what was actually signed.
8. Broadcast: base64-encode `TxRaw`'s protobuf serialization as `tx_bytes`.

### Message type URLs

| Message | `Any.type_url` |
|---|---|
| `MsgAttestDeposit` | `/steemvm.steembridge.v1.MsgAttestDeposit` |
| `MsgAttestWithdrawalPayout` | `/steemvm.steembridge.v1.MsgAttestWithdrawalPayout` |
| `MsgSubmitNameRegistration` | `/steemvm.steembridge.v1.MsgSubmitNameRegistration` |
| `MsgAggregateExchangeRatePrevote` | `/steemvm.oracle.data.v1.MsgAggregateExchangeRatePrevote` |
| `MsgAggregateExchangeRateVote` | `/steemvm.oracle.data.v1.MsgAggregateExchangeRateVote` |

## 3. Fees and gas

- **Bridge-attestation messages are fee-exempt** for bonded validators (up to 100/validator/block,
  see `app/ante_steembridge.go`). Safe to send with `fee.amount = []`, but `gas_limit` must still be
  set — mirror the Go client's constants: `gasBase = 200_000` + `gasPerMsg = 400_000` per message,
  batched up to `MaxMsgsPerTx = 50` messages/tx.
- **Price-feed messages (`MsgAggregateExchangeRatePrevote`/`Vote`) are NOT fee-exempt.** The
  signing account needs a real `asteem` balance and the tx needs an explicit fee. Default
  `ORACLE_GAS_PRICES` (see `.env.example`): `1000000000asteem`, matching the manual-attestation
  examples in `Instructions/README.md`.

## 4. Account/sequence lookup and broadcast

Recommended path for Python/JS (avoids a gRPC client dependency — the Go client uses raw CometBFT
RPC/gRPC, but REST is simpler and sufficient):

- `GET {NODE_REST}/cosmos/auth/v1beta1/accounts/{address}` → `account_number`/`sequence`.
  **Verify empirically** whether the response's account object is a plain `BaseAccount` or an
  extended type before assuming the JSON shape — do this once per implementation's signing spike.
- `POST {NODE_REST}/cosmos/tx/v1beta1/txs` with `{"tx_bytes": "<base64 TxRaw>", "mode":
  "BROADCAST_MODE_SYNC"}` → `tx_response.code`/`txhash`/`raw_log` (mirrors `BroadcastTxSync`).
- Delivery confirmation: poll `GET {NODE_REST}/cosmos/tx/v1beta1/txs/{hash}` until found, check
  `tx_response.code == 0`. Match the Go client's cadence: ~2s poll interval, ~45s timeout. Only
  advance any local cursor/state past a block once its txs are confirmed delivered — never
  optimistically advance on a bare broadcast-accepted response.
- `{NODE_REST}` defaults to the node's REST API on `:1317`; CometBFT RPC (`:26657`) is the Go
  client's existing fallback/alternative, documented here for completeness only.

## 5. Steem memo routing

**The gateway account is `svm.bank`, hardcoded in every implementation** (`x/oracle/bridge/types.
GatewayAccount` on-chain; `router.GATEWAY_ACCOUNT` / `GATEWAY_ACCOUNT` in each client) — never read
from the chain's `Params.gateway_account`, which still exists on the wire for backward-compat
display only and is ignored by consensus logic. A client that read this from params instead of
hardcoding it would silently trust a stale or wrong configured value; hardcoding closes that off.

From the reference relayer (`oracle/go/relayer/router.go`):

- Inbound transfer to the gateway account, memo starting with `svm-register` → name-registration
  attestation (`MsgSubmitNameRegistration`).
- Any other inbound transfer to the gateway (bare address memo, `svm-deposit <addr>`, unparseable
  memo) → deposit attestation (`MsgAttestDeposit`). The chain — not the client — ultimately decides
  claimability via memo-derived destination parsing.
- **SBD transfers always route as deposits**, regardless of memo content (no name-registration path
  for SBD).
- Outbound transfer FROM the gateway account with memo `svm-withdrawal <id>` → withdrawal payout
  attestation (`MsgAttestWithdrawalPayout`), where `<id>` is the withdrawal record ID.
  `amount_millisteem`/`asset` on this message MUST be parsed from the ACTUAL Steem transfer operation
  being attested (same amount/symbol parsing as an inbound deposit transfer — see the amount-parsing
  rules in this section), never copied from the on-chain `Withdrawal` record's expected value. The
  keeper independently compares the two and rejects (benign no-op, no confirmation recorded) any
  attestation whose reported amount/asset doesn't match what the withdrawal actually expects — a
  client that echoes the expected value back instead of reporting what it observed defeats this
  check entirely and must not do so.

## 6. Dedup / idempotency

- Deposits and name registrations: dedup key `(txid, op_index[, validator])`. Query
  `steembridge deposit-by-txid`/`name-registration-by-txid` before submitting — a validator should
  only attest each key once (matches `alreadyAttested` in the reference relayer).
- Withdrawal payouts: chain-side idempotent — no pre-query strictly required, duplicate attestations
  no-op on-chain.
- Price feed: dedup is inherent to the commit-reveal protocol — one prevote per period per
  validator; the chain overwrites any existing prevote on `Prevote.Set`.

## 7. Price feed: commit-reveal hashing and formatting

- Vote period: `period = current_height / VotePeriod` (query `oracledata` params for `VotePeriod`,
  currently 600 blocks, ~1h). Reveal must land in the period immediately after the commit
  (`prev.PrevotePeriod + 1 == period`) or the commit is abandoned, not late-revealed.
- Commit hash: `GetAggregateVoteHash(salt, exchangeRates, validator) = sha256(f"{salt}:
  {exchangeRates}:{validator}")`, **hex-encoded and truncated to the first 40 characters (20
  bytes)**. `validator` is the bech32 account address (§1).
- Salt: 16 random bytes, hex-encoded (32 hex characters) — fresh per prevote.
- `exchangeRates` string: pairs sorted **lexicographically ascending** (plain byte/codepoint sort —
  note `Price_Feed` sorts FIRST, since `'P' < 'S'`: `Price_Feed` < `SBD/USD_External` <
  `STEEM/SBD_Internal` < `STEEM/USD_External`), rendered as `PAIR:rate` and joined with `,` — **no
  spaces**.
- Rate formatting — **`LegacyDec.String()` canonical decimal, reproduced exactly**: fixed 18
  decimal places, trailing zeros are **never trimmed**. E.g. `1.23` → `"1.230000000000000000"`,
  `0.5` → `"0.500000000000000000"`. A default "format as decimal" call in any language (Python
  `Decimal.normalize()`/`str()`, JS number-to-string, `ethers.formatUnits`) will produce the WRONG
  string here — none of them preserve 18 fixed decimals with no trimming by default. This is the
  single highest-risk formatting detail in the whole protocol: a wrong string either fails
  `ParseExchangeRateTuples` outright (loud, visible) or — worse — produces a *different* string
  than was hashed, which the chain rejects via `ErrHashVerification` on reveal (also loud, but only
  visible one full vote period after the mistake was made).
- Whitelist: `STEEM/USD_External`, `STEEM/SBD_Internal`, `SBD/USD_External`, `Price_Feed`.
  Reject/drop any pair outside the whitelist before building the string — the chain rejects a vote
  carrying a non-whitelisted pair. Note `Price_Feed` is deliberately NOT pair-shaped (no `/`) — it's
  a single blockchain-native value (Steem's own witness-median feed price), not a tradeable market
  rate, so it doesn't get a `BASE/QUOTE` label like the other three. The chain itself doesn't
  enforce any naming format — `Whitelist` is just an opaque list of strings — so this labeling is
  purely a convention this deployment's oracle clients agree on.

### Price source → pair mapping

| Pair | Source |
|---|---|
| `STEEM/USD_External` | CoinMarketCap |
| `SBD/USD_External` | CoinMarketCap |
| `STEEM/SBD_Internal` | Steem `condenser_api.get_ticker`, latest-trade field (internal market) |
| `Price_Feed` | Steem `condenser_api.get_feed_history`, witness median (`current_median_history`) |

CoinMarketCap: batch `STEEM`+`SBD` into one `quotes/latest?symbol=STEEM,SBD&convert=USD` call
rather than two separate calls. Missing/failed pairs are dropped, not fatal — a partial price map
is valid (the chain-side feeder contract treats "fewer pairs than the whitelist" as fine; an empty
map for a whole cycle just means "no vote this period," which the unified slashing engine counts as
a price-duty miss, not a crash).

## 8. State-file schemas

So a validator can switch client language mid-operation without losing progress, both the scan
cursor and the price-feeder's pending commit persist as JSON with this shape (atomic write:
write to a temp file, then rename):

**Steem scan cursor** (`<ORACLE_STATE_DIR>/steem_relayer_state.json`):
```json
{ "last_scanned_block": 12345678 }
```

**Price feeder state** (`<ORACLE_STATE_DIR>/price_feeder_state.json`, kept separate from the
cursor file so the two duties' failure domains stay independent):
```json
{ "prevote_period": 4821, "salt": "a1b2c3...", "exchange_rates": "Price_Feed:0.520000000000000000,..." }
```
Empty/absent file means "nothing pending to reveal."

## 9. Worked test vector

Computed by `oracle/js` (Milestone 2 spike): fixed private key = `1` (32 bytes, big-endian —
secp256k1's generator-point scalar, independently reproducible in any secp256k1 library), a fixed
`MsgAttestDeposit`, and a fixed price-feed rate map. All values below are real output, not
illustrative — reproduce them with `signer.ts`/`priceFeeder.ts` (or an equivalent in another
language) to cross-check a new implementation.

### Key / address

```
private_key_hex:       0000000000000000000000000000000000000000000000000000000000000001
compressed_pubkey_hex: 0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798
address:                steem10e0525sfrf53yh2aljmm3sn9jq5njk7lj48rdt
```

### `MsgAttestDeposit` signing vector

Fixed field values: `validator=<address above>`, `txid="abc123def456abc123def456abc123def456789"`,
`op_index=0`, `steem_block=12345678`, `steem_timestamp="2026-08-17T00:00:00"`, `steem_sender="alice"`,
`gateway_account="steemvm-gateway"`, `amount_millisteem=1000000`,
`memo="svm-deposit steem1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqzxaysz"`, `asset=BRIDGE_ASSET_STEEM`.
`AuthInfo`: single signer, `SIGN_MODE_DIRECT`, `sequence=0`, `fee={amount: [], gas_limit: 600000}`
(fee-exempt). `SignDoc`: `chain_id="steemvm"`, `account_number=0`.

```
body_bytes_hex:      0af4010a282f737465656d766d2e737465656d6272696467652e76312e4d73674174746573744465706f73697412c7010a2c737465656d31306530353235736672663533796832616c6a6d6d33736e396a71356e6a6b376c6a3438726474122761626331323364656634353661626331323364656634353661626331323364656634353637383920cec2f1052a13323032362d30382d31375430303a30303a30303205616c6963653a0f737465656d766d2d6761746577617940c0843d4a3873766d2d6465706f73697420737465656d3171717171717171717171717171717171717171717171717171717171717171717a786179737a
auth_info_bytes_hex: 0a580a500a292f636f736d6f732e65766d2e63727970746f2e76312e657468736563703235366b312e5075624b657912230a210279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f8179812040a020801120410c0cf24
sign_doc_bytes_hex:  0af7010af4010a282f737465656d766d2e737465656d6272696467652e76312e4d73674174746573744465706f73697412c7010a2c737465656d31306530353235736672663533796832616c6a6d6d33736e396a71356e6a6b376c6a3438726474122761626331323364656634353661626331323364656634353661626331323364656634353637383920cec2f1052a13323032362d30382d31375430303a30303a30303205616c6963653a0f737465656d766d2d6761746577617940c0843d4a3873766d2d6465706f73697420737465656d3171717171717171717171717171717171717171717171717171717171717171717a786179737a12600a580a500a292f636f736d6f732e65766d2e63727970746f2e76312e657468736563703235366b312e5075624b657912230a210279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f8179812040a020801120410c0cf241a07737465656d766d
keccak256_digest_hex: 6e0e34f350dc69f8436809664007e133499a17d6137aa36ee1981b8e78afa656
signature_hex:        1b8d87ddc22055791a1529c02e27d2e7995e7ffcf9978c41a5a44a44f9e40fb304ea3be7769a235db03e1dbb99f2c61f0d6f699c5dadb90a4eadad887de882dd00
signature_length:     65 (R||S||V; V = 0x00, a raw recovery id — confirmed low-S: verified against secp256k1.verify(..., {lowS:true}))
```

`tx_bytes` (base64 `TxRaw`, ready to POST as `{"tx_bytes": ..., "mode": "BROADCAST_MODE_SYNC"}` —
note `account_number=0`/`sequence=0` and the placeholder gateway/memo fields mean this exact tx will
be rejected by a real chain unless replayed against an account genuinely at that number/sequence;
it's a byte-format vector, not a tx meant to be broadcast as-is):

```
CvcBCvQBCigvc3RlZW12bS5zdGVlbWJyaWRnZS52MS5Nc2dBdHRlc3REZXBvc2l0EscBCixzdGVlbTEwZTA1MjVzZnJmNTN5aDJhbGptbTNzbjlqcTVuams3bGo0OHJkdBInYWJjMTIzZGVmNDU2YWJjMTIzZGVmNDU2YWJjMTIzZGVmNDU2Nzg5IM7C8QUqEzIwMjYtMDgtMTdUMDA6MDA6MDAyBWFsaWNlOg9zdGVlbXZtLWdhdGV3YXlAwIQ9Sjhzdm0tZGVwb3NpdCBzdGVlbTFxcXFxcXFxcXFxcXFxcXFxcXFxcXFxcXFxcXFxcXFxcXp4YXlzehJgClgKUAopL2Nvc21vcy5ldm0uY3J5cHRvLnYxLmV0aHNlY3AyNTZrMS5QdWJLZXkSIwohAnm+Zn753LusVaBilc6HCwcCm/zbLc4o2VnygVsW+BeYEgQKAggBEgQQwM8kGkEbjYfdwiBVeRoVKcAuJ9LnmV5//PmXjEGlpEpE+eQPswTqO+d2miNdsD4du5nyxh8Nb2mcXa25Ck6trYh96ILdAA==
```

### Price-feed vector

Fixed salt `a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6` and rate map
`{STEEM/USD_External: "0.523", SBD/USD_External: "1.01", STEEM/SBD_Internal: "0.518", Price_Feed: "0.52"}`:

```
exchangeRates string: Price_Feed:0.520000000000000000,SBD/USD_External:1.010000000000000000,STEEM/SBD_Internal:0.518000000000000000,STEEM/USD_External:0.523000000000000000
validator:             steem10e0525sfrf53yh2aljmm3sn9jq5njk7lj48rdt
commit_hash (sha256, truncated 40 hex chars): aa83a62ac7bebf100aa5d5690d2cbd6af8dbf679
```

(Recomputed after the pair-name rename below §7 — `Price_Feed` now sorts first, changing both the
string and the hash from the original vector.)

**Status: offline vectors confirmed** (all key/address/signing-shape/price-hash values above were
computed and independently re-verified, not assumed — see `oracle/js/test/signingVectors.test.ts`
and `oracle/js/test/decimalFmt.test.ts`).

### Python client (Milestone 1): live-broadcast confirmation

The `go.mod` blocker noted above (mid-flight, concurrent dependency-migration on the main checkout)
was worked around for this spike by building a throwaway isolated devnet from the last known-good
pre-migration commit (`git worktree` at a detached commit, no changes to the main checkout) — the
signing/broadcast wire protocol this section pins down is independent of the Go module graph, so a
pre-migration binary is exactly as valid a target as a post-migration one for this purpose. Using a
different fixed key (mnemonic-derived, not the `private_key=1` vector above — see
`oracle/python/tests/test_signing_vectors.py` for the full reproduction), a real
`MsgAttestDeposit` signed and broadcast against that devnet (fresh genesis, bridge enabled, this
key as the sole bonded validator) returned **`tx_response.code == 0`** at height 12
(`gas_used=171697` of `gas_wanted=600000`), with `deposit_created` → `deposit_confirmed`
(`confirmed_ratio=1.000000000000000000`, single validator = 100% of voting power) →
`deposit_minted` events crediting `998000000000000000asteem` (`1000 millisteem × 10^15` gross,
minus a `2000000000000000asteem` bridge fee) to the destination. Signature: 65 bytes, `R‖S‖V`,
`V=0`, low-S — confirming §2's claims exactly. `GET /cosmos/auth/v1beta1/accounts/{address}`
confirmed a plain `BaseAccount` with `account_number`/`sequence` as JSON strings, per §4.

This closes out the "live-broadcast confirmation still outstanding" gap for at least one client
language; **JS (Milestone 2) and Go should still re-run their own live-broadcast half of the spike**
once the Part A dependency migration lands on the main checkout, since their vectors above are
offline-only.

### JS client (Milestone 2): live-broadcast confirmation

Re-run after the cosmos/evm v0.6.0→v0.7.0 migration landed on the main checkout (commits `7c378a2`,
`c439eb6`), so this spike built the current post-migration `steemvmd` directly — no worktree/old-commit
workaround needed. Devnet: fresh `steemvmd init` (not the tracked `Instructions/genesis.json`, which is
real production validator data and not bootable standalone — see the top of this file's history), with
`app_state.steembridge.params.bridge_enabled = true` and `gateway_account = "steemvm-gateway"` patched
in, `name_service_enabled` left `false` (so the validator-identity ante gate — `app/ante_steembridge.go`
— stays waived and a plain `MsgCreateValidator` gentx works), and `app_state.bank.denom_metadata`
populated with the standard `asteem`/`steem` entry (a fresh `steemvmd init` genesis omits this, and
`cosmos/evm`'s `x/vm` `InitGenesis` panics without it: `"error initializing evm coin info: denom
metadata asteem could not be found"` — unrelated to the bridge, needed for any fresh non-production
genesis on this chain). Also needed: `config.toml`'s `[mempool] type = "app"` (this app always wires its
own EVM-aware mempool as of cosmos/evm v0.7.0; the CometBFT default `"flood"` fails startup with `"EVM
mempool enabled, but comet-bft has invalid config.toml:mempool.type"`), `api.enable = true` and
`api.address = "tcp://0.0.0.0:1317"` in `app.toml` (both default off/loopback-only). One `eth_secp256k1`
key (`steemvmd keys add --key-type eth_secp256k1`) as the sole gentx validator, exported via `steemvmd
keys unsafe-export-eth-key` for use as `SPIKE_PRIVATE_KEY_HEX`.

Using `oracle/js/src/signer.ts` and `src/broadcast.ts` completely unmodified — no hand-rolled
signing/broadcast path — via a throwaway `oracle/js/scripts/spike.ts` that builds a real
`MsgAttestDeposit` and calls `broadcastAttestations()`. Result: **`tx_response.code == 0`** at height 25
(`gas_used=168015` of `gas_wanted=600000`), with `deposit_created` → `deposit_confirmed`
(`confirmed_ratio=1.000000000000000000`, single validator = 100% of voting power) → `deposit_minted`
crediting `997500000000000000000asteem` to the destination (`1,000,000` millisteem × `10^15` =
`1e21` gross, minus the `25`bps bridge fee = `2.5e18`, net `9.975e20` — arithmetic matches
`x/oracle/bridge/types/amounts.go` and the `BridgeFeeBps` default exactly). `GET
/cosmos/auth/v1beta1/accounts/{address}` confirmed a plain `BaseAccount` with `account_number`/
`sequence` as JSON strings, per §4 — `broadcast.ts`'s `fetchAccount()` doc comment is updated to cite
this run directly instead of the earlier, not-yet-backed "empirically verified" claim. Signature format
is confirmed 65 bytes `R‖S‖V` low-S by construction (`signer.ts`'s `signDirect()` always emits that
shape, asserted offline in `test/signingVectors.test.ts`) and confirmed *sufficient* by this run, since
a malformed signature would have failed `SigVerificationDecorator` and never reached deposit resolution.

This closes the JS live-broadcast gap alongside Python's. The throwaway devnet (its container and the
`steemvm-js-devnet-home` volume) was torn down after this spike; nothing under `oracle/js/` depends on
it existing. The shared `steemvm-build-gopath`/`steemvm-build-cache` build-cache volumes were reused
(not created by this spike) and were left in place. **Go (`oracle/go`) is the one client that still
hasn't re-run its own live-broadcast half of the spike** against the post-migration build.
