# Genesis validator setup (single validator, fresh chain)

Bootstraps a brand-new `steemvm` chain with **one validator** (`blazed007`) from scratch: init,
fund the account, seed its Steem identity as an already-ACTIVE name link, gentx, and produce a
final `genesis.json` ready to hand off to `docker compose`. This document stops at the finished
genesis file — starting the node itself is a separate step (`docker compose up -d`), not covered
here.

## Before you start

- No `--keyring-backend test` anywhere in this guide. Every command uses the default **`os`**
  backend, matching how `blazed007` was already created (`steemvmd keys add blazed007` with no
  backend flag). **Every command below will prompt for your keyring passphrase interactively** —
  that's expected, just type it each time.
- **Never mix backends.** The single bug behind the failed run in `logs.log` was exactly this: some
  commands were run with `--keyring-backend test`, others with the default (`os`), against a key
  that only exists in `os`. Keep it consistent across every command in this guide.
- All commands assume `--home ~/.steemvm`. Adjust if you're using a different path.

## 1. Get a clean genesis.json

Re-init with `--overwrite` to get a clean `genesis.json` — **this does not touch your keyring**,
`blazed007` and its mnemonic stay exactly as they are.

```sh
steemvmd init blazed007 --chain-id steemvm --home ~/.steemvm --overwrite
```

`--overwrite` only governs `genesis.json`. `config.toml` is unconditionally rewritten by every
`init` call regardless of the flag (so `mempool.type` always ends up correct), but **`app.toml` is
only written if it doesn't already exist yet** (`cosmos-sdk/server/util.go`, gated on
`os.IsNotExist`) — if you have a stale `app.toml` sitting around from before this repo's binary
started hardcoding `minimum-gas-prices`, `init --overwrite` will silently leave it as-is. If
`steemvmd start` later complains `set min gas price in app.toml`, delete it and let it regenerate:

```sh
rm -f ~/.steemvm/config/app.toml
steemvmd init blazed007 --chain-id steemvm --home ~/.steemvm --overwrite
```

## 2. Capture your address correctly

The `-a` flag is what makes this print *only* the raw address — without it you get the full YAML
block, which is what broke the JSON edit last time.

```sh
ADDR=$(steemvmd keys show blazed007 -a --home ~/.steemvm)
echo "$ADDR"
```

Confirm it printed a single line like `steem1m6ek05g9w966fkk5x46a7uxaskpd3ugpav28q2` — not a
multi-line YAML block — before continuing.

## 3. Fund the account: 3,000,000 STEEM + 3,000,000 SBD

Both `asteem` and `asbd` are 18-decimal, so 3,000,000 × 10¹⁸ = `3000000000000000000000000`
(a 3 followed by 24 zeros) in each:

```sh
steemvmd genesis add-genesis-account blazed007 \
  3000000000000000000000000asteem,3000000000000000000000000asbd \
  --home ~/.steemvm
```

## 4. Enable the bridge/name-service, add denom metadata for both asteem and asbd

A fresh `init` genesis ships with the bridge and name service **disabled**, and `bank.denom_metadata`
completely **empty** — not just missing `asbd`, `asteem` isn't there either. This one is not optional:
`x/vm`'s `InitGenesis` reads `bank.denom_metadata` for the EVM's configured native denom (`asteem`)
and **panics at boot** (`error initializing evm coin info: denom metadata asteem could not be found`)
if it's missing. Patch bridge params and both denoms together with `jq` (install with `apt install jq`
if you don't already have it):

```sh
GENESIS=~/.steemvm/config/genesis.json

jq '.app_state.steembridge.params.bridge_enabled = true
  | .app_state.steembridge.params.bridge_out_enabled = true
  | .app_state.steembridge.params.name_service_enabled = true
  | .app_state.bank.denom_metadata += [{
      "description": "The native staking and gas token of SteemVM, bridged 1:1 from Steem mainchain STEEM.",
      "denom_units": [
        {"denom": "asteem", "exponent": 0, "aliases": ["attosteem"]},
        {"denom": "steem", "exponent": 18, "aliases": []}
      ],
      "base": "asteem", "display": "steem", "name": "Steem", "symbol": "STEEM",
      "uri": "", "uri_hash": ""
    }, {
      "description": "Bridged SBD",
      "denom_units": [
        {"denom": "asbd", "exponent": 0, "aliases": ["attosbd"]},
        {"denom": "sbd", "exponent": 18, "aliases": []}
      ],
      "base": "asbd", "display": "sbd", "name": "Steem Backed Dollar",
      "symbol": "SBD", "uri": "", "uri_hash": ""
    }]
  | .app_state.bank.denom_metadata |= unique_by(.base)' "$GENESIS" > /tmp/genesis.json && mv /tmp/genesis.json "$GENESIS"
```

