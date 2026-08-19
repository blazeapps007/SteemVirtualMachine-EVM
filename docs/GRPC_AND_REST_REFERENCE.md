# gRPC & REST API Reference

This is a complete reference of every gRPC `Query`/`Msg` service and every grpc-gateway REST
endpoint exposed by `steemvmd`, covering both this chain's custom modules and the standard
Cosmos SDK v0.54.3 / cosmos/evm v0.7.1 / ibc-go v11.0.0 modules it wires up. It was compiled by
reading the actual `.proto` sources — this repo's own `proto/steemvm/**` for the custom modules,
and the vendored dependency proto sources (`google.golang.org` module cache) for everything else
— not by guessing endpoint shapes. Only modules actually present in `app/app_config.go`'s module
set are documented; nothing here is invented.

For the EVM JSON-RPC surface (`eth_*`, `net_*`, `web3_*`) and the two static precompiles
(steembridge at `0x...0900`, oracledata at `0x...0902`), see the companion EVM/precompile
reference doc — this document covers only the Cosmos-side gRPC/REST/grpc-gateway API.

## gRPC vs REST: same service, two transports

Every RPC method below is defined once, in a `.proto` file, as part of a gRPC `service Query` or
`service Msg`. Cosmos SDK's `grpc-gateway` reverse-proxies HTTP/JSON requests onto the exact same
gRPC method handlers, driven by `option (google.api.http) = { get: "..." }` (or `post`)
annotations attached to the RPC in the proto file. So "the REST API" and "the gRPC API" are not
two separate implementations — REST is JSON-over-HTTP routed into the identical gRPC service
method, translating the URL path/query parameters into the same request message fields. Not
every RPC has a `google.api.http` annotation: `Msg` (transaction) services in particular are
almost never REST-annotated, because a state-changing tx must be signed and broadcast as bytes
(`POST /cosmos/tx/v1beta1/txs` or the CometBFT RPC's `broadcast_tx_*`), not called as a bare
HTTP verb. Where a `Msg` RPC has no REST row below, that is why — it is gRPC/CLI/SDK-only, not a
gap in this document. (`cosmos.evm.erc20.v1.Msg/ConvertERC20` and `ConvertCoin` are the sole
odd exception on this chain: their proto declares a `google.api.http.get` annotation even though
they mutate state; see the erc20 section below. Treat that as informational, not as "you can
convert tokens with a bare GET" — a real client still needs to sign and broadcast.)

### Default addresses

| Server | Default bind address | Config key |
|---|---|---|
| gRPC (Cosmos SDK server) | `localhost:9090` | `grpc.address` in `app.toml` |
| REST / grpc-gateway | `tcp://localhost:1317` | `api.address` in `app.toml` |
| grpc-web | multiplexed onto the gRPC port | `grpc-web.enable` in `app.toml` (bool only, no separate address) |
| EVM JSON-RPC (separate doc) | `127.0.0.1:8545` (HTTP), `127.0.0.1:8546` (WS) | `json-rpc.address` / `json-rpc.ws-address` |

These are the Cosmos SDK `serverconfig.DefaultConfig()` values (`DefaultGRPCAddress =
"localhost:9090"`, `DefaultAPIAddress = "tcp://localhost:1317"`), confirmed against
`cmd/steemvmd/cmd/config.go` — `initAppConfig()` only overrides `MinGasPrices` and the embedded
cosmos/evm `EVM`/`JSONRPC`/`TLS` sections; it does not touch the gRPC or API server defaults.
cosmos/evm's own `server/config.EVMConfig` carries a vestigial `DefaultGRPCAddress =
"0.0.0.0:9900"` constant for an alternate grpc-web listener, but that listener is disabled by
default (`DefaultGRPCWebEnable = false`) and is not the path this chain's validators or the
off-chain oracle clients use — the SDK's own `:9090` gRPC server is the one that matters. Both
the gRPC and API servers must be explicitly enabled in `app.toml` (`grpc.enable = true`,
`api.enable = true`) to be reachable; a `docker compose up steemvm` node has both on by default.

### Worked examples

Both examples call the same underlying method: `steemvm.steembridge.v1.Query/BridgeStatistics`,
a parameterless query returning the bridge's running mint/burn/net-outstanding totals.

**grpcurl** (needs server reflection, which the Cosmos SDK grpc server enables by default):

```bash
grpcurl -plaintext localhost:9090 steemvm.steembridge.v1.Query/BridgeStatistics
```

**curl**, via the REST/grpc-gateway path declared in `query.proto`
(`option (google.api.http).get = "/steemvm/steembridge/v1/bridge_statistics"`):

```bash
curl http://localhost:1317/steemvm/steembridge/v1/bridge_statistics
```

Both return the same JSON shape (field names match the proto, gogoproto `customtype` fields like
`cosmossdk.io/math.Int` render as decimal strings):

```json
{
  "total_minted_asteem": "...",
  "total_burned_asteem": "...",
  "net_outstanding": "..."
}
```

To list a service's full method set without this document, `grpcurl -plaintext localhost:9090
list steemvm.steembridge.v1.Query` (or any other package/service name below) works against a
live node because reflection is on.

---

## Custom modules

### steembridge — `x/oracle/bridge` (Go import path; store key & proto package unchanged: `steembridge` / `steemvm.steembridge.v1`)

The Steem↔SteemVM bridge, name service, and validator identity data. Proto source:
`proto/steemvm/steembridge/v1/{query,tx}.proto`. Recall the two chain-specific gotchas this
module carries: the gateway account is the hardcoded Go constant `svm.bank`
(`x/oracle/bridge/types.GatewayAccount`) — the `Params.gateway_account` field below is wire-only
display, consensus code never reads it — and a `Withdrawal` that sits `REQUESTED` past
`withdrawal_timeout_blocks` is auto-refunded by `EndBlock` logic with no attestation
(`WithdrawalStatus_REFUNDED`), not by any RPC call.

**Query service** — `steemvm.steembridge.v1.Query` (20 methods, all REST-annotated, all GET)

| RPC | REST path | Description |
|---|---|---|
| `Params` | `/steemvm/steembridge/v1/params` | Module params (bridge enable flags, confirmation threshold, min/max bridge amount, fee bps, timeouts, etc.). |
| `GetDeposit` | `/steemvm/steembridge/v1/deposit/{id}` | Single deposit record by internal numeric id. |
| `ListDeposit` | `/steemvm/steembridge/v1/deposit` | Paginated list of all deposit records. |
| `GetWithdrawal` | `/steemvm/steembridge/v1/withdrawal/{id}` | Single withdrawal record by internal numeric id. |
| `ListWithdrawal` | `/steemvm/steembridge/v1/withdrawal` | Paginated list of all withdrawal records. |
| `PendingDeposits` | `/steemvm/steembridge/v1/pending_deposits` | Deposits still accumulating validator attestations (below 2/3 threshold). |
| `MintedDeposits` | `/steemvm/steembridge/v1/minted_deposits` | Deposits that reached threshold and minted `asteem`/`asbd`. |
| `RequestedWithdrawals` | `/steemvm/steembridge/v1/requested_withdrawals` | Bridge-out requests burned on-chain but not yet paid out on Steem (status `REQUESTED`). |
| `ProcessedWithdrawals` | `/steemvm/steembridge/v1/processed_withdrawals` | Withdrawals attested as paid out on Steem (status `PROCESSED`). |
| `DepositByTxid` | `/steemvm/steembridge/v1/deposit_by_txid/{txid}/{op_index}` | Look up a deposit by its Steem-side (txid, op_index) key — the dedup key attestations match on. |
| `BridgeStatistics` | `/steemvm/steembridge/v1/bridge_statistics` | Running totals: `total_minted_asteem`, `total_burned_asteem`, `net_outstanding` (all `math.Int` strings). |
| `ResolveName` | `/steemvm/steembridge/v1/name/{steem_account}` | Resolve an active Steem-account name link to its SteemVM address. |
| `NamesByAddress` | `/steemvm/steembridge/v1/names_by_address/{address}` | Paginated list of active name links owned by an address. |
| `NameRegistration` | `/steemvm/steembridge/v1/name_registration/{id}` | Single name registration record by id (any lifecycle status). |
| `NameRegistrationByTxid` | `/steemvm/steembridge/v1/name_registration_by_txid/{txid}/{op_index}` | Look up a name registration by its Steem-side (txid, op_index) key. |
| `NameRegistrationsByAccount` | `/steemvm/steembridge/v1/name_registrations_by_account/{steem_account}` | All registrations ever submitted for a given Steem account (any status). |
| `PendingNameRegistrations` | `/steemvm/steembridge/v1/pending_name_registrations` | Registrations still accumulating validator attestations. |
| `AwaitingNameRegistrations` | `/steemvm/steembridge/v1/awaiting_name_registrations` | Registrations at threshold, waiting on the destination's `MsgConfirmName`. |
| `AwaitingNameRegistrationsByDestination` | `/steemvm/steembridge/v1/awaiting_name_registrations_by_destination/{address}` | The subset of `AwaitingNameRegistrations` a given address can confirm. |
| `ValidatorIdentity` | `/steemvm/steembridge/v1/validator_identity/{validator_address}` | The Steem `owner`/`active`/`posting` pubkeys and username parsed from a validator's staking `Description.details` (accepts valoper, account, or 0x form). |

**Msg service** — `steemvm.steembridge.v1.Msg` (6 methods, no REST annotations — sign & broadcast as a tx)

| RPC | Description |
|---|---|
| `UpdateParams` | Gov-authority-only params update. |
| `AttestDeposit` | A bonded validator's attestation of a Steem gateway deposit (STEEM or SBD); at ≥2/3 bonded power the deposit resolves and mints. Fee-exempt under the custom ante handler (capped per-validator per-block). |
| `BridgeOut` | Burns `asteem`/`asbd` and writes an immutable `REQUESTED` withdrawal record; no automated Steem-side payout follows from this call alone. |
| `AttestWithdrawalPayout` | A bonded validator's attestation that the gateway paid a bridge-out on Steem; at threshold flips the withdrawal to `PROCESSED`. Fee-exempt. A late attestation against an already-`REFUNDED` withdrawal is a benign no-op (emits `withdrawal_payout_attested_after_refund`). |
| `SubmitNameRegistration` | A bonded validator's attestation that a Steem account sent a qualifying transfer requesting a name link. Fee-exempt. |
| `ConfirmName` | Signed by the memo-derived destination address to accept a registration that reached the confirmation threshold. Fee-exempt. |

### oracledata — `x/oracle/data` (store key `oracledata`, proto package `steemvm.oracle.data.v1`)

The commit-reveal price-feed module (STEEM/USD, SBD/USD, STEEM/SBD, STEEM/FEED). Proto source:
`proto/steemvm/oracle/data/v1/{query,tx}.proto`.

**Query service** — `steemvm.oracle.data.v1.Query` (5 methods, all REST-annotated, all GET)

| RPC | REST path | Description |
|---|---|---|
| `Params` | `/steemvm/oracle/data/v1/params` | Vote period, vote threshold, reward/miss bands, whitelisted pairs. |
| `ExchangeRate` | `/steemvm/oracle/data/v1/exchange_rate/{pair}` | The finalized power-weighted-median rate for a single pair (e.g. `STEEM/USD_External`). |
| `ExchangeRates` | `/steemvm/oracle/data/v1/exchange_rates` | All finalized rates. |
| `AggregatePrevote` | `/steemvm/oracle/data/v1/aggregate_prevote/{validator}` | A validator's currently-stored commit hash (not yet revealed). |
| `AggregateVote` | `/steemvm/oracle/data/v1/aggregate_vote/{validator}` | A validator's currently-revealed vote (all pairs). |

**Msg service** — `steemvm.oracle.data.v1.Msg` (3 methods, no REST annotations)

| RPC | Description |
|---|---|
| `AggregateExchangeRatePrevote` | Submits the commit hash of a validator's upcoming vote (bonded validators only). |
| `AggregateExchangeRateVote` | Reveals the salt and rates for a previously committed prevote, verified against the hash. |
| `UpdateParams` | Gov-authority-only params update. |

### oracle (parent) — `x/oracle` (store key `oracle`, proto package `steemvm.oracle.v1`)

The unified slashing engine both duty modules report participation into. **No `Query` service is
defined** (confirmed by reading `proto/steemvm/oracle/v1/*.proto` — only `params.proto`,
`tally.proto`, `genesis.proto`, and `tx.proto` exist; there is no `query.proto` in this package),
so slash-window params and evaluation state are not directly gRPC/REST-queryable — they surface
indirectly through `x/slashing`'s signing-info query (jailing) and this module's events.

**Msg service** — `steemvm.oracle.v1.Msg` (1 method, no REST annotation)

| RPC | Description |
|---|---|
| `UpdateParams` | Gov-authority-only update of `slash_window`, `slash_fraction`, `min_valid_per_window`, `w_price`/`w_bridge` duty weights, `duty_floor`, `reward_distribution_window`, `bridge_grace_blocks`. |

### steemvm — `x/steemvm` (store key `steemvm`, proto package `steemvm.steemvm.v1`)

An ignite-scaffolded placeholder module — effectively a `Params`-only shell, not to be confused
with the bridge. Proto source: `proto/steemvm/steemvm/v1/{query,tx}.proto`.

**Query service** — `steemvm.steemvm.v1.Query` (1 method, REST-annotated)

| RPC | REST path | Description |
|---|---|---|
| `Params` | `/steemvm/steemvm/v1/params` | The module's (currently empty) parameter set. |

**Msg service** — `steemvm.steemvm.v1.Msg` (1 method, no REST annotation)

| RPC | Description |
|---|---|
| `UpdateParams` | Gov-authority-only params update. |

---

## Standard Cosmos SDK v0.54.3 modules

Every module below is wired in `app/app_config.go`'s `appconfig.Compose(...)` module list. Proto
sources read from the vendored `github.com/cosmos/cosmos-sdk@v0.54.3` module cache.

### auth — `cosmos.auth.v1beta1`

**Query** (10 methods, all REST GET)

| RPC | REST path | Description |
|---|---|---|
| `Accounts` | `/cosmos/auth/v1beta1/accounts` | All existing accounts (paginated; can be gas-heavy). |
| `Account` | `/cosmos/auth/v1beta1/accounts/{address}` | Account details for a bech32 address. |
| `AccountAddressByID` | `/cosmos/auth/v1beta1/address_by_id/{id}` | Address for an account number. |
| `Params` | `/cosmos/auth/v1beta1/params` | Auth module params (max memo chars, tx sig limit, etc.). |
| `ModuleAccounts` | `/cosmos/auth/v1beta1/module_accounts` | All module accounts (fee_collector, distribution, steembridge, etc.). |
| `ModuleAccountByName` | `/cosmos/auth/v1beta1/module_accounts/{name}` | A single module account by name (e.g. `steembridge`, `bridge_reward`, `steemblackhole`). |
| `Bech32Prefix` | `/cosmos/auth/v1beta1/bech32` | The chain's bech32 prefix (`steem`). |
| `AddressBytesToString` | `/cosmos/auth/v1beta1/bech32/{address_bytes}` | Raw bytes → bech32 string. |
| `AddressStringToBytes` | `/cosmos/auth/v1beta1/bech32/{address_string}` | Bech32 string → raw bytes. |
| `AccountInfo` | `/cosmos/auth/v1beta1/account_info/{address}` | Common `BaseAccount` info (sequence, account number, pubkey) for any account type. |

**Msg** (1 method, no REST)

| RPC | Description |
|---|---|
| `UpdateParams` | Gov-authority-only params update. |

### bank — `cosmos.bank.v1beta1`

The `asteem`/`asbd` balances live here. **Query** (13 methods, all REST GET)

| RPC | REST path | Description |
|---|---|---|
| `Balance` | `/cosmos/bank/v1beta1/balances/{address}/by_denom` | Balance of one denom for one account. |
| `AllBalances` | `/cosmos/bank/v1beta1/balances/{address}` | All coin balances for an account. |
| `SpendableBalances` | `/cosmos/bank/v1beta1/spendable_balances/{address}` | Spendable (non-locked) balances for an account. |
| `SpendableBalanceByDenom` | `/cosmos/bank/v1beta1/spendable_balances/{address}/by_denom` | Spendable balance of one denom. |
| `TotalSupply` | `/cosmos/bank/v1beta1/supply` | Total supply of every coin. |
| `SupplyOf` | `/cosmos/bank/v1beta1/supply/by_denom` | Total supply of one denom (e.g. `asteem`). |
| `Params` | `/cosmos/bank/v1beta1/params` | Bank module params (default send-enabled, etc.). |
| `DenomsMetadata` | `/cosmos/bank/v1beta1/denoms_metadata` | Client metadata for all registered denoms. |
| `DenomMetadata` | `/cosmos/bank/v1beta1/denoms_metadata/{denom=**}` | Metadata for one denom. |
| `DenomMetadataByQueryString` | `/cosmos/bank/v1beta1/denoms_metadata_by_query_string` | Same as above, denom passed as a query string param (works around slashes in the path). |
| `DenomOwners` | `/cosmos/bank/v1beta1/denom_owners/{denom=**}` | All addresses holding a given denom, with balances. |
| `DenomOwnersByQuery` | `/cosmos/bank/v1beta1/denom_owners_by_query` | Same, denom as query string. |
| `SendEnabled` | `/cosmos/bank/v1beta1/send_enabled` | Per-denom `SendEnabled` overrides. |

**Msg** (4 methods, no REST)

| RPC | Description |
|---|---|
| `Send` | Transfer coins between two accounts. |
| `MultiSend` | Multi-in/multi-out transfer. |
| `UpdateParams` | Gov-authority-only params update. |
| `SetSendEnabled` | Gov-authority-only per-denom send-enabled override. |

### staking — `cosmos.staking.v1beta1`

The validator-identity ante gate (`app/ante_steembridge.go`) sits in front of `MsgCreateValidator`/
`MsgEditValidator` here — see the steembridge section above. **Query** (14 methods, all REST GET)

| RPC | REST path | Description |
|---|---|---|
| `Validators` | `/cosmos/staking/v1beta1/validators` | All validators matching a status filter. |
| `Validator` | `/cosmos/staking/v1beta1/validators/{validator_addr}` | One validator's info. |
| `ValidatorDelegations` | `/cosmos/staking/v1beta1/validators/{validator_addr}/delegations` | Delegations into a validator. |
| `ValidatorUnbondingDelegations` | `/cosmos/staking/v1beta1/validators/{validator_addr}/unbonding_delegations` | Unbonding delegations from a validator. |
| `Delegation` | `/cosmos/staking/v1beta1/validators/{validator_addr}/delegations/{delegator_addr}` | One delegator→validator delegation. |
| `UnbondingDelegation` | `/cosmos/staking/v1beta1/validators/{validator_addr}/delegations/{delegator_addr}/unbonding_delegation` | One delegator→validator unbonding entry. |
| `DelegatorDelegations` | `/cosmos/staking/v1beta1/delegations/{delegator_addr}` | All delegations by a delegator. |
| `DelegatorUnbondingDelegations` | `/cosmos/staking/v1beta1/delegators/{delegator_addr}/unbonding_delegations` | All unbonding entries for a delegator. |
| `Redelegations` | `/cosmos/staking/v1beta1/delegators/{delegator_addr}/redelegations` | Redelegations for a delegator (optionally scoped by src/dst validator). |
| `DelegatorValidators` | `/cosmos/staking/v1beta1/delegators/{delegator_addr}/validators` | Validators a delegator has delegated to. |
| `DelegatorValidator` | `/cosmos/staking/v1beta1/delegators/{delegator_addr}/validators/{validator_addr}` | One delegator→validator pair's validator info. |
| `HistoricalInfo` | `/cosmos/staking/v1beta1/historical_info/{height}` | Historical validator set info at a height. |
| `Pool` | `/cosmos/staking/v1beta1/pool` | Bonded/not-bonded token pool totals. |
| `Params` | `/cosmos/staking/v1beta1/params` | Staking params (unbonding time, max validators, bond denom `asteem`, etc.). |

**Msg** (7 methods, no REST)

| RPC | Description |
|---|---|
| `CreateValidator` | Create a new validator — gated by the steembridge validator-identity ante check (moniker must be an ACTIVE name link, `Description.details` must embed `owner/active/posting=STM...` keys). |
| `EditValidator` | Edit an existing validator — same identity gate. |
| `Delegate` | Delegate `asteem` to a validator. |
| `BeginRedelegate` | Move a delegation from one validator to another. |
| `Undelegate` | Begin unbonding a delegation. |
| `CancelUnbondingDelegation` | Cancel an in-flight unbonding, redelegating back. |
| `UpdateParams` | Gov-authority-only params update. |

### distribution — `cosmos.distribution.v1beta1`

The 0.25% bridge fee lands in `fee_collector` before distribution's `BeginBlocker` sweeps it (see
CLAUDE.md's note on `BeginBlockers` ordering). **Query** (13 methods, all REST GET)

| RPC | REST path | Description |
|---|---|---|
| `Params` | `/cosmos/distribution/v1beta1/params` | Distribution params (community tax — 0 on this chain — withdraw-addr-enabled, etc.). |
| `ValidatorDistributionInfo` | `/cosmos/distribution/v1beta1/validators/{validator_address}` | A validator's self-bond rewards + commission. |
| `ValidatorOutstandingRewards` | `/cosmos/distribution/v1beta1/validators/{validator_address}/outstanding_rewards` | Outstanding (undistributed) rewards for a validator. |
| `ValidatorCommission` | `/cosmos/distribution/v1beta1/validators/{validator_address}/commission` | Accumulated commission for a validator. |
| `ValidatorSlashes` | `/cosmos/distribution/v1beta1/validators/{validator_address}/slashes` | Slash events for a validator (optionally height-bounded). |
| `DelegationRewards` | `/cosmos/distribution/v1beta1/delegators/{delegator_address}/rewards/{validator_address}` | Rewards accrued by one delegation. |
| `DelegationTotalRewards` | `/cosmos/distribution/v1beta1/delegators/{delegator_address}/rewards` | Total rewards across all of a delegator's validators. |
| `DelegatorValidators` | `/cosmos/distribution/v1beta1/delegators/{delegator_address}/validators` | Validators a delegator has delegated to. |
| `DelegatorWithdrawAddress` | `/cosmos/distribution/v1beta1/delegators/{delegator_address}/withdraw_address` | The address rewards get withdrawn to. |
| `CommunityPool` | `/cosmos/distribution/v1beta1/community_pool` | Community pool coin balance. |
| `ValidatorHistoricalRewards` | `/cosmos/distribution/v1beta1/validators/{validator_address}/historical_rewards/{period}` | Historical rewards at a reward period. |
| `ValidatorCurrentRewards` | `/cosmos/distribution/v1beta1/validators/{validator_address}/current_rewards` | Current (in-progress) rewards. |
| `DelegatorStartingInfo` | `/cosmos/distribution/v1beta1/delegators/{delegator_address}/starting_info/{validator_address}` | The reward-period starting info for a delegation. |

**Msg** (7 methods, no REST)

| RPC | Description |
|---|---|
| `SetWithdrawAddress` | Change where a delegator's rewards get withdrawn to. |
| `WithdrawDelegatorReward` | Withdraw rewards from one validator. |
| `WithdrawValidatorCommission` | Validator withdraws its accumulated commission. |
| `FundCommunityPool` | Directly fund the community pool. |
| `UpdateParams` | Gov-authority-only params update. |
| `CommunityPoolSpend` | Gov-only: spend community pool funds to a recipient. |
| `DepositValidatorRewardsPool` | Add extra rewards for a specific validator's delegators. |

### gov — `cosmos.gov.v1` (the v1beta1 legacy package also exists in the module cache but is not separately enumerated here — same RPC shapes, deprecated in favor of v1)

**Query** (9 methods, all REST GET)

| RPC | REST path | Description |
|---|---|---|
| `Constitution` | `/cosmos/gov/v1/constitution` | The chain's constitution text. |
| `Proposal` | `/cosmos/gov/v1/proposals/{proposal_id}` | One proposal's details. |
| `Proposals` | `/cosmos/gov/v1/proposals` | Proposals filtered by status/voter/depositor. |
| `Vote` | `/cosmos/gov/v1/proposals/{proposal_id}/votes/{voter}` | One voter's vote on a proposal. |
| `Votes` | `/cosmos/gov/v1/proposals/{proposal_id}/votes` | All votes on a proposal. |
| `Params` | `/cosmos/gov/v1/params/{params_type}` | Voting/tallying/deposit params (or all, via the unified `params` field). |
| `Deposit` | `/cosmos/gov/v1/proposals/{proposal_id}/deposits/{depositor}` | One depositor's deposit on a proposal. |
| `Deposits` | `/cosmos/gov/v1/proposals/{proposal_id}/deposits` | All deposits on a proposal. |
| `TallyResult` | `/cosmos/gov/v1/proposals/{proposal_id}/tally` | Current tally of a proposal's votes. |

**Msg** (7 methods, no REST)

| RPC | Description |
|---|---|
| `SubmitProposal` | Submit a new proposal (any set of messages, incl. this chain's various `MsgUpdateParams`). |
| `ExecLegacyContent` | Wraps a legacy v1beta1 content proposal inside a v1 `MsgSubmitProposal`. |
| `Vote` | Cast a single-option vote. |
| `VoteWeighted` | Cast a split-weight vote across options. |
| `Deposit` | Add to a proposal's deposit. |
| `UpdateParams` | Gov-authority-only (self-referential) params update. |
| `CancelProposal` | Proposer cancels their own proposal before it passes. |

### slashing — `cosmos.slashing.v1beta1`

Standard SDK slashing (double-sign/downtime) — separate from the **parent `x/oracle`** unified
miss-counter described above; both can jail a validator but track independent windows.
**Query** (3 methods, all REST GET)

| RPC | REST path | Description |
|---|---|---|
| `Params` | `/cosmos/slashing/v1beta1/params` | Slashing params (signed-blocks window, min-signed-per-window, slash fractions, downtime jail duration). |
| `SigningInfo` | `/cosmos/slashing/v1beta1/signing_infos/{cons_address}` | One validator's signing/jail info by consensus address. |
| `SigningInfos` | `/cosmos/slashing/v1beta1/signing_infos` | Signing info for all validators. |

**Msg** (2 methods, no REST)

| RPC | Description |
|---|---|
| `Unjail` | Unjail a previously jailed validator. |
| `UpdateParams` | Gov-authority-only params update. |

### mint — `cosmos.mint.v1beta1`

Wired but inert on this chain — CLAUDE.md notes "no inflation (mint params are zero)".
**Query** (3 methods, all REST GET)

| RPC | REST path | Description |
|---|---|---|
| `Params` | `/cosmos/mint/v1beta1/params` | Mint params (zero on this chain). |
| `Inflation` | `/cosmos/mint/v1beta1/inflation` | Current inflation rate (expect `0.000000000000000000`). |
| `AnnualProvisions` | `/cosmos/mint/v1beta1/annual_provisions` | Current annual provisions (expect `0`). |

**Msg** (1 method, no REST)

| RPC | Description |
|---|---|
| `UpdateParams` | Gov-authority-only params update. |

### evidence — `cosmos.evidence.v1beta1`

**Query** (2 methods, both REST GET)

| RPC | REST path | Description |
|---|---|---|
| `Evidence` | `/cosmos/evidence/v1beta1/evidence/{hash}` | One piece of submitted evidence by hash. |
| `AllEvidence` | `/cosmos/evidence/v1beta1/evidence` | All submitted evidence, paginated. |

**Msg** (1 method, no REST)

| RPC | Description |
|---|---|
| `SubmitEvidence` | Submit arbitrary misbehavior evidence (e.g. equivocation). |

### upgrade — `cosmos.upgrade.v1beta1`

The dormant `x/upgrade` handler is `UpgradeName = "v0.0.3"` (`app/upgrades.go`); this chain runs
under cosmovisor so a future gov software-upgrade auto-swaps the staged binary at the target
height. **Query** (5 methods, all REST GET)

| RPC | REST path | Description |
|---|---|---|
| `CurrentPlan` | `/cosmos/upgrade/v1beta1/current_plan` | The currently scheduled upgrade plan, if any. |
| `AppliedPlan` | `/cosmos/upgrade/v1beta1/applied_plan/{name}` | The height a named upgrade was applied at. |
| `UpgradedConsensusState` | `/cosmos/upgrade/v1beta1/upgraded_consensus_state/{last_height}` | **Deprecated** — superseded by IBC's own client-state query. |
| `ModuleVersions` | `/cosmos/upgrade/v1beta1/module_versions` | Per-module consensus version list (useful for verifying an upgrade landed). |
| `Authority` | `/cosmos/upgrade/v1beta1/authority` | The address authorized to submit upgrade proposals (the gov module account). |

**Msg** (2 methods, no REST)

| RPC | Description |
|---|---|
| `SoftwareUpgrade` | Gov-only: schedule a software upgrade plan. |
| `CancelUpgrade` | Gov-only: cancel a previously scheduled upgrade. |

### authz — `cosmos.authz.v1beta1`

**Query** (3 methods, all REST GET)

| RPC | REST path | Description |
|---|---|---|
| `Grants` | `/cosmos/authz/v1beta1/grants` | Grants from a granter to a grantee (optionally filtered by msg type). |
| `GranterGrants` | `/cosmos/authz/v1beta1/grants/granter/{granter}` | All grants issued by a granter. |
| `GranteeGrants` | `/cosmos/authz/v1beta1/grants/grantee/{grantee}` | All grants received by a grantee. |

**Msg** (3 methods, no REST)

| RPC | Description |
|---|---|
| `Grant` | Grant an authorization from granter to grantee. |
| `Exec` | Grantee executes messages using a granted authorization. |
| `Revoke` | Granter revokes a previously issued grant. |

### feegrant — `cosmos.feegrant.v1beta1`

**Query** (3 methods, all REST GET)

| RPC | REST path | Description |
|---|---|---|
| `Allowance` | `/cosmos/feegrant/v1beta1/allowance/{granter}/{grantee}` | One fee allowance. |
| `Allowances` | `/cosmos/feegrant/v1beta1/allowances/{grantee}` | All allowances granted to a grantee. |
| `AllowancesByGranter` | `/cosmos/feegrant/v1beta1/issued/{granter}` | All allowances issued by a granter. |

**Msg** (3 methods, no REST)

| RPC | Description |
|---|---|
| `GrantAllowance` | Grant a fee allowance (basic/periodic/allowed-msg) to a grantee. |
| `RevokeAllowance` | Revoke a fee allowance. |
| `PruneAllowances` | Prune up to 75 expired allowances at a time. |

### params — `cosmos.params.v1beta1`

Legacy generic param-subspace query, kept for read access to any module still using the
legacy `x/params` subspace pattern (most modules on this chain use the newer per-module
`Params`/`MsgUpdateParams` shape instead). **Query** (2 methods, both REST GET) — **no `Msg`
service**; legacy param changes go through `x/gov` v1beta1 `ParameterChangeProposal` content, not
a direct message.

| RPC | REST path | Description |
|---|---|---|
| `Params` | `/cosmos/params/v1beta1/params` | One parameter, by subspace + key. |
| `Subspaces` | `/cosmos/params/v1beta1/subspaces` | All registered subspaces and their keys. |

### consensus — `cosmos.consensus.v1`

**Query** (1 method, REST GET)

| RPC | REST path | Description |
|---|---|---|
| `Params` | `/cosmos/consensus/v1/params` | CometBFT consensus params (block size/gas, evidence, validator pubkey types, ABCI, authority). |

**Msg** (1 method, no REST)

| RPC | Description |
|---|---|
| `UpdateParams` | Gov-authority-only update of block/evidence/validator/ABCI/auth consensus params. |

---

## cosmos/evm v0.7.1 modules

Proto sources read from the vendored `github.com/cosmos/evm@v0.7.1` module cache
(`proto/cosmos/evm/**`). Wired in `app/evm.go`; EVM chain id `8163` (`0x1fe3`), evm denom `asteem`
(18 dec). All three below are also directly reachable through the EVM JSON-RPC surface (`eth_*`
etc.) documented in the companion EVM reference — the entries here are the Cosmos-side gRPC/REST
duplicate of that same state.

### vm — `cosmos.evm.vm.v1`

**Query** (15 methods, all REST GET, several doubling as the backing implementation for
`eth_call`/`eth_estimateGas`/`debug_trace*` JSON-RPC methods)

| RPC | REST path | Description |
|---|---|---|
| `Account` | `/cosmos/evm/vm/v1/account/{address}` | Balance/code-hash/nonce for a 0x address. |
| `CosmosAccount` | `/cosmos/evm/vm/v1/cosmos_account/{address}` | The bech32 Cosmos address + sequence/account-number for a 0x address. |
| `ValidatorAccount` | `/cosmos/evm/vm/v1/validator_account/{cons_address}` | The Cosmos address behind a validator's consensus address. |
| `Balance` | `/cosmos/evm/vm/v1/balances/{address}` | EVM-denom (`asteem`) balance for a 0x address. |
| `Storage` | `/cosmos/evm/vm/v1/storage/{address}/{key}` | Raw contract storage slot value. |
| `Code` | `/cosmos/evm/vm/v1/codes/{address}` | Deployed contract bytecode. |
| `Params` | `/cosmos/evm/vm/v1/params` | EVM module params (active static precompiles list — includes `0x...0900`/`0x...0902` — extra EIPs, etc.). |
| `EthCall` | `/cosmos/evm/vm/v1/eth_call` | Simulate a call (backs `eth_call`). |
| `EstimateGas` | `/cosmos/evm/vm/v1/estimate_gas` | Gas estimate for a call (backs `eth_estimateGas`). |
| `TraceTx` | `/cosmos/evm/vm/v1/trace_tx` | Debug trace of a single transaction (backs `debug_traceTransaction`). |
| `TraceBlock` | `/cosmos/evm/vm/v1/trace_block` | Debug trace of every tx in a block (backs `debug_traceBlockBy{Number,Hash}`). |
| `TraceCall` | `/cosmos/evm/vm/v1/trace_call` | Debug trace of a simulated call (backs `debug_traceCall`). |
| `BaseFee` | `/cosmos/evm/vm/v1/base_fee` | EIP-1559 base fee of the parent block (also checks London-fork activation). |
| `Config` | `/cosmos/evm/vm/v1/config` | The active `ChainConfig` (fork activation heights/flags). |
| `GlobalMinGasPrice` | `/cosmos/evm/vm/v1/min_gas_price` | The feemarket's min gas price, rescaled to 18 decimals. |

**Msg** (3 methods; `EthereumTx` is REST-POST-annotated, the other two are not)

| RPC | REST path | Description |
|---|---|---|
| `EthereumTx` | `POST /cosmos/evm/vm/v1/ethereum_tx` | Submit a raw signed Ethereum transaction wrapped as a Cosmos `Msg` (the mechanism behind `eth_sendRawTransaction`). |
| `UpdateParams` | — | Gov-authority-only params update. |
| `RegisterPreinstalls` | — | Gov-authority-only: register preinstalled contracts directly into EVM state. |

### erc20 — `cosmos.evm.erc20.v1`

Backs both static ERC20 registrations and dynamic precompiles for IBC-received vouchers (see
`app/ibc.go`'s `erc20.NewIBCMiddleware` wrapping of the transfer stack). **Query** (3 methods,
all REST GET)

| RPC | REST path | Description |
|---|---|---|
| `TokenPairs` | `/cosmos/evm/erc20/v1/token_pairs` | All registered Cosmos-coin ↔ ERC20 token pair mappings. |
| `TokenPair` | `/cosmos/evm/erc20/v1/token_pairs/{token=**}` | One mapping, looked up by either the ERC20 hex address or the Cosmos denom. |
| `Params` | `/cosmos/evm/erc20/v1/params` | erc20 module params (enable flags, dynamic-precompile behavior). |

**Msg** (5 methods — note `ConvertERC20`/`ConvertCoin` carry `google.api.http.get` annotations in
the proto, an unusual pattern for a state-mutating `Msg`; treat this as informational, not as a
usable no-signing endpoint)

| RPC | REST path | Description |
|---|---|---|
| `ConvertERC20` | `GET /cosmos/evm/erc20/v1/tx/convert_erc20` (proto-declared, still requires a signed tx in practice) | Convert an ERC20 token balance into its native Cosmos-coin representation. |
| `ConvertCoin` | `GET /cosmos/evm/erc20/v1/tx/convert_coin` (same caveat) | Convert a native Cosmos coin into its ERC20 representation. |
| `UpdateParams` | — | Gov-authority-only params update. |
| `RegisterERC20` | — | Gov-authority-only: register a token pair for an existing ERC20 contract. |
| `ToggleConversion` | — | Gov-authority-only: enable/disable conversion for a token pair. |

### feemarket — `cosmos.evm.feemarket.v1`

EIP-1559 fee market; genesis `base_fee` matches `srvCfg.MinGasPrices = "1000000000asteem"` set in
`cmd/steemvmd/cmd/config.go`. **Query** (3 methods, all REST GET)

| RPC | REST path | Description |
|---|---|---|
| `Params` | `/cosmos/evm/feemarket/v1/params` | Feemarket params (base fee change denominator, elasticity multiplier, min gas price, etc.). |
| `BaseFee` | `/cosmos/evm/feemarket/v1/base_fee` | Current EIP-1559 base fee. |
| `BlockGas` | `/cosmos/evm/feemarket/v1/block_gas` | Gas used at a given block height. |

**Msg** (1 method, no REST)

| RPC | Description |
|---|---|
| `UpdateParams` | Gov-authority-only params update. |

---

## ibc-go v11.0.0 modules

Wired in `app/ibc.go`: IBC core + ICS-20 transfer (wrapped by the erc20 middleware, both v1 and
v2/"eureka" routers) + interchain-accounts (both controller and host). Light-client modules
(07-tendermint, 06-solomachine) are registered but expose no gRPC `Query`/`Msg` service of their
own — they're reached through the core client queries below.

### ibc-transfer — `ibc.applications.transfer.v1`

**Query** (6 methods, all REST GET)

| RPC | REST path | Description |
|---|---|---|
| `Params` | `/ibc/apps/transfer/v1/params` | Transfer module params (send/receive enabled). |
| `Denoms` | `/ibc/apps/transfer/v1/denoms` | All known IBC denom traces. |
| `Denom` | `/ibc/apps/transfer/v1/denoms/{hash=**}` | One denom trace by hash. |
| `DenomHash` | `/ibc/apps/transfer/v1/denom_hashes/{trace=**}` | Hash for a given `[port]/[channel]/denom` trace path. |
| `EscrowAddress` | `/ibc/apps/transfer/v1/channels/{channel_id}/ports/{port_id}/escrow_address` | The escrow account address for a port/channel. |
| `TotalEscrowForDenom` | `/ibc/apps/transfer/v1/total_escrow/{denom=**}` | Total amount of a denom currently held in escrow. |

**Msg** (2 methods, no REST)

| RPC | Description |
|---|---|
| `Transfer` | Send an ICS-20 fungible token transfer packet. |
| `UpdateParams` | Gov/authority-only params update. |

### interchain-accounts (controller) — `ibc.applications.interchain_accounts.controller.v1`

**Query** (2 methods, both REST GET)

| RPC | REST path | Description |
|---|---|---|
| `InterchainAccount` | `/ibc/apps/interchain_accounts/controller/v1/owners/{owner}/connections/{connection_id}` | The registered ICA address for an owner on a given connection. |
| `Params` | `/ibc/apps/interchain_accounts/controller/v1/params` | ICA-controller submodule params. |

**Msg** (3 methods, no REST)

| RPC | Description |
|---|---|
| `RegisterInterchainAccount` | Register a new interchain account over a connection. |
| `SendTx` | Send a packet of messages to be executed by the interchain account on the host chain. |
| `UpdateParams` | Gov/authority-only params update. |

### interchain-accounts (host) — `ibc.applications.interchain_accounts.host.v1`

**Query** (1 method, REST GET)

| RPC | REST path | Description |
|---|---|---|
| `Params` | `/ibc/apps/interchain_accounts/host/v1/params` | ICA-host submodule params (allowed message types). |

**Msg** (2 methods, no REST)

| RPC | Description |
|---|---|
| `UpdateParams` | Gov/authority-only params update. |
| `ModuleQuerySafe` | Executes a batch of module-safe queries on behalf of a remote controller chain (ICS-27 query support). |

### ibc core — client / connection / channel

These are the low-level handshake and packet-relay primitives relayers use; end users rarely call
them directly, but they're fully queryable.

**client** — `ibc.core.client.v1.Query` (11 methods, all REST GET except `VerifyMembership` which is POST)

| RPC | REST path | Description |
|---|---|---|
| `ClientState` | `/ibc/core/client/v1/client_states/{client_id}` | One light client's state. |
| `ClientStates` | `/ibc/core/client/v1/client_states` | All light clients on this chain. |
| `ConsensusState` | `/ibc/core/client/v1/consensus_states/{client_id}/revision/{revision_number}/height/{revision_height}` | A client's consensus state at a given height. |
| `ConsensusStates` | `/ibc/core/client/v1/consensus_states/{client_id}` | All consensus states for a client. |
| `ConsensusStateHeights` | `/ibc/core/client/v1/consensus_states/{client_id}/heights` | Just the heights of a client's stored consensus states. |
| `ClientStatus` | `/ibc/core/client/v1/client_status/{client_id}` | Whether a client is Active/Frozen/Expired. |
| `ClientParams` | `/ibc/core/client/v1/params` | IBC client submodule params (allowed client types). |
| `ClientCreator` | `/ibc/core/client/v1/client_creator/{client_id}` | The address that created a given client. |
| `UpgradedClientState` | `/ibc/core/client/v1/upgraded_client_states` | The staged post-upgrade client state, if any. |
| `UpgradedConsensusState` | `/ibc/core/client/v1/upgraded_consensus_states` | The staged post-upgrade consensus state, if any. |
| `VerifyMembership` | `POST /ibc/core/client/v1/verify_membership` | Verify a Merkle proof against a client's root (module-query-safe). |

**client Msg** — `ibc.core.client.v1.Msg` (7 methods, no REST)

| RPC | Description |
|---|---|
| `CreateClient` | Create a new IBC light client. |
| `UpdateClient` | Submit a new header to update a client. |
| `UpgradeClient` | Upgrade a client across a counterparty chain upgrade. |
| `RecoverClient` | Gov-only: recover an expired/frozen client using a substitute client. |
| `IBCSoftwareUpgrade` | Gov-only: schedule an IBC-coordinated software upgrade. |
| `UpdateClientParams` | Gov/authority-only: update allowed client types. |
| `DeleteClientCreator` | Remove the stored creator record for a client. |

**connection** — `ibc.core.connection.v1.Query` (6 methods, all REST GET)

| RPC | REST path | Description |
|---|---|---|
| `Connection` | `/ibc/core/connection/v1/connections/{connection_id}` | One connection end. |
| `Connections` | `/ibc/core/connection/v1/connections` | All connections on this chain. |
| `ClientConnections` | `/ibc/core/connection/v1/client_connections/{client_id}` | Connection paths associated with a client. |
| `ConnectionClientState` | `/ibc/core/connection/v1/connections/{connection_id}/client_state` | The client state backing a connection. |
| `ConnectionConsensusState` | `/ibc/core/connection/v1/connections/{connection_id}/consensus_state/revision/{revision_number}/height/{revision_height}` | The consensus state backing a connection at a height. |
| `ConnectionParams` | `/ibc/core/connection/v1/params` | IBC connection submodule params. |

**connection Msg** — `ibc.core.connection.v1.Msg` (5 methods, no REST)

| RPC | Description |
|---|---|
| `ConnectionOpenInit` | Handshake step 1 (initiator). |
| `ConnectionOpenTry` | Handshake step 2 (counterparty). |
| `ConnectionOpenAck` | Handshake step 3. |
| `ConnectionOpenConfirm` | Handshake step 4 (final). |
| `UpdateConnectionParams` | Gov/authority-only params update. |

**channel** — `ibc.core.channel.v1.Query` (14 methods, all REST GET)

| RPC | REST path | Description |
|---|---|---|
| `Channel` | `/ibc/core/channel/v1/channels/{channel_id}/ports/{port_id}` | One channel end. |
| `Channels` | `/ibc/core/channel/v1/channels` | All channels on this chain. |
| `ConnectionChannels` | `/ibc/core/channel/v1/connections/{connection}/channels` | Channels riding on a given connection. |
| `ChannelClientState` | `/ibc/core/channel/v1/channels/{channel_id}/ports/{port_id}/client_state` | The client state backing a channel. |
| `ChannelConsensusState` | `/ibc/core/channel/v1/channels/{channel_id}/ports/{port_id}/consensus_state/revision/{revision_number}/height/{revision_height}` | The consensus state backing a channel at a height. |
| `PacketCommitment` | `/ibc/core/channel/v1/channels/{channel_id}/ports/{port_id}/packet_commitments/{sequence}` | One stored packet commitment hash. |
| `PacketCommitments` | `/ibc/core/channel/v1/channels/{channel_id}/ports/{port_id}/packet_commitments` | All packet commitments on a channel. |
| `PacketReceipt` | `/ibc/core/channel/v1/channels/{channel_id}/ports/{port_id}/packet_receipts/{sequence}` | Whether a given packet sequence was received. |
| `PacketAcknowledgement` | `/ibc/core/channel/v1/channels/{channel_id}/ports/{port_id}/packet_acks/{sequence}` | One stored packet acknowledgement hash. |
| `PacketAcknowledgements` | `/ibc/core/channel/v1/channels/{channel_id}/ports/{port_id}/packet_acknowledgements` | All packet acks on a channel. |
| `UnreceivedPackets` | `/ibc/core/channel/v1/channels/{channel_id}/ports/{port_id}/packet_commitments/{packet_commitment_sequences}/unreceived_packets` | Which of a given sequence set is still unreceived (relayer helper). |
| `UnreceivedAcks` | `/ibc/core/channel/v1/channels/{channel_id}/ports/{port_id}/packet_commitments/{packet_ack_sequences}/unreceived_acks` | Which of a given sequence set is still unacknowledged (relayer helper). |
| `NextSequenceReceive` | `/ibc/core/channel/v1/channels/{channel_id}/ports/{port_id}/next_sequence` | The next expected receive sequence. |
| `NextSequenceSend` | `/ibc/core/channel/v1/channels/{channel_id}/ports/{port_id}/next_sequence_send` | The next send sequence. |

**channel Msg** — `ibc.core.channel.v1.Msg` (10 methods, no REST)

| RPC | Description |
|---|---|
| `ChannelOpenInit` | Handshake step 1 (initiator). |
| `ChannelOpenTry` | Handshake step 2. |
| `ChannelOpenAck` | Handshake step 3. |
| `ChannelOpenConfirm` | Handshake step 4 (final). |
| `ChannelCloseInit` | Initiate closing a channel. |
| `ChannelCloseConfirm` | Confirm a channel close. |
| `RecvPacket` | Relay a received packet into the app (transfer/ICA). |
| `Timeout` | Relay a packet timeout. |
| `TimeoutOnClose` | Relay a packet timeout triggered by a channel close. |
| `Acknowledgement` | Relay a packet acknowledgement back to the sender. |

There is also a newer `ibc.core.channel.v2` (IBC-eureka) `Query`/`Msg` pair in the vendored proto
tree, and `app/ibc.go` does call `app.IBCKeeper.SetRouterV2(ibcv2Router)` with a transfer route —
but only the v1 packet-relay path is exercised by anything else in this app (no v2-only app
module is registered), so v2 is not separately enumerated here; treat it as present but unused
by this chain's own modules today.

---

## Wired modules with no meaningfully queryable surface for this reference

A few more modules from `app/app_config.go` are present in the module set but out of scope for
the module list this document was scoped to cover (they were not in the requested module list,
and/or carry only one side of a Query/Msg pair):

- **`x/epochs`** (`cosmos.epochs.v1beta1.Query`, GET `/cosmos/epochs/v1beta1/epochs` and
  `/cosmos/epochs/v1beta1/current_epoch`) — `EpochInfos`/`CurrentEpoch`. No `Msg` service; epoch
  hooks are internal.
- **`x/auth/vesting`** (`cosmos.vesting.v1beta1.Msg`) — `CreateVestingAccount`,
  `CreatePermanentLockedAccount`, `CreatePeriodicVestingAccount`. No `Query` service; vesting
  account state reads through `auth`'s `Account`/`AccountInfo` queries instead.
- **`genutil`**, **`tx`** (the `TxConfig`/msg-service-router wiring, not a queryable module) —
  no proto service of their own.

## Summary

This document enumerates **~256** gRPC RPC methods with verified proto sources: the 4 custom
modules (37 methods: steembridge 26, oracledata 8, parent oracle 1, steemvm 2), 13 standard
Cosmos SDK modules (120 methods), 3 cosmos/evm modules (30 methods), and 6 ibc-go
module/submodules (69 methods). Every REST path quoted above was copied verbatim from a
`google.api.http` annotation in the actual `.proto` source, not inferred from a naming
convention — where no REST row/column appears for an RPC, that RPC genuinely has no
`google.api.http` option in its proto.
