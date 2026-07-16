# Running a SteemVM Node & Becoming a Validator

This guide takes you from nothing to a running, staked validator using the
`docker-compose.yml` in the repository root. The files in this directory
(`app.toml`, `config.toml`, `client.toml`, `genesis.json`) are the canonical
node configuration — the compose setup copies them into the node home on
every start. You don't need to edit anything by hand, though you may
optionally set your node moniker in `config.toml` first (see step 2).

> **Read this first — validators must have a Steem identity.** SteemVM does not
> let anonymous validators join. To create a validator you must prove you own a
> Steem account by linking it to your chain address through the name service,
> and publish that account's three public keys. Concretely, your validator's
> **moniker must be your Steem username**, that username must have an **ACTIVE**
> name link pointing at **your validator key's address**, and your
> `details` field must carry your `owner`/`active`/`posting` public keys.
> Steps 5 and 6 below do the linking; `create-validator` (step 9) is rejected
> until they are done.

**The whole path at a glance:**

| Step | What |
|---|---|
| 1–3 | Install Docker, start the node, wait for sync |
| 4 | Create your key (your chain address) |
| 5 | Send `0.001 STEEM` with memo `svm-register <address>` |
| 6 | Confirm the name is yours → link goes ACTIVE |
| 7 | Get faucet coins for the self-stake |
| 8–9 | Build `validator.json` and create the validator |
| 10+ | Attest Steem transfers as a bonded validator |

> **Placeholders**: anything in `<angle brackets>` below is a value you must
> substitute — replace the brackets too. Pasting `<your-address>` literally
> makes the shell try to read from a file and fail with
> `bash: your-address: No such file or directory`.

## 1. Prerequisites: Docker and Docker Compose

Make sure both are installed before anything else:

```sh
docker --version
docker compose version
```

