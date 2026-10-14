// policy.ts — the deterministic, verify-independently policy: what to
// approve, deny or leave for a human, and what to propose. Port of
// oracle/go/multisig/policy.go. A deny vote is final, so "deny" is only for
// content that is wrong and can never become right; anything that may clear
// later is "abstain" and gets re-reviewed next pass.

import { apportion, exceedsTolerance, type StakeEntry } from "./apportion";
import type { Vote } from "./ballot";
import { parseWithdrawalMemo, withdrawalMemo } from "./chain";
import { ApprovalJournal, journalConflicts, subjectOf } from "./journal";
import * as tx from "./steemTx";
import { isLive, type Proposal, type View } from "./view";

export type Decision = "abstain" | "approve" | "deny";

/** The SVM bridge-out record a payout must match. */
export interface Withdrawal {
  id: bigint;
  destination: string;
  amountMilli: bigint; // NET payout (gross minus the bridge fee)
  symbol: string; // "STEEM" or "SBD"
  createdAt: bigint; // SVM height
  status: string; // "REQUESTED", "PROCESSED", "REFUNDED"
}

export interface SVM {
  height(): Promise<bigint>;
  withdrawalTimeoutBlocks(): Promise<bigint>;
  requestedWithdrawals(): Promise<Withdrawal[]>;
  withdrawal(id: bigint): Promise<Withdrawal | undefined>;
  validatorStakes(height: bigint): Promise<StakeEntry[]>;
  selfSteemAccount(): Promise<string>;
}

/** Every validator should run identical values for the rules that must agree
 * across signers (threshold, tolerance, snapshot spacing and age). */
export interface PolicyConfig {
  bankAccount: string;
  thresholdPercent: number;
  reweightTolerance: number;
  payoutMarginBlocks: bigint;
  snapshotEvery: bigint;
  snapshotMaxAge: bigint;
  snapshotLag: bigint;
  ownerCooldownMs: number;
  fallbackDelayMs: number;
  maxPerTxMilli: Record<string, bigint>;
}

export function defaultPolicyConfig(): PolicyConfig {
  return {
    bankAccount: "svm.bank", thresholdPercent: 75, reweightTolerance: 50, payoutMarginBlocks: 14400n,
    snapshotEvery: 1000n, snapshotMaxAge: 3000n, snapshotLag: 10n, ownerCooldownMs: 3600_000, fallbackDelayMs: 120_000,
    maxPerTxMilli: {},
  };
}

export interface SVMState {
  height: bigint;
  timeout: bigint;
  requested: Map<bigint, Withdrawal>;
}

export interface Intent {
  key: string;
  title: string;
  ops: tx.Operation[];
}

// ASCII digits only, like Go's RE2 \d.
const SNAPSHOT_RE = /@svm:([0-9]+)/;

function singleTransfer(p: Proposal): tx.Transfer | undefined {
  if (p.tx.operations.length !== 1) return undefined;
  const op = p.tx.operations[0];
  return op.kind === "transfer" ? op : undefined;
}

function symbolOf(amount: string): string {
  try {
    return tx.parseAsset(amount).symbol;
  } catch {
    return "";
  }
}

const capOf = (cfg: PolicyConfig, sym: string): bigint =>
  Object.prototype.hasOwnProperty.call(cfg.maxPerTxMilli, sym) ? cfg.maxPerTxMilli[sym] : 0n;

/** Distance (in chain order) from the designated proposer index; -1 when me
 * is not a member. */
export function rankOf(members: string[], me: string, designated: number): number {
  const n = members.length;
  const i = members.indexOf(me);
  return i < 0 ? -1 : (((i - designated) % n) + n) % n;
}

export class Policy {
  private cache = new Map<bigint, tx.Authority>();

  constructor(public cfg: PolicyConfig, public svm: SVM, public me: string, public journal?: ApprovalJournal) {}

  async desiredAt(height: bigint): Promise<tx.Authority> {
    const hit = this.cache.get(height);
    if (hit) return hit;
    const a = apportion(await this.svm.validatorStakes(height), this.cfg.thresholdPercent);
    if (this.cache.size > 16) this.cache.clear();
    this.cache.set(height, a);
    return a;
  }

  checkpoint(tip: bigint): bigint {
    const s = tip - this.cfg.snapshotLag;
    return s <= 0n ? 0n : s - (s % this.cfg.snapshotEvery);
  }

  async review(p: Proposal, v: View, st: SVMState, myVotes: Map<string, Vote>): Promise<[Decision, string]> {
    if (p.tx.operations.length !== 1) return ["deny", "more than one operation"];
    const op = p.tx.operations[0];
    switch (op.kind) {
      case "transfer":
        return this.reviewTransfer(p, op, v, st, myVotes);
      case "account_update":
        return this.reviewAccountUpdate(p, op, v, st, myVotes);
      case "transfer_to_vesting":
      case "withdraw_vesting":
      case "change_recovery_account":
        return ["abstain", "treasury/recovery operation: needs a human"];
      default:
        return ["deny", "operation type not allowed: " + op.kind];
    }
  }

