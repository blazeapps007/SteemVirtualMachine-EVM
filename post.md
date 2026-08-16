# SteemVM v0.0.3 — SBD Bridging, a Price Oracle, and Validator Accountability

*A deep dive into the next SteemVM upgrade: what's changing, why it matters, and what you need to do.*

---

Hi everyone 👋

SteemVM is the EVM app-chain bridged to the Steem mainchain — bridged STEEM is the native gas token, contracts run in a real Ethereum Virtual Machine, and a quorum of bonded validators (not a trusted relay) secures every deposit and withdrawal. If you've used the STEEM ↔ `asteem` bridge or deployed a contract with MetaMask against chain-id **8163**, you've already used it.

**v0.0.3** is our biggest release yet. It brings **SBD** onto the chain as a first-class bridged coin, adds a **validator-run price oracle**, introduces a **single accountability system** that keeps every validator honest across *all* of their duties, and reshapes the fee economics so that stakers actually earn. It ships as one coordinated governance upgrade that every node swaps to at the same block — with **near-zero downtime** thanks to cosmovisor.

Let me walk you through everything, in detail.

---

## 1. SBD is now bridgeable — meet `asbd`

Until now the bridge only moved **STEEM**. v0.0.3 adds **Steem Dollars (SBD)** as a second native bridged coin on SteemVM, called **`asbd`**.

- Send **SBD** to the gateway account with the same memo convention you already use for STEEM, and validators mint you `asbd` on SteemVM at a **1:1 peg** (3-decimal `millisteem` → 18-decimal on-chain, exactly like STEEM).
- `asbd` is exposed to the EVM as a **dynamic ERC-20 precompile at `0x…0901`**, so wallets and contracts see it as a normal token with proper `name`/`symbol`/`decimals` metadata. `balanceOf`, transfers, DeFi — all just work.
- Bridging out `asbd` works the same as STEEM: your coins are burned on SteemVM and a withdrawal request is recorded for validators to relay back to Steem.

Importantly, the **SBD peg stays 1:1** — the price oracle (below) is for smart-contract consumers and future peg logic, it is **never** a mint input. One SBD in always equals one `asbd` out.

SBD bridging is **feature-gated**: the off-chain oracle software only starts attesting SBD deposits once the chain is actually live on v0.0.3, so nothing can mint `asbd` before the chain knows how to.

## 2. A small bridge fee — that goes 100% to stakers

Every bridge-in and bridge-out now carries a tiny **bridge fee (0.25%)**. Here's the part that matters: **the whole fee is routed to staking rewards.**

- On a deposit, the full amount is minted, you receive **99.75%**, and the **0.25%** fee flows into a dedicated `bridge_reward` account.
- Each block, that account is swept into the fee collector and paid out by the distribution module — **100% to validators and their delegators**, at each validator's configured commission. Nothing leaks to anywhere else.
- It's multi-denom: STEEM bridging grows `asteem` rewards, SBD bridging grows `asbd` rewards.

So the fee isn't skimmed off to an operator — it becomes **real, claimable yield for everyone who stakes**. The gateway operator backs it with custody and forgoes the fee themselves.

## 3. The STEEMBLACKHOLE — one place coins are born, one place they die

We introduced a special module account, **`STEEMBLACKHOLE`**, that makes the token supply auditable at a glance.

- Every bridged coin is **minted into the black hole first**, then sent out to you — so on-chain a deposit reads as a *transfer from the black hole*, not a bare mint you can't trace.
- The black hole is also the **only place coins are ever burned**. Once per block it burns its entire balance. Bridge-out returns, the burn portion of tx fees, and anything else meant to disappear all funnel through this single sink.

One mint origin, one burn sink. If you want to reduce supply, you can literally send coins to the black hole address and watch total supply drop next block.

## 4. Fee economics: 50% stakers / 25% community / 25% burned

Ordinary transaction fees (including EVM gas) are now split three ways every block:

- **50%** stays for stakers,
- **25%** goes to the community pool (governance-controlled funding),
- **25%** is burned (via the black hole).

Combined with the bridge fee from §2, validators and delegators now have **two real, native reward streams** — a healthy, deflation-aware fee model rather than fees vanishing into a single bucket.

## 5. Withdrawals you can actually confirm

Bridging out used to end at "requested." Now the loop is closed:

- When the gateway pays your withdrawal on Steem (memo `svm-withdrawal <id>`), validators **attest the payout**, and at the ⅔ threshold the withdrawal flips **REQUESTED → PROCESSED** with the Steem payout txid recorded on-chain.
- A new **processed-withdrawals** query lets you see exactly which withdrawals have been paid, while **requested-withdrawals** stays the "still to pay" queue.

Full transparency on both sides of the bridge.

---

## 6. A validator-run price oracle (`x/oracle/data`)

This is a big new capability. SteemVM validators now collectively publish **on-chain exchange rates** for pairs like `STEEM/USD`, `STEEM/SBD`, and `SBD/USD`.

**How it works — commit-reveal, so nobody can copy:**