If either command fails, install [Docker Desktop](https://docs.docker.com/get-docker/)
(Windows/macOS) or [Docker Engine + the Compose plugin](https://docs.docker.com/engine/install/)
(Linux), then re-check.

## 2. Start the node

> **Optional but recommended — set your node moniker first.** Before the first
> `docker compose up`, open [`Instructions/config.toml`](config.toml) and change
> the node moniker (line 22) from `your_username` to your Steem username:
>
> ```toml
> # A custom human readable name for this node
> moniker = "blazed007"
> ```
>
> This is only the node's cosmetic P2P label (shown in RPC `/status` and to your
> peers). It is **not verified** and is **not** your on-chain validator identity —
> that is the separate `moniker` you set in `validator.json` (step 8), which
> *must* be your Steem username and *is* verified. Using your Steem username here
> too just makes your node easy to recognize on the network. Edit it in
> `Instructions/config.toml`, **not** the copy inside the container: the compose
> setup re-copies the files from `Instructions/` into the node home on **every**
> start, so an edit made only inside the container is wiped on the next restart.

From the repository root:

```sh
docker compose up -d
```

The first start compiles the chain binary inside the container, initializes
the node home, and copies the config + genesis from this directory. Watch
progress with:

```sh
docker compose logs -f
```

## 3. Wait for block sync

The node must be fully synced before you do anything else. Check sync status:

```sh
curl -s http://localhost:26657/status | grep catching_up
```

Wait until it reports `"catching_up": false`. You can also watch the block
height climb in the logs (`docker compose logs -f`).

## 4. Create your address (key)

Create a key inside the running container. `blazed007` is just the **local
keyring name** — use whatever you like, but use the same name everywhere below:

```sh
docker exec -it steemvm-node /root/go/bin/steemvmd keys add blazed007 --key-type eth_secp256k1 --keyring-backend test --home /root/.steemvm
```

This prints your `steem...` address **and a mnemonic phrase. Write the
mnemonic down and keep it safe — it is the only way to recover the key.**

Print the address again any time (you will need it in step 5):

```sh
docker exec steemvm-node /root/go/bin/steemvmd keys show blazed007 -a --keyring-backend test --home /root/.steemvm
```

> **Key type**: SteemVM uses Ethermint/Evmos-style `eth_secp256k1` keys at
> BIP44 coin type 60 (this is the binary's default; the explicit
> `--key-type eth_secp256k1` just guards against older builds). This means
> your mnemonic is **MetaMask-compatible**: importing it into MetaMask gives
> you the exact same account — the `steem...` address and the `0x...`
> address are two views of the same key. Do NOT create keys with the plain
> `secp256k1` type: those accounts cannot sign EVM transactions and their
> mnemonic derives a different address in MetaMask.

> The `test` keyring stores keys unencrypted inside the `steemvm-home` docker
> volume. Fine for a testnet; do not use it for anything holding real value.

## 5. Link your Steem account (send 0.001 STEEM)

From **the Steem account you want your validator to be known as**, send a
transfer on the Steem blockchain:

| Field | Value |
|---|---|
| **To** | `blaze.apps` (the gateway account) |
| **Amount** | `0.001 STEEM` (the minimum; more is fine) |
| **Memo** | `svm-register <your steem1... address from step 4>` |

Example memo:

```
svm-register steem1rjy2n2gcu9qjdcftz3vhu5hy2r2lh6kvht06gk
```

Use any Steem wallet (steemit.com wallet, Keychain, etc.). Two things that
trip people up:

- **The sending account is the name that gets linked.** If you send from
  `blazed007`, then `blazed007` is the username you must later use as your
  validator moniker. Sending from the wrong account links the wrong name.
- **The memo address must be your validator key's address** (step 4). The
  chain checks that the linked address is the same account that creates the
  validator — a link to some other address will not let you validate.
  A `0x...` address works too; it is the same account.

Validators' relayers watch Steem's last irreversible block and attest your
transfer automatically — usually within a minute or two. Once validators
holding more than 2/3 of bonded stake have attested it, the registration
moves to `AWAITING_CONFIRMATION`. Watch for it:

```sh
docker exec -it steemvm-node /root/go/bin/steemvmd query steembridge \
  awaiting-name-registrations-by-destination <your-address>
```

Note the `id` in the output — that is your `registration_id` for step 6.
You can also list every registration ever made for your Steem name:

```sh
docker exec -it steemvm-node /root/go/bin/steemvmd query steembridge \
  name-registrations-by-account blazed007
```

> At this point the chain also credits the `0.001 STEEM` you sent to your
> address as `asteem`, so a brand-new address has a little gas to work with.

## 6. Confirm the name is yours

Attestation proves the transfer happened; **confirmation proves you control
the destination address.** The link is not ACTIVE until you confirm it, and
only the address in the memo can do so.

```sh
docker exec -it steemvm-node /root/go/bin/steemvmd tx steembridge confirm-name <registration-id> \
  --from blazed007 --keyring-backend test --home /root/.steemvm \
  --chain-id steemvm --gas auto --gas-adjustment 1.5 --fees 0asteem -y
```

Valid confirmations are **fee-exempt on the CLI path**, so `--fees 0asteem`
works even with an empty balance. (Prefer MetaMask? Call
`confirmName(<registration-id>)` on the precompile at
`0x0000000000000000000000000000000000000900` — see section 11. That path pays
normal gas, which the credited registration fee covers.)

Verify the link is live — this must print **your** address:

```sh
docker exec -it steemvm-node /root/go/bin/steemvmd query steembridge resolve-name blazed007
```

The reverse lookup should list your name:

```sh
docker exec -it steemvm-node /root/go/bin/steemvmd query steembridge names-by-address <your-address>
```

Only once `resolve-name` returns your address are you allowed to create a
validator. (Re-linking later is allowed: a newer confirmed registration
supersedes the old link.)

## 7. Get faucet coins

**Before creating a validator, ask for faucet coins in the project Discord.**
Post the `steem...` address from step 4 and wait until the tokens arrive. You
need enough to cover the self-stake (the `amount` in step 8) plus a little
extra for gas. Check your balance with:

```sh
docker exec -it steemvm-node /root/go/bin/steemvmd query bank balances $(docker exec steemvm-node /root/go/bin/steemvmd keys show blazed007 -a --keyring-backend test --home /root/.steemvm)
```

## 8. Create validator.json

First get **your node's** consensus public key:

```sh
docker exec steemvm-node /root/go/bin/steemvmd comet show-validator --home /root/.steemvm
```

Then collect your Steem account's three **public** keys. They are public data —
fetch them straight from a Steem node (replace `blazed007` with your account):

```sh
curl -s https://api.steemit.com -X POST -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","method":"condenser_api.get_accounts","params":[["blazed007"]],"id":1}' \
  | python3 -c "import sys,json; d=json.load(sys.stdin)['result'][0]; \
print('owner  ', d['owner']['key_auths'][0][0]); \
print('active ', d['active']['key_auths'][0][0]); \
print('posting', d['posting']['key_auths'][0][0])"
```

> These are **public** keys (they start with `STM`). Never put a private key
> (`5...` / `P5...`) in this file — it would be published on-chain forever.

Now create `validator.json` in the **repository root** (the repo is mounted
into the container at `/workspace`, so the file appears there as
`/workspace/validator.json`). Paste your own consensus pubkey and your own
Steem keys — the values below are examples and will NOT work on your node:

```sh
cat > validator.json <<'EOF'
{
  "pubkey": {"@type":"/cosmos.crypto.ed25519.PubKey","key":"xZYrcNf4q0nRQ7h/BTsjcR82HH28UnkeZzBWeZQKNJo="},
  "amount": "1000000000000000000000asteem",
  "moniker": "blazed007",
  "identity": "",
  "website": "",
  "security": "",
  "details": "owner=STM<your-owner-key>;active=STM<your-active-key>;posting=STM<your-posting-key>",
  "commission-rate": "0.10",
  "commission-max-rate": "0.20",
  "commission-max-change-rate": "0.01",
  "min-self-delegation": "1"
}
EOF
```

Field notes:

- **`moniker` must be your Steem username** — the exact account you sent the
  `svm-register` transfer from in step 5, now ACTIVE-linked to this key. It is
  no longer a free-form display name: the chain verifies this username resolves
  to this validator's own account.
- **`details` is mandatory** and must parse as
  `owner=STM…;active=STM…;posting=STM…` — the three keys from above, separated
  by `;`, in any order. Do **not** put your username here; the moniker carries
  it. Each key is format-checked on-chain, so a malformed `STM…` value is
  rejected.
- `amount` is your self-stake in `asteem` (18 decimals):
  `1000000000000000000000asteem` = 1000 STEEM. It must be covered by your
  faucet balance (step 7).
- `commission-*` and `min-self-delegation` are standard Cosmos staking
  settings; the values above are sane defaults.

## 9. Stake (create the validator)

```sh
docker exec -it steemvm-node /root/go/bin/steemvmd tx staking create-validator /workspace/validator.json \
  --from blazed007 --keyring-backend test --home /root/.steemvm \
  --chain-id steemvm --gas auto --gas-adjustment 1.5 \
  --gas-prices 1000000000asteem -y
```

Confirm your validator is live and bonded:

```sh
docker exec -it steemvm-node /root/go/bin/steemvmd query staking validators
```

Your moniker should appear with `status: BOND_STATUS_BONDED`. Read your
recorded Steem identity back at any time (accepts `steemvaloper…`, `steem1…`,
or `0x…`):

```sh
docker exec -it steemvm-node /root/go/bin/steemvmd query steembridge validator-identity <your-valoper-address>
```

> **Editing later**: `tx staking edit-validator` is held to the same rule. An
> edit that leaves the moniker and details untouched is fine, but one that
> rewrites either to something that no longer resolves to a valid, owned,
> ACTIVE identity is rejected — you cannot strip your identity after the fact.

### Add more stake later

`create-validator` only ever **creates** a validator — running it again won't
top up your stake. To increase the stake on a validator that already exists you
**delegate** to it; delegating from your own operator key is how you raise your
own self-stake.

First grab your validator's operator address (`--bech val` prints the
`steemvaloper1...` encoding of the same key):