  /** [withdrawal id, "approve" if a correct payout / "deny" if wrong forever /
   * "abstain" if it can't be judged yet, reason]. */
  async payoutFacts(t: tx.Transfer, v: View, st: SVMState): Promise<[bigint | undefined, Decision, string]> {
    if (t.from !== this.cfg.bankAccount) return [undefined, "deny", "not from " + this.cfg.bankAccount];
    const id = parseWithdrawalMemo(t.memo);
    if (id === undefined) return [undefined, "deny", 'memo is not exactly "svm-withdrawal <id>"'];
    if (v.history.isPaid(id)) return [id, "deny", "withdrawal already paid on Steem"];
    let w = st.requested.get(id);
    if (!w) {
      let got: Withdrawal | undefined;
      try {
        got = await this.svm.withdrawal(id);
      } catch (err) {
        return [id, "abstain", `cannot read withdrawal: ${err instanceof Error ? err.message : String(err)}`];
      }
      if (!got) return [id, "abstain", "SVM withdrawal not found (yet)"];
      if (got.status !== "REQUESTED") return [id, "deny", "SVM withdrawal is " + got.status];
      w = got;
    }
    if (t.to !== w.destination) return [id, "deny", "recipient differs from the SVM withdrawal destination"];
    if (t.amount !== tx.formatMilli(w.amountMilli, w.symbol)) return [id, "deny", "amount differs from the SVM withdrawal net amount"];
    if (st.height + this.cfg.payoutMarginBlocks > w.createdAt + st.timeout) return [id, "deny", "too close to the automatic SVM refund"];
    return [id, "approve", ""];
  }

  private async reviewTransfer(p: Proposal, t: tx.Transfer, v: View, st: SVMState, myVotes: Map<string, Vote>): Promise<[Decision, string]> {
    const [id, d, reason] = await this.payoutFacts(t, v, st);
    if (d !== "approve") return [d, reason];
    const cap = capOf(this.cfg, symbolOf(t.amount));
    if (cap > 0n && tx.parseAsset(t.amount).amount > cap) return ["abstain", "over the configured per-transaction cap"];
    // One payout per withdrawal: never sign a second proposal for the same
    // withdrawal while one this signer approved can still execute, and approve
    // only the earliest correct live one.
    if (journalConflicts(this.journal, subjectOf(p.tx), p.id, v.nowMs)) {
      return ["abstain", "already approved another unexpired proposal for this withdrawal"];
    }
    let earliest = "";
    for (const q of v.proposals) { // sorted by (created, id)
      const qt = singleTransfer(q);
      if (!qt || qt.memo !== t.memo) continue;
      // Not just live ones: a "rejected" proposal can still execute if a
      // member flips a deny to an approval before it expires.
      if (q.id !== p.id && q.status !== "expired") {
        const mv = myVotes.get(q.id);
        if (mv && mv.a === 1) return ["abstain", "already approved another unexpired proposal for this withdrawal"];
      }
      if (!earliest && isLive(q) && (await this.payoutFacts(qt, v, st))[1] === "approve") earliest = q.id;
    }
    if (earliest !== p.id) return ["abstain", `an earlier correct proposal exists for withdrawal ${id}`];
    return ["approve", `pays SVM withdrawal ${id} exactly`];
  }

  private async reviewAccountUpdate(p: Proposal, u: tx.AccountUpdate, v: View, st: SVMState, myVotes: Map<string, Vote>): Promise<[Decision, string]> {
    if (u.account !== this.cfg.bankAccount) return ["deny", "not " + this.cfg.bankAccount];
    if (!u.owner || !u.active || !u.posting) return ["deny", "must set owner, active and posting together"];
    const m = SNAPSHOT_RE.exec(p.title);
    if (!m) return ["deny", "title has no @svm:<height> snapshot"];
    const s = BigInt(m[1]);
    if (s <= 0n || s >= 1n << 63n || s % this.cfg.snapshotEvery !== 0n || st.height - s > this.cfg.snapshotMaxAge) {
      return ["deny", "snapshot is not a recent checkpoint"];
    }
    if (s > st.height) return ["abstain", "this signer's SVM node has not reached the snapshot yet"];
    let desired: tx.Authority;
    try {
      desired = await this.desiredAt(s);
    } catch (err) {
      return ["abstain", `cannot compute the stake-proportional set: ${err instanceof Error ? err.message : String(err)}`];
    }
    if (!u.owner.equal(desired) || !u.active.equal(desired) || !u.posting.equal(desired)) {
      return ["deny", `authority differs from the stake-proportional set at @svm:${s}`];
    }
    if (u.memoKey !== v.bank.memoKey) return ["deny", "memo key changed"];
    if (u.jsonMetadata && u.jsonMetadata !== v.bank.jsonMetadata) return ["deny", "json_metadata changed"];
    if (!exceedsTolerance(v.bank.owner, desired, this.cfg.reweightTolerance)) return ["deny", "change is within the reweight tolerance"];
    if (journalConflicts(this.journal, subjectOf(p.tx), p.id, v.nowMs)) {
      return ["abstain", "already approved another unexpired validator-set proposal"];
    }
    for (const q of v.proposals) {
      if (q.id === p.id || q.status === "expired" || q.status === "executed") continue;
      if (q.tx.operations[0].kind === "account_update") {
        const mv = myVotes.get(q.id);
        if (mv && mv.a === 1) return ["abstain", "already approved another unexpired validator-set proposal"];
      }
    }
    return ["approve", `equals the stake-proportional set at @svm:${s}`];
  }

