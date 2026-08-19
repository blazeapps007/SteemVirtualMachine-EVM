# SteemVM EVM JSON-RPC Reference

SteemVM exposes a go-ethereum-compatible JSON-RPC interface over the Cosmos SDK
v0.54 + cosmos/evm v0.7.1 EVM layer. This document is a from-source reference for:

1. every JSON-RPC namespace/method actually wired up by `cosmos/evm@v0.7.1` and
   which of them are live on this chain's deployment,
2. this chain's two custom static precompiles (`steembridge`, `oracledata`) plus
   the dynamically-registered SBD ERC20, with full ABIs, and
3. how to connect a wallet or CLI tool to it.

All facts below were verified against this repository's source
(`app/evm.go`, `app/register_sbd.go`, `cmd/steemvmd/cmd/config.go`,
`docker-compose.yml`, `Instructions/genesis.json`, `precompiles/**`) and the
vendored `github.com/cosmos/evm@v0.7.1` module (`rpc/apis.go`,
`rpc/namespaces/ethereum/**`, `server/config/config.go`). Anything that could
not be confirmed from source is called out explicitly rather than guessed.

## Chain facts

| | |
|---|---|
| Cosmos chain-id | `steemvm` |
| EVM chain-id | `8163` (`0x1fe3`) |
| Native/gas denom | `asteem` (18 decimals) — the EVM's native value/gas unit; 1 `asteem` = 1 wei-equivalent at the EVM layer |
| Bridged stablecoin | `asbd` (18 decimals on the EVM side; native bank denom `asbd`, display `sbd`) |
| Currency symbol (wallets) | `STEEM` |
| Bech32 prefix | `steem` |
| Binary | `steemvmd` |
| JSON-RPC HTTP | `http://<host>:8545` |
| JSON-RPC WebSocket | `ws://<host>:8546` |

### MetaMask / wallet network config

| Field | Value |
|---|---|
| Network name | SteemVM (or any label you like) |
| New RPC URL | `http://<host>:8545` |
| Chain ID | `8163` |
| Currency symbol | `STEEM` |
| Currency decimals | `18` |
| Block explorer URL | *(none known to exist for this chain yet — leave blank, or fill in once one is deployed)* |

### `cast` (Foundry) quickstart

```bash
# Chain ID (should print 8163)
cast chain-id --rpc-url http://localhost:8545

# Latest block number
cast block-number --rpc-url http://localhost:8545

# Native asteem balance of an address, in wei-equivalent units
cast balance 0xYourAddress --rpc-url http://localhost:8545

# Same, formatted as ether-equivalent (18 dec) units
cast balance 0xYourAddress --rpc-url http://localhost:8545 --ether
```

## Operational note: which namespaces are actually live

`cosmos/evm@v0.7.1`'s library default (`server/config.GetDefaultAPINamespaces()`,
`server/config/config.go`) is:

```go
func GetDefaultAPINamespaces() []string {
    return []string{"eth", "net", "web3"}
}
```

This repo's own `cmd/steemvmd/cmd/config.go` (`initAppConfig`) does not override
that default — it just calls `cosmosevmserverconfig.DefaultJSONRPCConfig()`
as-is — and the checked-in `Instructions/app.toml.example` correspondingly ships
`api = "eth,net,web3"`.