```sh
docker exec steemvm-node /root/go/bin/steemvmd keys show blazed007 --bech val -a --keyring-backend test --home /root/.steemvm
```

Then delegate. Capturing it in a shell variable avoids copy/paste mistakes:

```sh
VALOPER=$(docker exec steemvm-node /root/go/bin/steemvmd keys show blazed007 --bech val -a --keyring-backend test --home /root/.steemvm)

docker exec -it steemvm-node /root/go/bin/steemvmd tx staking delegate \
  "$VALOPER" 5000000000000000000000asteem \
  --from blazed007 --keyring-backend test --home /root/.steemvm \
  --chain-id steemvm --gas auto --gas-adjustment 1.5 \
  --gas-prices 1000000000asteem -y
```

`5000000000000000000000asteem` is 5 followed by 21 zeros — 5000 STEEM at 18
decimals. Your balance must cover the amount **plus** gas.

Verify the new total — `tokens` should have grown by the delegated amount:

```sh
docker exec -it steemvm-node /root/go/bin/steemvmd query staking validator "$VALOPER"
```

> Delegating does **not** re-check your Steem identity: the validator gate only
> runs on `create-validator` / `edit-validator`, so you can top up freely.

## 10. Attesting Steem transfers (bridge-in & name service)

Once you are a **bonded validator**, you participate in the bridge by
attesting to STEEM transfers sent to the gateway account on the Steem
blockchain — including other people's `svm-register` transfers like the one
you sent in step 5. When validators representing more than 2/3 of bonded stake
have submitted **identical** facts, the chain acts on them.