  /** The payouts and validator-set change to propose now, plus an error (the
   * payouts are still returned when only the validator-set part failed).
   * firstSeen tracks (locally) when each became due, so non-designated
   * members wait rank * fallbackDelay before stepping in. */
  async intents(v: View, st: SVMState, firstSeen: Map<string, number>, nowMs: number): Promise<[Intent[], string | undefined]> {
    const members = v.sortedMembers();
    const n = members.length;
    if (n === 0 || rankOf(members, this.me, 0) < 0) return [[], undefined];
    const due = (key: string, designated: number): boolean => {
      if (!firstSeen.has(key)) firstSeen.set(key, nowMs);
      const rank = rankOf(members, this.me, designated);
      return rank === 0 || nowMs - firstSeen.get(key)! >= rank * this.cfg.fallbackDelayMs;
    };

    const live = new Set<string>();
    let liveUpdate = false;
    for (const q of v.proposals) {
      if (!isLive(q)) continue;
      const qt = singleTransfer(q);
      if (qt) {
        live.add(qt.memo);
        const id = parseWithdrawalMemo(qt.memo);
        if (id !== undefined) firstSeen.delete(`w:${id}`); // restart the fallback clock while live
      }
      if (q.tx.operations.length === 1 && q.tx.operations[0].kind === "account_update") liveUpdate = true;
    }

    const balance = new Map<string, bigint>();
    for (const s of [v.bank.balance, v.bank.sbdBalance]) {
      try {
        const a = tx.parseAsset(s);
        balance.set(a.symbol, a.amount);
      } catch {
        // unknown balance: nothing payable in it
      }
    }

    const out: Intent[] = [];
    for (const id of [...st.requested.keys()].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0))) {
      const w = st.requested.get(id)!;
      const memo = withdrawalMemo(id);
      if (live.has(memo) || v.history.isPaid(id) || journalConflicts(this.journal, "transfer:" + memo, "", v.nowMs)) continue;
      if (st.height + this.cfg.payoutMarginBlocks > w.createdAt + st.timeout) continue; // the automatic SVM refund will handle it
      const cap = capOf(this.cfg, w.symbol);
      if (cap > 0n && w.amountMilli > cap) continue;
      if ((balance.get(w.symbol) ?? 0n) < w.amountMilli) continue;
      const key = `w:${id}`;
      if (!due(key, Number(id % BigInt(n)))) continue;
      balance.set(w.symbol, (balance.get(w.symbol) ?? 0n) - w.amountMilli);
      const amount = tx.formatMilli(w.amountMilli, w.symbol);
      out.push({
        key,
        title: `Withdrawal ${id}: ${amount} to @${w.destination}`,
        ops: [{ kind: "transfer", from: this.cfg.bankAccount, to: w.destination, amount, memo }],
      });
    }

    if (!liveUpdate && !journalConflicts(this.journal, "account_update:" + this.cfg.bankAccount, "", v.nowMs)) {
      let cooled = true;
      if (v.history.lastOwnerChange) {
        try {
          if (v.nowMs - tx.parseTime(v.history.lastOwnerChange) < this.cfg.ownerCooldownMs) cooled = false;
        } catch {
          // undatable: treat as cooled, the chain enforces the hour anyway
        }
      }
      const s = this.checkpoint(st.height);
      if (cooled && s > 0n) {
        let desired: tx.Authority;
        try {
          desired = await this.desiredAt(s);
        } catch (err) {
          return [out, `computing the stake-proportional set at @svm:${s}: ${err instanceof Error ? err.message : String(err)}`];
        }
        if (exceedsTolerance(v.bank.owner, desired, this.cfg.reweightTolerance)) {
          const key = `v:${s}`;
          if (due(key, Number((s / this.cfg.snapshotEvery) % BigInt(n)))) {
            out.push({
              key,
              title: `Validator set @svm:${s}`,
              ops: [{ kind: "account_update", account: this.cfg.bankAccount, owner: desired, active: desired, posting: desired, memoKey: v.bank.memoKey, jsonMetadata: "" }],
            });
          }
        }
      }
    }
    return [out, undefined];
  }
}
