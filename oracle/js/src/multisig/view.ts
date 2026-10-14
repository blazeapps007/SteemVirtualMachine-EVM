// view.ts — verified proposals and tallies, derived identically by every
// client (oracle/multisigguide.md §2.5). Port of oracle/go/multisig/view.go.

import { Ballot, byteOrder } from "./ballot";
import { Account, HistoryIndex } from "./chain";
import * as tx from "./steemTx";

export type Status = "open" | "ready" | "rejected" | "expired" | "executed";

export interface VoteInfo {
  approve: boolean;
  sig: string;
  valid: boolean; // approvals: recovers to a key that satisfies the voter's active authority
}

export interface Proposal {
  id: string;
  creator: string;
  title: string;
  created: string;
  tx: tx.Transaction;
  digest: Uint8Array;
  expirationMs: number;
  owner: boolean; // needs svm.bank's owner authority (else active)
  votes: Map<string, VoteInfo>;
  approve: number;
  deny: number;
  status: Status;
}

export const isLive = (p: Proposal) => p.status === "open" || p.status === "ready";

export class View {
  proposals: Proposal[] = []; // sorted by (created, id)
  constructor(
    public nowMs: number,
    public props: tx.DynamicProps | undefined,
    public bank: Account,
    public members: Map<string, Account>,
    public history: HistoryIndex,
  ) {}

  authority(p: Proposal): tx.Authority {
    return p.owner ? this.bank.owner : this.bank.active;
  }

  isMember(name: string): boolean {
    return this.bank.owner.weightOf(name) > 0;
  }

  sortedMembers(): string[] {
    return this.bank.owner.sorted().accountAuths.map(([n]) => n);
  }

  /** Valid approval signatures in chain order until the threshold; Steem
   * rejects unnecessary signatures, so it stops exactly there. */
  minimalSignatures(p: Proposal): string[] | undefined {
    const auth = this.authority(p).sorted();
    const sigs: string[] = [];
    let w = 0;
    for (const [name, weight] of auth.accountAuths) {
      if (w >= auth.weightThreshold) break;
      const vi = p.votes.get(name);
      if (vi && vi.approve && vi.valid) {
        sigs.push(vi.sig);
        w += weight;
      }
    }
    return w >= auth.weightThreshold ? sigs : undefined;
  }
}

export function needsOwner(t: tx.Transaction): boolean {
  return t.operations.some(
    (op) => ((op.kind === "account_update" || op.kind === "account_update2") && op.owner !== undefined) || op.kind === "change_recovery_account",
  );
}

export function targetsBank(t: tx.Transaction, bank: string): boolean {
  if (t.operations.length === 0) return false;
  return t.operations.every((op) => {
    switch (op.kind) {
      case "transfer":
      case "transfer_to_vesting":
        return op.from === bank;
      case "withdraw_vesting":
      case "account_update":
      case "account_update2":
        return op.account === bank;
      case "change_recovery_account":
        return op.accountToRecover === bank;
    }
  });
}

/** Whether a single key meets an active authority. */
export function keySatisfies(active: tx.Authority, pub: string): boolean {
  return active.keyAuths.some(([k, w]) => k === pub && w >= active.weightThreshold);
}

/** Verifies and tallies every proposal published by current members. */
export function buildView(
  nowMs: number,
  props: tx.DynamicProps | undefined,
  bank: Account,
  members: Map<string, Account>,
  ballots: Map<string, Ballot>,
  hist: HistoryIndex,
): View {
  const v = new View(nowMs, props, bank, members, hist);
  const byId = new Map<string, Proposal>();
  const creators = [...ballots.keys()].sort(byteOrder);
  for (const creator of creators) {
    if (!v.isMember(creator)) continue; // only proposals from svm.bank's current owner authority
    for (const [id, sp] of ballots.get(creator)!.props) {
      let digest: Uint8Array;
      let expirationMs: number;
      try {
        if (sp.tx.id() !== id) continue; // never trust an ID that doesn't match the transaction
        if (!targetsBank(sp.tx, bank.name)) continue;
        digest = sp.tx.digest();
        expirationMs = tx.parseTime(sp.tx.expiration);
      } catch {
        continue; // unserializable: unverifiable
      }
      const p: Proposal = {
        id, creator, title: sp.t, created: sp.c, tx: sp.tx, digest, expirationMs, owner: needsOwner(sp.tx),
        votes: new Map(), approve: 0, deny: 0, status: "open",
      };
      const prev = byId.get(id);
      if (prev && (byteOrder(prev.created, p.created) < 0 || (prev.created === p.created && byteOrder(prev.creator, p.creator) < 0))) {
        continue; // the same transaction published twice: keep the earliest
      }
      byId.set(id, p);
    }
  }

  for (const p of byId.values()) {
    const auth = v.authority(p);
    for (const voter of creators) {
      const w = auth.weightOf(voter);
      if (w === 0) continue;
      const vote = ballots.get(voter)!.votes.get(p.id);
      if (!vote) continue;
      const vi: VoteInfo = { approve: vote.a === 1, sig: vote.s, valid: false };
      if (vi.approve) {
        let pub = "";
        try {
          pub = tx.recoverSigner(p.digest, vote.s);
        } catch {
          pub = "";
        }
        const acct = members.get(voter);
        if (pub && acct && keySatisfies(acct.active, pub)) {
          vi.valid = true;
          p.approve += w;
        }
      } else {
        vi.valid = true;
        p.deny += w;
      }
      p.votes.set(voter, vi);
    }
    const total = auth.total();
    if (hist && hist.executed[p.id]) p.status = "executed";
    else if (!(nowMs < p.expirationMs)) p.status = "expired";
    else if (total >= auth.weightThreshold && p.deny > total - auth.weightThreshold) p.status = "rejected";
    else if (p.approve >= auth.weightThreshold) p.status = "ready";
    else p.status = "open";
    v.proposals.push(p);
  }
  v.proposals.sort((a, b) => byteOrder(a.created, b.created) || byteOrder(a.id, b.id));
  return v;
}