**Memo formats users send to the gateway on Steem:**

| Memo | Meaning |
|---|---|
| `svm-deposit <address>` (or just `<address>`) | Bridge deposit — mints asteem to the address |
| `svm-register <address>` (≥ 0.001 STEEM) | Name registration — links the sender's Steem account to the address (the address must then confirm) |

`<address>` is either a `steem1...` or a `0x...` address.

### The built-in relayer (recommended)

The node binary does the watching and attesting for you. Enable it in
`/root/.steemvm/config/app.toml`:

```toml
[steem-relayer]
steem-rpc-url = "https://api.steemit.com"
key-name = "blazed007"        # your keyring key (the validator operator account)
```

and restart the node. The relayer polls Steem's **last irreversible block**
every 3 seconds, scans transfers to the gateway account, and broadcasts the
attestation transactions automatically (fee-exempt — they cost you nothing).
Its scan cursor persists in `data/steem_relayer_state.json`; it idles
harmlessly while your validator is not bonded, and catches up automatically
after downtime.

Check where it has scanned to:

```sh
docker exec steemvm-node cat /root/.steemvm/data/steem_relayer_state.json
```

### Manual attestation (fallback)

```sh
docker exec -it steemvm-node /root/go/bin/steemvmd tx steembridge submit-steem-deposit \
  <txid> <op-index> <steem-block> <steem-timestamp> <steem-sender> <gateway-account> <amount-millisteem> <memo> \
  --from blazed007 --keyring-backend test --home /root/.steemvm \
  --chain-id steemvm --gas auto --gas-adjustment 1.5 \
  --gas-prices 1000000000asteem -y
```

Example — Steem tx `bce1dd3184e39bcd9bdd7886b22681268a708e03` where `alice`
sent `1000.000 STEEM` to the gateway with an EVM address as the memo:

