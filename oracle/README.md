# SteemVM oracle clients

Three interchangeable implementations of the same validator oracle duties — pick the language
you're comfortable operating. They share one config file and are mutually exclusive: run exactly
one at a time per validator (they'd race on the same signing key and scan cursor otherwise).

| Client | Directory | Status |
|---|---|---|
| Go | [`go/`](go/) | Reference implementation |
| Python | [`python/`](python/) | See `python/README.md` |
| JS / Node | [`js/`](js/) | See `js/README.md` |

## What they do

Each client performs the full validator oracle duty set:

1. **Bridge attestation** — scans Steem for gateway transfers and payouts, submits
   `MsgAttestDeposit` / `MsgAttestWithdrawalPayout` / `MsgSubmitNameRegistration` to SteemVM.
2. **Price feed** — sources STEEM/USD_External + SBD/USD_External from CoinMarketCap or CoinGecko
   (operator-selected via `ORACLE_PRICE_SOURCE`) and
   STEEM/SBD_Internal + Price_Feed (Steem's own witness-median feed price) from
   the same Steem RPC node, and runs the commit-reveal `MsgAggregateExchangeRatePrevote` /
   `MsgAggregateExchangeRateVote` cycle.
3. **svm.bank multisig signer** (opt-in, `ORACLE_MULTISIG_ENABLED=true`). This duty:
   - pays SVM withdrawals from the @svm.bank gateway;
   - keeps svm.bank's owner, active and posting authorities equal to each validator's exact share
     of bonded stake, summing to exactly 10,000;
   - votes, proposes and executes through the `svm_ms` protocol in each validator's Steem
     metadata.

   It needs the validator's Steem **active** key (`ORACLE_STEEM_ACTIVE_WIF`) and nothing else. The
   client refuses to start if an owner key, posting key or master password is configured. Rules
   are in [`PROTOCOL.md`](PROTOCOL.md) §11; background is in
   [`multisigguide.md`](multisigguide.md). All three clients pass the shared vectors in
   [`multisig/vectors.json`](multisig/vectors.json) and produce byte-identical ballots, so a
   validator set can mix them freely.

See [`Instructions/ORACLE_COMMANDS.md`](../Instructions/ORACLE_COMMANDS.md) for the manual
`steemvmd tx ...` equivalents (useful for one-off attestation or debugging without running a
client), and [`PROTOCOL.md`](PROTOCOL.md) for the exact signing/hashing/formatting rules every
client implements — read that first if you're porting this to a fourth language.

## Running one

```sh
cp oracle/.env.example oracle/.env    # then edit oracle/.env
docker compose --profile go up -d       # or: --profile python / --profile js
```

Never bring up more than one profile at once against the same validator key. For the multisig
signer that's also enforced: it locks the shared state volume, so a second signer refuses to start.

The Python and JS images build from `oracle/`, so they include `multisig/vectors.json`:
`docker build -f oracle/python/Dockerfile oracle`. The Go image builds from the repo root.