**However**, the actual node-launch command in `docker-compose.yml` (the `steemvm`
service's `command:` block) passes an explicit CLI flag that overrides the
config-file value:

```
exec /root/go/bin/cosmovisor run start \
  --home /root/.steemvm \
  --json-rpc.enable \
  --json-rpc.address "0.0.0.0:8545" \
  --json-rpc.api "eth,net,web3,txpool,debug" \
  --json-rpc.enable-indexer
```

So **as actually deployed via `docker compose up`, five namespaces are enabled:
`eth`, `net`, `web3`, `txpool`, and `debug`** — not just the library's three-namespace
default. This document therefore covers all five in full. If you (or a validator)
run `steemvmd start` directly without this flag, or edit `app.toml`'s `[json-rpc]
api` value, you get back to the library default of `eth,net,web3` only, and
`txpool`/`debug` calls will fail with "the method ... does not exist".

Two more namespaces exist in the vendored code (`personal`, `miner` — see
`rpc/apis.go`'s `apiCreators` map and `server/config.GetAPINamespaces()`, which
lists all seven available namespaces: `web3, eth, personal, net, txpool, debug,
miner`) but are **not** passed by `docker-compose.yml` and are not enabled on
this chain. They are omitted from the method tables below; enable them the same
way (`--json-rpc.api eth,net,web3,txpool,debug,personal,miner`) if needed.

Also note: the compose command only overrides `--json-rpc.address` (HTTP,
`0.0.0.0:8545`, matching the `8545:8545` port mapping). It does **not** pass a
`--json-rpc.ws-address` flag, so the WebSocket bind address falls back to
whatever `app.toml`'s `[json-rpc] ws-address` says — `127.0.0.1:8546` in the
checked-in `Instructions/app.toml.example` template. A loopback bind inside the
container is not reachable through the `8546:8546` Docker port mapping from
outside the container. If `ws://<host>:8546` doesn't connect, check that the
running node's `app.toml` sets `ws-address = "0.0.0.0:8546"` (edit
`Instructions/app.toml` — see the compose file's seeding comment — and restart).

## Namespace: `eth` (enabled by default)

Standard go-ethereum JSON-RPC namespace. Implemented across two Go types wired
into the `eth` namespace by `rpc/apis.go`: `rpc/namespaces/ethereum/eth.PublicAPI`
(most methods) and `rpc/namespaces/ethereum/eth/filters.PublicFilterAPI` (the
`eth_newFilter`/`eth_getLogs` family — go-ethereum registers filters under the
`eth` namespace too, not a separate one).

Method names below are the real JSON-RPC names (go-ethereum's `rpc` package
lower-cases the first letter of the exported Go method and prefixes the
namespace), taken from `rpc/namespaces/ethereum/eth/api.go` and
`rpc/namespaces/ethereum/eth/filters/api.go`.

### Blocks

| Method | Params | Returns | Description |
|---|---|---|---|
| `eth_blockNumber` | — | `hexutil.Uint64` | Latest block height. |
| `eth_getBlockByNumber` | `blockNr`, `fullTx bool` | block object or `null` | Block by number (`"latest"`, `"pending"`, `"earliest"`, `"finalized"`, `"safe"`, or hex height). |
| `eth_getBlockByHash` | `hash`, `fullTx bool` | block object or `null` | Block by hash. |
| `eth_getHeaderByNumber` | `blockNr` | header object | Header only (no tx bodies). Supports the same pseudo-tags as above, incl. `-3`/`-4` for finalized/safe. |
| `eth_getHeaderByHash` | `hash` | header object | Header only, by hash. |
| `eth_getBlockTransactionCountByHash` | `hash` | `hexutil.Uint` | Tx count in a block, by hash. |
| `eth_getBlockTransactionCountByNumber` | `blockNr` | `hexutil.Uint` | Tx count in a block, by number. |
| `eth_getBlockReceipts` | `blockNrOrHash` | `[]receipt` | All receipts in a block. |

### Transactions

| Method | Params | Returns | Description |
|---|---|---|---|
| `eth_getTransactionByHash` | `hash` | tx object or `null` | Transaction by hash. |
| `eth_getTransactionByBlockHashAndIndex` | `hash`, `index` | tx object | Transaction by block hash + index. |
| `eth_getTransactionByBlockNumberAndIndex` | `blockNr`, `index` | tx object | Transaction by block number + index. |
| `eth_getTransactionReceipt` | `hash` | receipt object or `null` | Receipt (status, gas used, logs, contract address if any). |
| `eth_getTransactionCount` | `address`, `blockNrOrHash` | `hexutil.Uint64` | Account nonce at a given block. |
| `eth_sendRawTransaction` | `data` (signed RLP) | tx hash | Broadcast a signed transaction. |
| `eth_sendTransaction` | `TransactionArgs` | tx hash | Sign (via the node's unlocked keys, if any) and send. Not useful unless the node has an unlocked local account — normal wallet flows use `eth_sendRawTransaction`. |
| `eth_fillTransaction` | `TransactionArgs` | `{raw, tx}` | Fills in gas/nonce/price defaults on an unsigned tx and returns the RLP for external signing. |
| `eth_resend` | `TransactionArgs`, `gasPrice`, `gasLimit` | tx hash | Re-broadcasts a tx with updated gas price/limit. |
| `eth_getTransactionLogs` | `txHash` | `[]Log` | **cosmos/evm extension, not part of standard go-ethereum JSON-RPC** — logs for one transaction directly, without fetching the full receipt. |

### Account / state

| Method | Params | Returns | Description |
|---|---|---|---|
| `eth_getBalance` | `address`, `blockNrOrHash` | `hexutil.Big` | Native `asteem` balance in wei-equivalent units, at a block. |
| `eth_getStorageAt` | `address`, `key`, `blockNrOrHash` | `hexutil.Bytes` | Raw storage slot value. |
| `eth_getCode` | `address`, `blockNrOrHash` | `hexutil.Bytes` | Deployed contract bytecode (empty for EOAs, and for precompile addresses unless they publish code). |
| `eth_getProof` | `address`, `storageKeys[]`, `blockNrOrHash` | `AccountResult` | Merkle-proof-style account/storage proof. |
| `eth_accounts` | — | `[]address` | Addresses the node holds unlocked keys for (normally empty on a validator/full node). |

### Execution / gas

| Method | Params | Returns | Description |
|---|---|---|---|
| `eth_call` | `TransactionArgs`, `blockNrOrHash`, `overrides?` | `hexutil.Bytes` | Simulate a call, no state change — the standard way to invoke `view`/`pure` functions, including both custom precompiles below. |
| `eth_estimateGas` | `TransactionArgs`, `blockNrOrHash?`, `overrides?` | `hexutil.Uint64` | Estimate gas for a call/tx. |
| `eth_createAccessList` | `TransactionArgs`, `blockNrOrHash`, `overrides?` | `AccessListResult` | EIP-2930 access list + gas estimate for a tx. |
| `eth_gasPrice` | — | `hexutil.Big` | Current suggested gas price (feemarket base fee + tip). |
| `eth_maxPriorityFeePerGas` | — | `hexutil.Big` | Suggested EIP-1559 priority fee. |
| `eth_feeHistory` | `blockCount`, `lastBlock`, `rewardPercentiles[]` | `FeeHistoryResult` | EIP-1559 historical base-fee/gas-used/reward data. |

### Signing

| Method | Params | Returns | Description |
|---|---|---|---|
| `eth_sign` | `address`, `data` | `hexutil.Bytes` | Sign arbitrary data with a node-held key (geth signature format). Requires an unlocked local key. |
| `eth_signTypedData` | `address`, `TypedData` | `hexutil.Bytes` | EIP-712 typed-data signing with a node-held key. |

### Chain / misc

| Method | Params | Returns | Description |
|---|---|---|---|
| `eth_chainId` | — | `hexutil.Big` | EVM chain ID — `0x1fe3` (8163) on this chain. |
| `eth_protocolVersion` | — | `hexutil.Uint` | Reports a static protocol version. |
| `eth_syncing` | — | `false` or a sync-status object | Node sync state. |
| `eth_getUncleByBlockHashAndIndex` | `hash`, `index` | `null` (always) | Stubbed — Cosmos consensus has no uncle blocks, this method always returns `nil`. |
| `eth_getUncleByBlockNumberAndIndex` | `number`, `index` | `null` (always) | Same stub. |
| `eth_getUncleCountByBlockHash` | `hash` | `0x0` (always) | Same stub. |
| `eth_getUncleCountByBlockNumber` | `blockNr` | `0x0` (always) | Same stub. |

### Filters / logs (registered under the `eth` namespace)

| Method | Params | Returns | Description |
|---|---|---|---|
| `eth_newFilter` | `FilterCriteria` | filter id | Create a log filter (address/topics/block range). |
| `eth_newBlockFilter` | — | filter id | Create a new-block-hash filter. |
| `eth_newPendingTransactionFilter` | — | filter id | Create a new-pending-tx-hash filter. |
| `eth_getFilterChanges` | filter id | array of changes | Poll a filter for new results since the last poll. |
| `eth_getFilterLogs` | filter id | `[]Log` | All logs matching a filter's criteria (for log filters). |
| `eth_getLogs` | `FilterCriteria` | `[]Log` | One-shot log query (no filter id retained). |
| `eth_uninstallFilter` | filter id | `bool` | Remove a filter. |

Methods explicitly commented out as unimplemented in the `eth` namespace's own
interface definition (`rpc/namespaces/ethereum/eth/api.go`'s `EthereumAPI`
interface doc comments) and therefore **not available**: `eth_signTransaction`,
`eth_getCompilers`, `eth_compileSolidity`, `eth_compileLLL`, `eth_compileSerpent`,
`eth_getWork`, `eth_submitWork`, `eth_submitHashrate`, `eth_sendPrivateTransaction`,
`eth_cancelPrivateTransaction`.

## Namespace: `net` (enabled by default)

Source: `rpc/namespaces/ethereum/net/api.go`.

| Method | Params | Returns | Description |
|---|---|---|---|
| `net_version` | — | `string` | The network id — matches the EVM chain ID as a decimal string (`"8163"`). |
| `net_listening` | — | `bool` | Whether the node is accepting network connections. |
| `net_peerCount` | — | `int` (hex-encoded) | Number of connected CometBFT peers. |

## Namespace: `web3` (enabled by default)

Source: `rpc/namespaces/ethereum/web3/api.go`.

| Method | Params | Returns | Description |
|---|---|---|---|
| `web3_clientVersion` | — | `string` | Client identifier string. |
| `web3_sha3` | `input` (hex bytes) | `hexutil.Bytes` | Keccak-256 hash of the input. |

## Namespace: `txpool` (enabled on this chain's deployment)

Enabled here via `docker-compose.yml`'s explicit `--json-rpc.api` flag (see the
operational note above) — not part of the cosmos/evm library's own default.
Source: `rpc/namespaces/ethereum/txpool/api.go`. All four standard
go-ethereum-style methods:

| Method | Params | Returns | Description |
|---|---|---|---|
| `txpool_content` | — | pending/queued tx maps, by sender/nonce | Full contents of the local mempool. |
| `txpool_contentFrom` | `address` | pending/queued tx maps | Mempool contents for one sender. |
| `txpool_inspect` | — | human-readable summaries | Same as `content` but summarized as strings instead of full tx objects. |
| `txpool_status` | — | `{pending, queued}` counts | Pending/queued tx counts. |

Because this chain runs cosmos/evm's own "Krakatoa" EVM mempool
(`app/evm.go`'s `configureEVMMempool`) rather than a stock go-ethereum txpool,
treat these as a best-effort adaptation over that mempool rather than a
byte-for-byte go-ethereum txpool implementation — no explicit fidelity caveat
was found in the vendored source, so no further gap is claimed beyond that
structural difference.

## Namespace: `debug` (enabled on this chain's deployment)

Also enabled via the same `--json-rpc.api` flag. Source:
`rpc/namespaces/ethereum/debug/api.go` and `.../debug/trace.go`.

### Tracing

| Method | Params | Returns | Description |
|---|---|---|---|
| `debug_traceTransaction` | `hash`, `TraceConfig?` | trace result | Replay and trace a single transaction's EVM execution. |
| `debug_traceBlockByNumber` | `blockNr`, `TraceConfig?` | `[]TxTraceResult` | Trace every tx in a block, by number. |
| `debug_traceBlockByHash` | `hash`, `TraceConfig?` | `[]TxTraceResult` | Trace every tx in a block, by hash. |
| `debug_traceBlock` | RLP-encoded block, `TraceConfig?` | `[]TxTraceResult` | Trace every tx in a supplied raw block. |
| `debug_traceCall` | `TransactionArgs`, `blockNrOrHash`, `TraceConfig?` | trace result | Trace a hypothetical `eth_call`-style execution. |
| `debug_intermediateRoots` | `hash`, config | `[]common.Hash` | Intermediate state roots after each tx in a block. |

### Raw block/header access

| Method | Params | Returns | Description |
|---|---|---|---|
| `debug_getRawBlock` | `blockNrOrHash` | `hexutil.Bytes` | RLP-encoded block. |
| `debug_getHeaderRlp` | `number` | `hexutil.Bytes` | RLP-encoded header. |
| `debug_getBlockRlp` | `number` | `hexutil.Bytes` | RLP-encoded block (by number only). |
| `debug_printBlock` | `number` | `string` | Human-readable block dump. |

### Process/profiling (Go runtime introspection — gated by `EnableProfiling`)

| Method | Params | Returns | Description |
|---|---|---|---|
| `debug_cpuProfile` | `file`, `nsec` | — | Capture a CPU profile to a file for N seconds. |
| `debug_startCPUProfile` | `file` | — | Start CPU profiling. |
| `debug_stopCPUProfile` | — | — | Stop CPU profiling. |
| `debug_blockProfile` | `file`, `nsec` | — | Capture a blocking-profile. |
| `debug_mutexProfile` | `file`, `nsec` | — | Capture a mutex-contention profile. |
| `debug_setMutexProfileFraction` | `rate` | — | Adjust mutex-profile sampling rate. |
| `debug_writeBlockProfile` | `file` | — | Write current block profile to a file. |
| `debug_writeMemProfile` | `file` | — | Write current heap profile to a file. |
| `debug_writeMutexProfile` | `file` | — | Write current mutex profile to a file. |
| `debug_setBlockProfileRate` | `rate` | — | Adjust block-profile sampling rate. |
| `debug_gcStats` | — | `debug.GCStats` | Go GC statistics. |
| `debug_memStats` | — | `runtime.MemStats` | Go runtime memory stats. |
| `debug_freeOSMemory` | — | — | Force-return memory to the OS. |
| `debug_setGCPercent` | `v` | previous value | Adjust Go's GC target percentage. |
| `debug_stacks` | — | `string` | Dump all goroutine stacks. |
| `debug_goTrace` | `file`, `nsec` | — | Capture a Go execution trace for N seconds. |
| `debug_startGoTrace` | `file` | — | Start a Go execution trace. |
| `debug_stopGoTrace` | — | — | Stop the Go execution trace. |

`ctx.Logger` calls in these handlers use `EnableProfiling` from
`JSONRPCConfig` (`app.toml`'s `[json-rpc] enable-profiling`, default `false`)
to gate the profiling subset — check that setting if these return a
"profiling disabled" style error.

No explicit source comment was found stating reduced trace fidelity versus a
native go-ethereum node, so no such caveat is asserted here; the trace path
does go through cosmos/evm's own tracer plumbing (`evmtrace`/OpenTelemetry
spans wrapping each call) rather than being a direct geth passthrough, which
is the one structural difference confirmed in source.

## Custom EVM-callable contracts on this chain

Three contracts are callable from the EVM beyond the standard namespaces above.
Two are **static precompiles** (registered by Go address in the EVM keeper, see
`app/evm.go`), one is a **dynamic ERC20 precompile** (registered through the
`erc20` module's token-pair mechanism, see `app/register_sbd.go`) — the
practical difference is explained under SBD below.

### Static precompile activation

Both static precompiles are registered on the EVM keeper's precompile map in
Go code (`app/evm.go`'s `registerEVMModules`), but a static precompile is only
*callable* if its address is also listed in the `evm` module's
`active_static_precompiles` genesis/governance param.

`steemvmd init` bakes this in automatically as of
`cmd/steemvmd/cmd/init_genesis_defaults.go`: a fresh init's genesis.json
activates all 11 static precompiles this chain ships — the 9 standard
cosmos/evm ones (p256 `0x...0100`, bech32 `0x...0400`, staking `0x...0800`,
distribution `0x...0801`, ics20 `0x...0802`, bank `0x...0804`, gov `0x...0805`,
slashing `0x...0806`, ics02 `0x...0807`) plus this chain's own two
(steembridge `0x...0900`, oracledata `0x...0902`). Core EVM protocol
precompiles (ecrecover, sha256, modexp, etc.) need no entry — cosmos/evm's
`IsAvailableStaticPrecompile` (`x/vm/keeper/static_precompiles.go`) always
allows those regardless of this param. Confirm the live chain's actual param
with:

```bash
steemvmd query evm params --output json | jq '.active_static_precompiles'
```

The dynamic SBD ERC20 precompile is unaffected by this param — it is enabled
directly via `Erc20Keeper.EnableDynamicPrecompile` in `registerSBD` (see below),
not through the static-precompile list.

### 1. `steembridge` precompile — `0x0000000000000000000000000000000000000900`

Source: `precompiles/steembridge/ISteemBridge.sol`, `abi.json`, `steembridge.go`,
`tx.go`, `query.go`. Wraps `x/oracle/bridge` (Go import path; on-chain module
name still `steembridge`).

| Function | Inputs | Outputs | Type | Description |
|---|---|---|---|---|
| `confirmName(uint64 registrationId)` | `registrationId` | `bool success` | **tx** (nonpayable) | Confirms a name registration that reached validator-attestation threshold. `msg.sender` must be the registration's memo-derived destination address or it reverts. Equivalent to `MsgConfirmName`, but note: the Cosmos tx path is fee-exempt, this EVM path is **not** — it costs normal gas. |
| `bridgeOut(string destinationSteemAccount, uint256 amountAsteem, string memo)` | destination Steem account, amount in `asteem` (18 dec), memo | `bool success` | **tx** (nonpayable) | Burns `asteem` from `msg.sender` and records a `Withdrawal` for validators to relay to Steem. Amount must be a positive multiple of `10^15` asteem (one millisteem). Destination `"null"` is the provable-burn path. Equivalent to `MsgBridgeOut`. |
| `resolveName(string steemAccount)` | Steem account name | `address addr, uint64 registrationId, uint64 linkedAt` | **view** | Resolves an active name link. Returns all-zero values (not a revert) if unset — safe to probe. |
| `namesOf(address owner)` | address | `string[] steemAccounts` | **view** | Lists Steem account names actively linked to an address. |
| `awaitingRegistrationIds(address destination)` | address | `uint64[] registrationIds` | **view** | Lists registration ids awaiting confirmation by a destination address. |

Events: `NameConfirmed(address indexed confirmer, uint64 registrationId, string steemAccount)`,
`BridgeOutRequested(address indexed sender, string destinationSteemAccount, uint256 amountAsteem, string memo, uint64 withdrawalId)`.

**Read example (`cast call`, assuming the precompile is active per the genesis
caveat above):**

```bash
cast call 0x0000000000000000000000000000000000000900 \
  "resolveName(string)(address,uint64,uint64)" "someaccount" \
  --rpc-url http://localhost:8545
```

**State-changing example — `bridgeOut`:** this is a real transaction, not a
`cast call`. It burns `asteem` and only makes sense as a signed, broadcast tx
(via `cast send` with a funded private key, or from a dApp/wallet flow):

```bash
cast send 0x0000000000000000000000000000000000000900 \
  "bridgeOut(string,uint256,string)" "destination-account" 1000000000000000 "withdraw memo" \
  --rpc-url http://localhost:8545 --private-key $PRIVKEY
```

(`1000000000000000` = `10^15` asteem = 1 millisteem, the minimum granularity.)
`confirmName` is likewise a real tx (`cast send`, not `cast call`).

### 2. `oracledata` precompile — `0x0000000000000000000000000000000000000902`

Source: `precompiles/oracledata/IOracleData.sol`, `abi.json`, `oracledata.go`,
`query.go`. Wraps `x/oracle/data`. Entirely read-only — `Precompile.IsTransaction`
always returns `false` (`oracledata.go`), so both methods are `eth_call`s, never
transactions.

| Function | Inputs | Outputs | Type | Description |
|---|---|---|---|---|
| `getPrice(string pair)` | pair, e.g. `"STEEM/USD_External"` | `uint256 rate, uint256 updateEpoch, uint256 updateTime` | **view** | Latest finalized power-weighted-median rate for a pair, as an 18-decimal fixed-point number (`LegacyDec` scale — divide by `1e18` for the human value). Unknown/unfinalized pair returns all zeros rather than reverting; probe with `rate != 0`. `updateEpoch` is the vote-period epoch; `updateTime` is a Unix-second block time. |
| `getPrices()` | — | `string[] pairs, uint256[] rates, uint256[] updateTimes` | **view** | Every finalized rate, as parallel arrays. |

**Read example:**

```bash
cast call 0x0000000000000000000000000000000000000902 \
  "getPrice(string)(uint256,uint256,uint256)" "STEEM/USD_External" \
  --rpc-url http://localhost:8545
```

### 3. SBD dynamic ERC20 — `0x0000000000000000000000000000000000000901`

Source: `app/register_sbd.go` (`SBDERC20Address`, `registerSBD`). This is
**not** a static precompile like the two above — it is registered dynamically
through the `x/erc20` module's token-pair mechanism
(`Erc20Keeper.SetToken` + `Erc20Keeper.EnableDynamicPrecompile`), driven both at
genesis (`InitChainer`) and idempotently by the `v0.0.3` upgrade handler, so it
is **not** gated by the `active_static_precompiles` param that the other two
precompiles need — a dynamic ERC20 pair is active as soon as it's registered on
the `erc20` keeper, independent of that list.

Practically: this exposes the native `asbd` bank balance as a **standard
ERC20 token** at that address — `balanceOf`, `transfer`, `approve`,
`transferFrom`, `totalSupply`, `name`, `symbol`, `decimals`, plus `Transfer`/
`Approval` events, all mirroring the underlying bank module balance/supply of
`asbd`. There is no bespoke ABI to document here (unlike steembridge/oracledata)
— any standard ERC20 ABI (OpenZeppelin's `IERC20`, ethers.js's built-in ERC20
fragment, etc.) works out of the box; a wallet or dApp can just "add token"
`0x0000000000000000000000000000000000000901` like any other ERC20 and see/send
`asbd` balances. Denom metadata (`registerSBD`): name `SBD`, symbol `SBD`,
display `sbd`, 18 decimals.

**Read example (standard ERC20 ABI, `cast`'s built-in shorthand):**

```bash
cast call 0x0000000000000000000000000000000000000901 \
  "balanceOf(address)(uint256)" 0xYourAddress \
  --rpc-url http://localhost:8545
```

## Hardhat example

```js
// hardhat.config.js
module.exports = {
  solidity: "0.8.20",
  networks: {
    steemvm: {
      url: "http://localhost:8545",
      chainId: 8163,
      accounts: [process.env.PRIVATE_KEY],
    },
  },
};
```

```js
// scripts/read-precompiles.js — ethers v6 example
const { ethers } = require("hardhat");

const STEEMBRIDGE = "0x0000000000000000000000000000000000000900";
const ORACLEDATA   = "0x0000000000000000000000000000000000000902";
const SBD_ERC20     = "0x0000000000000000000000000000000000000901";

async function main() {
  const bridgeAbi = require("../precompiles/steembridge/abi.json").abi;
  const oracleAbi = require("../precompiles/oracledata/abi.json").abi;
  const erc20Abi  = [
    "function balanceOf(address) view returns (uint256)",
    "function symbol() view returns (string)",
  ];

  const [signer] = await ethers.getSigners();

  const bridge = new ethers.Contract(STEEMBRIDGE, bridgeAbi, signer);
  console.log(await bridge.resolveName("someaccount"));

  const oracle = new ethers.Contract(ORACLEDATA, oracleAbi, signer);
  console.log(await oracle.getPrice("STEEM/USD_External"));

  const sbd = new ethers.Contract(SBD_ERC20, erc20Abi, signer);
  console.log(await sbd.symbol(), await sbd.balanceOf(await signer.getAddress()));
}

main();
```

## Summary of what's verified vs. not

Verified directly from source in this session:
- The five enabled namespaces on the `docker-compose.yml` deployment (`eth`,
  `net`, `web3`, `txpool`, `debug`) and the library's un-flagged default
  (`eth`, `net`, `web3`) — `cmd/steemvmd/cmd/config.go`,
  `Instructions/app.toml.example`, `docker-compose.yml`,
  `cosmos/evm@v0.7.1`'s `server/config/config.go`.
- Every method listed per namespace, read from the actual Go method sets in
  `cosmos/evm@v0.7.1`'s `rpc/namespaces/ethereum/{eth,eth/filters,net,web3,txpool,debug}/api.go`.
- Both static precompiles' full ABI and Go dispatch (`ISteemBridge.sol`,
  `abi.json`, `steembridge.go`/`tx.go`/`query.go`; `IOracleData.sol`,
  `abi.json`, `oracledata.go`), their fixed addresses, and their
  registration/read-write classification.
- The SBD dynamic ERC20's address, registration mechanism, and the fact it's
  independent of `active_static_precompiles` (`app/register_sbd.go`).
- `Instructions/genesis.json`'s `active_static_precompiles` array now lists all
  11 addresses this chain activates, matching what `steemvmd init` produces
  automatically (`cmd/steemvmd/cmd/init_genesis_defaults.go`) — reconfirm
  against the *live* chain's current param regardless, since it's a
  governance-settable value that can change post-genesis.

Not independently verified (flagged rather than guessed):
- Whether the currently *running* chain's on-chain `evm.params` still matches
  the checked-in `Instructions/genesis.json` byte-for-byte, versus having been
  changed since by a governance param-change proposal — check with
  `steemvmd query evm params` against the live node.
- Whether the deployed node's `app.toml` `ws-address` was hand-edited away from
  the `127.0.0.1:8546` template default described above.
- No block explorer is known to exist for this chain; the MetaMask table above
  leaves that field intentionally blank.