```sh
docker exec -it steemvm-node /root/go/bin/steemvmd tx steembridge submit-steem-deposit \
  bce1dd3184e39bcd9bdd7886b22681268a708e03 0 95000000 2026-07-10T08:00:00 alice blaze.apps 1000000 0x9b379Dfd7d22eA756eA79a19B3336192d64DcD1a \
  --from blazed007 --keyring-backend test --home /root/.steemvm \
  --chain-id steemvm --gas auto --gas-adjustment 1.5 \
  --gas-prices 1000000000asteem -y
```

Field-by-field:

| Field | Meaning |
|---|---|
| `txid` | The Steem transaction ID containing the transfer |
| `op-index` | Index of the transfer operation inside that transaction (usually `0`) |
| `steem-block` | Steem block number the transaction was included in |
| `steem-timestamp` | The Steem block timestamp, exactly as Steem reports it |
| `steem-sender` | Steem account that sent the transfer |
| `gateway-account` | The gateway account the transfer was sent to — must match the on-chain param (`blaze.apps`) or the tx is rejected |
| `amount-millisteem` | Amount in millisteem: STEEM × 1000, so `1000.000 STEEM` = `1000000` |
| `memo` | The transfer's memo, verbatim — a `steem...` bech32 address or a `0x...` EVM address; this decides who receives the minted `asteem` |

(For a name registration, the same fields go to
`tx steembridge submit-name-registration` instead.)

Rules that matter:

- **Every field must exactly match what is on Steem** (and what the other
  validators submit). A submission that disagrees with the already-pending
  deposit is ignored (with an on-chain mismatch event) and does not count
  toward the threshold.
- **Each validator can confirm a given `(txid, op-index)` only once.**
- **Copy the memo verbatim — do not "fix" or convert it.** The chain itself
  derives the destination from the raw memo in consensus. An unparseable memo
  makes the deposit UNCLAIMABLE rather than minting to a guess.
- **Honest confirmations are free**: valid submissions from bonded validators
  are fee-exempt (up to 100 per validator per block), so the tx succeeds even
  with a zero fee. Submissions that would be rejected pay normal fees.
- A pending deposit that doesn't reach the threshold within
  `deposit_timeout_blocks` (~7 days) expires and can be submitted fresh.

Track deposit status:

```sh
docker exec -it steemvm-node /root/go/bin/steemvmd query steembridge pending-deposits
docker exec -it steemvm-node /root/go/bin/steemvmd query steembridge deposit-by-txid <txid> <op-index>
docker exec -it steemvm-node /root/go/bin/steemvmd query steembridge bridge-statistics
```

## 11. Name service & bridge-out from MetaMask (precompile)

The chain exposes the steembridge module to EVM wallets and smart contracts
through a precompiled contract at:

```
0x0000000000000000000000000000000000000900
```

The full interface is [`precompiles/steembridge/ISteemBridge.sol`](../precompiles/steembridge/ISteemBridge.sol):

- `confirmName(uint64 registrationId)` — the step-6 confirmation, from
  MetaMask. Must be sent **from the address the memo pointed at**; any other
  sender reverts. (The Cosmos CLI equivalent,
  `steemvmd tx steembridge confirm-name`, is gas-free; this path pays normal
  gas.)
- `bridgeOut(string destinationSteemAccount, uint256 amountAsteem, string memo)` —
  burn asteem from your account and queue a withdrawal to Steem. The amount
  must be a positive multiple of 10^15 (whole millisteem).
- `resolveName(string steemAccount)` / `namesOf(address)` /
  `awaitingRegistrationIds(address)` — read-only lookups; `resolveName`
  returns `address(0)` for unlinked names instead of reverting, so contracts
  can resolve Steem account names on-chain.

Example with ethers.js against the chain's JSON-RPC (port 8545):