The trailing `unique_by(.base)` makes this command idempotent — safe to run twice in a row, or to
re-run later if you're not sure whether it already ran, without producing a `duplicate client
metadata for denom asteem` error from `gentx`/`validate-genesis`. If you're fixing forward from that
exact error (you ran an older two-step version of this guide and ended up with a duplicate), just
run this same command again — the dedupe pass cleans it up regardless of how it got duplicated.

## 5. Seed blazed007's Steem name as ACTIVE

No live chain exists yet to run the normal attest → confirm flow, so this seeds the link directly
in genesis state instead. Two pieces are required together — **`active_name_list` alone is not
enough**: `ValidateGenesis` (`x/oracle/bridge/types/genesis.go:120-132`, run by both `gentx` and
`validate-genesis`) cross-checks every `active_name_list` entry against a matching **ACTIVE**
`NameRegistration` record with the same `steem_account` and `derived_destination` — an
`active_name_list` entry with no backing registration fails with `references missing registration
N`. (`InitGenesis` itself is more permissive and wouldn't reject this, but the CLI validation path
runs first and is what you'll actually hit.)

Seed both together — a synthetic `NameRegistration` already in `ACTIVE` status (`id: 0`, amount/txid
are placeholders since there's no real Steem transaction behind a genesis-seeded link) plus the
`active_name_list` entry pointing at it:

```sh
jq --arg addr "$ADDR" '.app_state.steembridge.name_registration_list += [{
    "id": "0",
    "txid": "genesis-blazed007",
    "op_index": 0,
    "steem_block": "0",
    "steem_timestamp": "1970-01-01T00:00:00",
    "steem_account": "blazed007",
    "gateway_account": "svm.bank",
    "amount_millisteem": "1",
    "memo": "genesis",
    "derived_destination": $addr,
    "destination_type": "DESTINATION_TYPE_COSMOS",
    "status": "NAME_REGISTRATION_STATUS_ACTIVE",
    "created_at": "0",
    "awaiting_since": "0",
    "confirmed_at": "0",
    "confirm_tx_hash": "",
    "validator_confirmations": []
  }]
  | .app_state.steembridge.name_registration_list |= unique_by(.id)
  | .app_state.steembridge.name_registration_count = (.app_state.steembridge.name_registration_list | length | tostring)
  | .app_state.steembridge.active_name_list += [{
      "steem_account": "blazed007",
      "address": $addr,
      "registration_id": "0",
      "linked_at": "0"
    }]
  | .app_state.steembridge.active_name_list |= unique_by(.steem_account)' "$GENESIS" > /tmp/genesis.json && mv /tmp/genesis.json "$GENESIS"
```

The trailing `unique_by(...)` passes make this safe to run more than once — if you're fixing forward
from `references missing registration N` or `duplicated steem account in active name list`, just
run this exact command again; the dedupe collapses whatever's already there back to one of each.

Sanity-check both before moving on:

```sh
jq '.app_state.steembridge.active_name_list, .app_state.steembridge.name_registration_list' "$GENESIS"
```

You should see one `active_name_list` object with `"address": "steem1m6ek..."` (a single bech32
string, not a multi-line block) and one `name_registration_list` object with matching
`"steem_account"`/`"derived_destination"` and `"status": "NAME_REGISTRATION_STATUS_ACTIVE"`.

Because this is seeded now, `blazed007` will also pass the validator-identity ante gate for any
*future* live `MsgEditValidator`, without ever needing to run the normal attest/confirm dance.

## 6. gentx

Pick your own self-stake amount — this example uses 100,000 STEEM, leaving 2.9M STEEM +
3M SBD free for operational spending (gas, price-feed votes, etc.). Substitute your real Steem
`owner`/`active`/`posting` public keys in `--details` (optional at genesis since gentx bypasses the
live ante-gate identity check, but worth setting now for a complete record — see step 5's note).

```sh
steemvmd genesis gentx blazed007 100000000000000000000000asteem \
  --chain-id steemvm --moniker blazed007 \
  --details "owner=STM...;active=STM...;posting=STM..." \
  --home ~/.steemvm
```

Watch the output — if this errors, it'll dump the full `Usage:` help text rather than a clean
error message (a quirk of this CLI command), so scroll past the usage block to find the actual
error line at the bottom before assuming it's a flag problem.

## 7. Collect and validate

```sh
steemvmd genesis collect-gentxs --home ~/.steemvm
steemvmd genesis validate-genesis --home ~/.steemvm
```

`validate-genesis` should print nothing and exit cleanly. If it repeats the "unparseable address"
error, go back to step 5 — it means `$ADDR` still holds the wrong value.

## 8. Hand off to docker compose

```sh
cp ~/.steemvm/config/genesis.json Instructions/genesis.json
```

From here, starting the node is `docker compose up -d` against this `Instructions/genesis.json`,
which is a separate step from this guide.
