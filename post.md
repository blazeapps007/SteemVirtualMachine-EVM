# SteemVM v0.0.6: Automatic Withdrawals, a Hardened Bridge, and a Security Refresh

*What's in the next SteemVM upgrade, why it matters, and what (little) you need to do.*

---

Hi everyone 👋

SteemVM is the EVM app-chain bridged to the Steem mainchain. Bridged STEEM is the native gas token, contracts run in a real Ethereum Virtual Machine (chain-id **8163**), and a quorum of bonded validators, not a trusted relay, secures every deposit and withdrawal.

**v0.0.6** is about making the bridge both **faster and safer**:

- **Withdrawals pay themselves.** Validators no longer relay payouts to Steem by hand; each validator's oracle can now run an automated signer for the **@svm.bank** gateway.
- **The gateway's signing power matches stake, exactly.** Every validator's share of @svm.bank equals its share of bonded stake, kept in sync automatically.
- **The bridge attestation logic is hardened**, closing the findings from our October security review.
- **The whole stack moves to the latest stable, security-patched dependencies.**

It ships as one coordinated governance upgrade, and your keys, balances, stake and validator identity are untouched.

---

## 1. Withdrawals now pay themselves: the svm.bank signer

### How the gateway works

When you bridge out, your coins are burned on SteemVM and a **withdrawal request** is recorded. The STEEM or SBD is then paid to you on Steem from **@svm.bank**, the gateway account.

@svm.bank has no keys of its own. It is a **multisig**: its authority is a list of validator accounts, each with a weight, and a transaction from @svm.bank is only valid when validators holding at least **75%** of the weight have signed it. No single validator can move the funds.

Until now, collecting those signatures was manual. v0.0.6 automates it.

### What the signer does

Every validator's oracle (the companion process that already attests bridge events) can now run a **svm.bank signer**. For each withdrawal:

1. **Propose.** One validator's signer is designated for that withdrawal. It drafts the Steem payout: a transfer from @svm.bank to your account, for your exact net amount, with the memo `svm-withdrawal <id>`.
2. **Check independently.** Every other signer verifies the payout against **SteemVM itself**, not against the proposer's word. The withdrawal must exist and still be unpaid, and the recipient, the amount (to the last 0.001) and the asset must all match the on-chain record.
3. **Approve.** Signers that agree publish their signatures in their own Steem profile metadata, which is public, auditable and needs no central server.
4. **Execute.** As soon as approvals reach 75% of @svm.bank's weight, any signer broadcasts the payout to Steem.
5. **Confirm.** Validators' oracles see the payout on Steem and attest it to SteemVM. At ⅔ of bonded voting power, your withdrawal flips **REQUESTED → PROCESSED**, with the Steem transaction ID recorded on-chain.

With enough validators running the signer, a withdrawal goes from request to payout in minutes, without anyone at a keyboard.

### Built so it can't pay twice

Moving real funds automatically demands paranoia, so the signer has hard rules:

- **One payout per withdrawal.** A signer approves at most one proposal per withdrawal, and only the earliest correct one. It also keeps a local record of every approval it signs, written *before* the signature is published, so even a lagging Steem node can't trick it into signing a second payout.
- **Never race the refund.** If a withdrawal isn't confirmed within 3.5 days, SteemVM automatically refunds it. The signer will not pay any withdrawal within about **one day** of that refund deadline, so you can never be paid on Steem *and* refunded on SteemVM.
- **Wrong is wrong forever, unsure means wait.** Content that can never be correct (wrong amount, wrong recipient, wrong memo) is denied. Anything that might clear later, such as a temporarily unreachable node, is left alone and checked again on the next pass.
- **Treasury and recovery operations are never automatic.** Staking @svm.bank's STEEM, powering down, or changing its recovery account always waits for humans.

### Only the active key, nothing more

A validator enables the signer with its Steem **active** key, and that is the only Steem key it ever uses. The owner, active and posting authorities of @svm.bank all resolve through validators' *active* keys, so an owner key is never needed. The signer **refuses to start** if:

- an owner key, posting key or master password is configured;
- the key doesn't match the validator's verified Steem identity;
- the key is one of the account's owner keys.

### Three clients, one result

The signer ships in all three oracle clients (**Go, Python and JS**), so each validator keeps whichever language they already run. All three are held to the same test data, built from real @svm.bank transactions on Steem mainnet. They produce **byte-identical** proposals and votes for the same inputs, so validators can mix clients freely.

### Turning it on (validators)

The signer is **off by default**. Once v0.0.6 has applied, add two lines to `oracle/.env`:

```sh
ORACLE_MULTISIG_ENABLED=true
ORACLE_STEEM_ACTIVE_WIF=5K…        # your validator account's ACTIVE key
```

We recommend starting with `ORACLE_MULTISIG_DRY_RUN=true`: the signer logs every decision it would make and publishes nothing. Publishing votes costs only Resource Credits from your Steem Power, never STEEM. The full rules are in `oracle/PROTOCOL.md` §11; the background is in `oracle/multisigguide.md`.

---

## 2. Signing power that matches stake, exactly

@svm.bank's weights are meant to follow each validator's stake on SteemVM. Today they were set by rounding each validator's share separately. That drifted the total to **10,003** and the threshold to **7,503**, instead of a clean 10,000 and 7,500.

The signers now keep the authority in sync automatically, and they do the math **exactly**:

- **Whole-number arithmetic only, no floating point.** Stake is an 18-decimal amount, too large for floating point to represent exactly. The weights always add up to **exactly 10,000** and the threshold is exactly **7,500** (75%).
- **Every signer computes the same answer.** Weights are taken from a fixed SteemVM block, chosen every 1,000 blocks. Each signer recomputes from that same block and approves only a byte-for-byte identical result.
- **No churn.** A new set is proposed only when a validator joins or leaves, or when someone's weight moves by more than 0.5 percentage points. Steem's rule of one owner change per hour is respected.

The first automatic sync will correct the 10,003 / 7,503 drift to exactly 10,000 / 7,500.

---

## 3. A hardened bridge (October security review)

We reviewed the chain and its precompiles end to end before going further. v0.0.6 closes what we found in the bridge's attestation logic.

**Attestations are tallied per observed fact.** Deposits, name registrations and withdrawal payouts used to adopt the facts from whichever validator attested first. Now each distinct version of the facts is counted separately, and only the version backed by ⅔ of bonded voting power wins. A minority reporting a bogus payout or wrong deposit details can no longer block or redirect the honest majority. Each validator still gets exactly one vote per event, so two versions can never both win. This closes a withdrawal double-payout path and a deposit/name griefing path.

**Size limits.** Memos are capped at 2,048 bytes (Steem's own limit), and timestamps are bounded and validated. Bridge-out now rejects absurd amounts and enforces the minimum on every path, including calls through the EVM precompile.

**Bounded end-of-block work.** Expiring stale deposits, withdrawals and name registrations now handles a bounded number of records per block, so a flood of open records can't slow block production.

**Fair duty credit.** A validator whose correct attestation lands in the same block, just after the ⅔ threshold was crossed, is now credited for it. Before, it was recorded as a missed duty, which counts toward slashing.

Every finding has its own regression test.

---

## 4. A security refresh of the whole stack

v0.0.6 moves to the newest stable releases that work together:

- **cosmos/evm v0.7.3**, an upstream security release: EVM state changes stay atomic when precompiles run, and a balance-overflow path is closed.
- **ibc-go v11.2.0** and the **cosmos go-ethereum fork v1.17.2-cosmos-1** (includes an EIP-712 fix).
- **gRPC v1.84.0**, with the OpenTelemetry libraries and other dependencies brought up to date.
- **Go 1.26.9**, which includes 12 standard-library security fixes.

Cosmos SDK stays at **v0.54.4** and CometBFT at **v0.39.4**. They are the newest versions cosmos/evm supports today; SDK v0.55 will follow once cosmos/evm releases support for it.

---

## 5. How the upgrade happens (validators)

v0.0.6 is a **coordinated upgrade**: governance sets a block height, and every node switches to the new binary at that height. Nodes run under **cosmovisor**, so once the binary is staged the switch happens automatically, with no reset and near-zero downtime.

**Stage it ahead of time, with one line from your SteemVM folder:**

```sh
git fetch && git checkout release/v0.0.6 && ./update.sh
```

`update.sh` detects whether your node runs in Docker or on bare metal and stages v0.0.6 where cosmovisor expects it. It also moves a running oracle to its latest image. For Docker nodes it prints a `sha256:` line, which must be:

```
cf0148411dd28b9c3116735b504dd425dbc9aee9e421160cfce82caf5b13b3eb
```

⚠️ **Before the upgrade height, don't change your node's image and don't run `docker compose pull && docker compose up -d` on the node.** Starting the new binary early halts your node and gets you jailed. The compose file keeps the live node on v0.0.5 until the upgrade applies, so plain commands stay safe.

If you miss staging, your node halts at the upgrade height. Nothing is lost: run the same one line, and `update.sh` stages v0.0.6, switches cosmovisor and restarts the node. The full guide, including manual steps, is in `Instructions/UPGRADE_v0.0.6.md`.

---

## 6. What changes for you

- **Bridging users:** nothing to do. Deposits and bridge-outs work exactly as before. Once validators switch on their signers, withdrawals are paid automatically instead of waiting for manual relaying.
- **Developers:** nothing to do. The chain-id, precompile addresses (`0x…0900` bridge, `0x…0901` SBD, `0x…0902` price oracle) and message formats are unchanged.
- **Validators:** stage the upgrade with the one line above. After it applies, turn on the svm.bank signer (start in dry-run). The first validator-set sync needs approvals from validators holding the current 7,503 threshold.

---

## TL;DR

- 🤖 **Automatic withdrawals:** an svm.bank signer in every validator's oracle proposes, independently verifies, signs and executes payouts.
- 🛑 **No double payouts:** one approval per withdrawal, a write-ahead approval record, and no payout near the refund deadline.
- 🔑 **Active key only:** the signer refuses to run with an owner key, posting key or master password.
- ⚖️ **Exact stake-weighted signing power:** whole-number math, always 10,000 total and 7,500 threshold, synced automatically.
- 🧩 **Go, Python and JS signers**, byte-identical and freely mixable.
- 🛡️ **Bridge hardening:** per-fact attestation tallies, field size limits, bounded end-of-block work, fair duty credit.
- 🔒 **Security refresh:** cosmos/evm v0.7.3, ibc-go v11.2.0, gRPC v1.84.0, Go 1.26.9.
- 🚀 **One-line staging:** `./update.sh`, then cosmovisor switches at the height.

v0.0.6 turns the bridge's last manual step into code that every validator can run, check and audit, without anyone having to trust anyone else.

Questions, feedback, or want to run a validator? Drop a comment below. 🙌

*— The SteemVM team*