```js
const abi = ["function confirmName(uint64 registrationId) returns (bool)",
             "function awaitingRegistrationIds(address destination) view returns (uint64[])",
             "function resolveName(string steemAccount) view returns (address addr, uint64 registrationId, uint64 linkedAt)"];
const bridge = new ethers.Contract("0x0000000000000000000000000000000000000900", abi, signer);
const ids = await bridge.awaitingRegistrationIds(await signer.getAddress());
await bridge.confirmName(ids[0]);           // MetaMask pops up, signs, done
console.log(await bridge.resolveName("blazed007"));
```

## 12. Public HTTPS endpoints (nginx reverse proxy + Let's Encrypt)

By default the node publishes its RPC ports in the clear on the host
(`docker-compose.yml`): EVM JSON-RPC on `8545`/`8546`, CometBFT RPC on `26657`,
the Cosmos REST API on `1317`. To serve these to the public over HTTPS — so
MetaMask, dApps, and explorers get a proper `https://`/`wss://` URL — put
[`nginx.conf`](nginx.conf) in front as a TLS-terminating reverse proxy and issue
certificates with [certbot](https://certbot.eff.org/).

> This is for a **public RPC provider** node. A pure validator should keep these
> ports firewalled and not expose them at all — the only port a validator must
> open to the world is the p2p port `26656` (which is a raw protocol and is
> **not** proxied by nginx).

### What maps where

| Public host | Backend | Purpose |
|---|---|---|
| `evm.<domain>` | `127.0.0.1:8545` | EVM JSON-RPC (HTTP) — MetaMask "New RPC URL" |
| `evm-ws.<domain>` | `127.0.0.1:8546` | EVM JSON-RPC (WebSocket) — `wss://` subscriptions |
| `rpc.<domain>` | `127.0.0.1:26657` | CometBFT RPC (incl. `/websocket`) |
| `api.<domain>` | `127.0.0.1:1317` | Cosmos REST API (LCD) |
| `grpc.<domain>` | `127.0.0.1:9090` | gRPC (optional; disabled in the config by default) |

### Prerequisites

- A host with a public IP and ports **80** and **443** open in the firewall.
- **DNS A/AAAA records** for each subdomain you want (`evm`, `evm-ws`, `rpc`,
  `api`) pointing at the host. certbot's HTTP-01 challenge needs these live
  first.
- nginx and certbot installed (Debian/Ubuntu):

  ```sh
  sudo apt update
  sudo apt install -y nginx certbot python3-certbot-nginx
  ```

### Steps

1. **Install the proxy config.** Copy `Instructions/nginx.conf` into nginx and
   replace the `example.com` domains with yours:

   ```sh
   sudo cp Instructions/nginx.conf /etc/nginx/conf.d/steemvm.conf
   sudo sed -i 's/example\.com/mychain.io/g' /etc/nginx/conf.d/steemvm.conf
   sudo nginx -t && sudo systemctl reload nginx
   ```

2. **Issue certificates.** The `--nginx` plugin obtains the certs over HTTP-01
   and edits each server block in place to add the `listen 443 ssl` block plus an
   HTTP→HTTPS redirect — you don't hand-write any TLS directives:

   ```sh
   sudo certbot --nginx \
     -d evm.mychain.io \
     -d evm-ws.mychain.io \
     -d rpc.mychain.io \
     -d api.mychain.io \
     --redirect --agree-tos -m you@mychain.io --no-eff-email
   ```

   Re-run `sudo nginx -t && sudo systemctl reload nginx` if you make further
   edits afterward.

3. **Confirm auto-renewal.** The certbot package installs a systemd timer that
   renews certs before they expire (90-day lifetime). Dry-run it:

   ```sh
   sudo certbot renew --dry-run
   systemctl list-timers | grep certbot
   ```

### Harden the exposure

- **Bind the backends to localhost.** Once nginx is proxying, change the port
  mappings in `docker-compose.yml` so the plaintext ports are only reachable by
  nginx, e.g. `"127.0.0.1:8545:8545"` (and the same for `8546`, `26657`, `1317`).
  Leave `26656:26656` public — p2p needs it. After that, only nginx on `443` is
  internet-facing.
- **Drop the `debug` JSON-RPC namespace for a public endpoint.** The compose
  command starts the node with `--json-rpc.api "eth,net,web3,txpool,debug"`;
  `debug_*` methods (e.g. `debug_traceTransaction`) are expensive and a DoS
  vector when open to the world. For a public provider, remove `debug` (and
  usually `txpool`) from that list.
- **Rate limiting** is already applied per client IP in `nginx.conf`
  (`limit_req_zone ... rate=25r/s`); adjust to your capacity.
- CometBFT RPC (`26657`) has `cors_allowed_origins = ["*"]` and the REST API
  (`1317`) has `enabled-unsafe-cors = true` in the shipped configs. That is fine
  for a permissionless public endpoint but review it if you intend to restrict
  access.

### Use it

- **MetaMask** → *Add network manually*: RPC URL `https://evm.mychain.io`, Chain
  ID `8163`, currency `STEEM`. WebSocket subscriptions use
  `wss://evm-ws.mychain.io`.
- **CometBFT RPC**: `https://rpc.mychain.io/status`,
  `https://rpc.mychain.io/net_info`, etc.
- **REST / LCD**: `https://api.mychain.io/cosmos/bank/v1beta1/...`.

## Troubleshooting

**Identity / validator creation**

- **`create-validator` rejected: "has no active name-service registration"** —
  your moniker isn't a live linked name. Either you skipped steps 5–6, or the
  registration is still `AWAITING_CONFIRMATION` (run `confirm-name`), or the
  moniker is misspelled. `query steembridge resolve-name <moniker>` must return
  your address.
- **`create-validator` rejected: "registered to a different account"** — the
  name is linked, but to a different address than the key you are creating the
  validator with. The `svm-register` memo must contain *this* key's address.
  Re-register with the correct address (a newer confirmed link supersedes the
  old one).
- **`create-validator` rejected: mentions Description.details / public key** —
  `details` is missing, malformed, or a key isn't a valid `STM…` value. It must
  read `owner=STM…;active=STM…;posting=STM…` with no `steem=` entry.
- **Registration never appears** — validators only attest Steem's *irreversible*
  blocks, so allow a minute or two. Confirm you sent to `blaze.apps`, sent at
  least `0.001 STEEM`, and used the `svm-register ` prefix followed by a space
  and the address.
- **`confirm-name` fails** — it must be signed by the address in the memo, and
  the registration must be in `AWAITING_CONFIRMATION`. Check with
  `query steembridge awaiting-name-registrations-by-destination <your-address>`.

**Node / staking**

- **Any tx fails with `Cannot encode unregistered concrete type ethsecp256k1.PubKey`**:
  your binary predates the legacy-amino key registration, so `--gas auto` (which
  simulates the tx) panics for every Cosmos tx. Rebuild the node
  (`docker compose up -d --build`). As a one-off workaround you can skip
  simulation with an explicit limit instead, e.g. `--gas 400000` in place of
  `--gas auto --gas-adjustment 1.5`.
- **Node won't start after a genesis change**: chain data in the
  `steemvm-home` volume no longer matches the new genesis. Reset with
  `docker compose down -v` (wipes node keys and caches too) and start over.
- **`create-validator` fails with insufficient funds**: your balance must
  cover `amount` + gas. Ask the faucet for more (step 7).
- **Validator jailed**: your node fell out of sync or was offline too long.
  Get it synced again, then unjail:
  `docker exec -it steemvm-node /root/go/bin/steemvmd tx slashing unjail --from blazed007 --keyring-backend test --home /root/.steemvm --chain-id steemvm --gas auto --gas-adjustment 1.5 --gas-prices 1000000000asteem -y`