1. Each voting period (~1 hour), a validator submits a **prevote** — a hash committing to its prices without revealing them.
2. The next period it **reveals** the actual prices plus the secret salt; the chain checks the hash matches.
3. The chain takes the **power-weighted median** of all revealed votes. Median (not average) means a minority can't drag the price around, and it's weighted by stake so it reflects real economic backing.

**Reading prices from contracts:** a read-only **precompile at `0x…0902`** exposes `getPrice("STEEM/USD")` and `getPrices()` to the EVM. Rates come back as standard 18-decimal fixed-point, so a Solidity `rate / 1e18` recovers the human number. We ship the `IOracleData.sol` interface so you can drop it straight into a contract.

This unlocks price-aware DeFi, future peg mechanisms, and on-chain analytics — all sourced from the same validators that already secure the bridge, with no external trusted feed.

## 7. One accountability system for everything (`x/oracle` — unified slashing)

Validators now have **two jobs**: attest bridge events **and** vote prices. v0.0.3 introduces a single parent engine that scores **both duties together** and enforces participation — this is the safety heart of the release.

- Each validator gets **one combined score** per slash window: `w_price · (price accuracy) + w_bridge · (bridge attestation rate)`.
- **Opportunity-based & fair:** you're only judged on events that actually finalized while you were bonded, and duties with nothing to do that window are simply excluded. An honest node that misses one sporadic event is **not** punished.
- **The anti-masking `DutyFloor`:** here's the clever part. A validator that votes prices *perfectly* but attests **none** of the bridge events is **still slashed** — you cannot hide total absence from one duty behind a great score in the other. A naive pooled counter would let a validator coast on price votes while ignoring the bridge; the DutyFloor closes that hole.
- Fall below the threshold (or flat-line any active duty) and you're **jailed and slashed**, once per window.

All validator oracle messages — bridge attestations, payout confirmations, price votes — are **gas-free** (within a per-block cap), so doing your job costs nothing, but *not* doing it costs you. The result: a bridge and a price feed that stay live because it's economically irrational to slack off.

## 8. The off-chain oracle software

The node's companion oracle process got three upgrades, all feature-gated to self-activate once v0.0.3 is live:

- **SBD deposit extraction** — scans gateway transfers for both STEEM and SBD, tags the asset, and broadcasts the right attestation.
- **Withdrawal-payout detection** — watches the gateway's *outbound* Steem transfers and attests payouts so withdrawals reach PROCESSED.
- **Price feeder** — runs the commit-reveal cycle each voting period, with a hash that exactly matches the on-chain check. (The concrete market data *sources* are configurable and intentionally deferred for now — the machinery ships ready.)

---

## 9. Launching fresh — and cosmovisor for what comes next

Everything above ships **baked into genesis** as a fresh chain launch, so there's no migration to coordinate — the features are live from block 1.

- Starting a node is just `docker compose up -d`. A new node **replays cleanly from genesis** (the one in `Instructions/`); there's no state-sync step to worry about.
- The node runs under **cosmovisor**, so any *future* governance software-upgrade auto-swaps the staged binary at its height — no manual action at the block, near-zero downtime. It's there and ready even though this launch doesn't need it.

Becoming a validator still requires a **Steem identity**: your moniker must be your Steem username, linked ACTIVE to your validator address via the name service, with your account's public keys published. The full walkthrough lives in `Instructions/README.md`.

---

## 10. Under the hood: a cleaner module layout (nothing breaks)

We reorganized the code so the bridge and the new oracle modules live together as a family under `x/oracle/`:

- `x/oracle/bridge` — the Steem bridge (moved from `x/steembridge`)
- `x/oracle/data` — the price feed
- `x/oracle` — the unified slashing engine

This is **purely a source-code refactor**. The on-chain module name and storage stay exactly the same (`steembridge`), message formats are unchanged, and the bridge precompile stays at `0x…0900`. Your accounts, balances, names, and integrations are **completely unaffected** — nothing on-chain changes because of the move.

---

## TL;DR

- 🪙 **SBD bridging** as native `asbd` (ERC-20 at `0x…0901`), 1:1 peg
- 💸 **0.25% bridge fee → 100% to stakers**, multi-denom
- 🕳️ **STEEMBLACKHOLE**: one mint origin, one burn sink, auditable supply
- ⚖️ **Tx fees split 50% stakers / 25% community / 25% burned**
- ✅ **Withdrawals confirmable** on-chain (REQUESTED → PROCESSED)
- 📈 **Validator price oracle** with an EVM precompile at `0x…0902`
- 🛡️ **Unified slashing** across bridge + price duties, with an anti-masking floor
- 🚀 **Fresh launch** — features live from genesis; nodes run under cosmovisor for future upgrades
- 🔁 **New nodes just replay from genesis** — `docker compose up -d`, no state-sync step

This release turns SteemVM from "a bridge with an EVM" into a **validator-secured economic layer** for Steem: two bridged assets, a native price feed, real staking yield, and accountability that keeps it all running.

Questions, feedback, or want to run a validator? Drop a comment below. 🙌

*— The SteemVM team*
