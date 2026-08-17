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
2. **Price feed** — sources STEEM/USD + SBD/USD from CoinMarketCap and STEEM/SBD + STEEM/FEED from
   the same Steem RPC node, and runs the commit-reveal `MsgAggregateExchangeRatePrevote` /
   `MsgAggregateExchangeRateVote` cycle.

See [`Instructions/ORACLE_COMMANDS.md`](../Instructions/ORACLE_COMMANDS.md) for the manual
`steemvmd tx ...` equivalents (useful for one-off attestation or debugging without running a
client), and [`PROTOCOL.md`](PROTOCOL.md) for the exact signing/hashing/formatting rules every
client implements — read that first if you're porting this to a fourth language.

## Running one

```sh
cp oracle/.env.example oracle/.env    # then edit oracle/.env
docker compose --profile go up -d       # or: --profile python / --profile js
```

Never bring up more than one profile at once against the same validator key.
