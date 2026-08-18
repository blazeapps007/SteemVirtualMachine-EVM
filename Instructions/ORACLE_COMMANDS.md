# Oracle command reference

Every `steemvmd` command a validator's oracle duties boil down to — for manual/one-off attestation,
debugging, or verifying what a running oracle client just did. If you're running one of the
`oracle/{go,python,js}` clients, you won't type these by hand day-to-day; see
[`oracle/README.md`](../oracle/README.md) instead. For the exact signing/hashing rules behind the
price-feed commands, see [`oracle/PROTOCOL.md`](../oracle/PROTOCOL.md).

All examples assume the node container and flags used throughout
[`Instructions/README.md`](README.md):

```sh
docker exec -it steemvm-node /root/go/bin/steemvmd <command> \
  --from <your-key-name> --keyring-backend test --home /root/.steemvm \
  --chain-id steemvm --gas auto --gas-adjustment 1.5 \
  --gas-prices 1000000000asteem -y
```

Bridge-attestation commands (§1) are fee-exempt for bonded validators up to 100/validator/block —
the `--gas-prices` flag above is harmless to include (unused when the tx qualifies) and required as
a fallback for non-qualifying submissions. Price-feed commands (§2) are **NOT** fee-exempt — always
include `--gas-prices`.

## 1. `steembridge` — bridge attestations

### `attest-deposit` — bridge-in (deposit) attestation

```sh
tx steembridge attest-deposit [txid] [op-index] [steem-block] [steem-timestamp] \
  [steem-sender] [gateway-account] [amount-millisteem] [memo] [asset]
```

| Arg | Meaning |
|---|---|
| `txid` | Steem transaction ID containing the transfer |
| `op-index` | Index of the transfer operation inside that transaction (usually `0`) |
| `steem-block` | Steem block number the transaction was included in |
| `steem-timestamp` | The Steem block timestamp, exactly as Steem reports it |
| `steem-sender` | Steem account that sent the transfer |
| `gateway-account` | The gateway account the transfer was sent to (must match the hardcoded chain constant, `svm.bank`) |
| `amount-millisteem` | Amount in millisteem: STEEM × 1000 (`1000.000 STEEM` → `1000000`) |
| `memo` | The transfer's memo, verbatim — decides who receives the minted `asteem` |
| `asset` | `BRIDGE_ASSET_STEEM` or `BRIDGE_ASSET_SBD` |

Example:

```sh
docker exec -it steemvm-node /root/go/bin/steemvmd tx steembridge attest-deposit \
  bce1dd3184e39bcd9bdd7886b22681268a708e03 0 95000000 2026-07-10T08:00:00 alice svm.bank \
  1000000 0x9b379Dfd7d22eA756eA79a19B3336192d64DcD1a BRIDGE_ASSET_STEEM \
  --from blazed007 --keyring-backend test --home /root/.steemvm \
  --chain-id steemvm --gas auto --gas-adjustment 1.5 --gas-prices 1000000000asteem -y
```

### `attest-withdrawal-payout` — bridge-out payout attestation

Confirms a gateway payout you (or another validator) already sent on Steem for a `bridge-out`
request, memoed `svm-withdrawal <id>`.

```sh
tx steembridge attest-withdrawal-payout [withdrawal-id] [steem-txid] [op-index] \
  [steem-block] [steem-timestamp]
```

| Arg | Meaning |
|---|---|
| `withdrawal-id` | The on-chain `Withdrawal` record ID (from `bridge-out`, or `query steembridge requested-withdrawals`) |
| `steem-txid` | The Steem transaction ID of the gateway's payout |
| `op-index` | Index of the payout transfer operation |
| `steem-block` | Steem block number the payout was included in |
| `steem-timestamp` | The Steem block timestamp |

### `submit-name-registration` — name-service attestation

```sh
tx steembridge submit-name-registration [txid] [op-index] [steem-block] [steem-timestamp] \
  [steem-account] [gateway-account] [amount-millisteem] [memo]
```

Same fields as `attest-deposit` minus `asset`, plus `steem-account` (the Steem username being
linked) in place of `steem-sender`. The destination confirms afterward with `confirm-name
[registration-id]` (not a validator duty — the destination address signs this itself).

## 2. `oracledata` — price feed (commit-reveal)

**Not fee-exempt.** The signing account needs a real `asteem` balance.

### `aggregate-exchange-rate-prevote` — commit

```sh
tx oracledata aggregate-exchange-rate-prevote [validator] [hash] --gas-prices 1000000000asteem
```

`validator` is your bech32 **account** address (`steem1...`), not `steemvaloper1...`. `hash` is
`sha256(f"{salt}:{exchangeRates}:{validator}")`, hex-encoded, truncated to 40 characters (see
`oracle/PROTOCOL.md` §7). Compute it by hand for a one-off manual vote, e.g. in a shell:

```sh
python3 -c "import hashlib; print(hashlib.sha256(b'MYSALT123:STEEM/USD_External:0.250000000000000000:steem1...').hexdigest()[:40])"
```

### `aggregate-exchange-rate-vote` — reveal

```sh
tx oracledata aggregate-exchange-rate-vote [validator] [salt] [exchange-rates] --gas-prices 1000000000asteem
```

Must be submitted in the vote period **immediately after** the matching prevote, with the exact
same `salt` and `exchange-rates` string used to compute that prevote's hash — a mismatch, or
missing the one-period reveal window, is rejected. `exchange-rates` is a sorted `"PAIR:rate,..."`
CSV, e.g.:

```
Price_Feed:0.245000000000000000,SBD/USD_External:1.010000000000000000,STEEM/SBD_Internal:0.248000000000000000,STEEM/USD_External:0.250000000000000000
```

Whitelist: `STEEM/USD_External`, `STEEM/SBD_Internal`, `SBD/USD_External`, `Price_Feed`. The
`_External`/`_Internal` suffixes mark market pairs by source (external CoinMarketCap price vs.
Steem's own internal market); `Price_Feed` isn't pair-shaped at all — it's Steem's own
witness-median feed price (a single blockchain-native value, not a tradeable rate).

## 3. Query commands (verification)

```sh
# Bridge
steemvmd query steembridge deposit-by-txid <txid> <op-index>
steemvmd query steembridge pending-deposits
steemvmd query steembridge minted-deposits
steemvmd query steembridge requested-withdrawals
steemvmd query steembridge processed-withdrawals
steemvmd query steembridge name-registration-by-txid <txid> <op-index>
steemvmd query steembridge resolve-name <steem-account>
steemvmd query steembridge bridge-statistics

# Price feed
steemvmd query oracledata params
steemvmd query oracledata exchange-rate <pair>          # e.g. STEEM/USD_External
steemvmd query oracledata exchange-rates
steemvmd query oracledata aggregate-prevote <validator>
steemvmd query oracledata aggregate-vote <validator>
```

## Rules that matter

- **Every field must exactly match what's on Steem** (and what other validators submit) — a
  mismatched submission is ignored and doesn't count toward the 2/3 threshold.
- **Each validator can confirm a given `(txid, op-index)` only once.**
- **Copy Steem memos verbatim** — the chain derives the destination from the raw memo in
  consensus; don't "fix" or reformat it.
- **Price-feed reveals must land in the very next vote period** after the matching prevote, or the
  commit is abandoned (not late-revealable).
- **`validator` in every message above is the bech32 account address**, never the operator
  (`steemvaloper...`) address.
