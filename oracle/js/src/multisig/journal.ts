// journal.ts — this signer's local, write-ahead record of every approval it
// has signed. Saved before an approval is published and consulted by every
// review, so "at most one live approval per withdrawal" holds even when a
// lagging Steem node serves stale metadata. Port of
// oracle/go/multisig/journal.go; the file is shared with the Go and Python clients.

import fs from "node:fs";
import path from "node:path";

import { PRUNE_AFTER_MS } from "./ballot";
import { atomicWrite } from "./chain";
import * as tx from "./steemTx";

export const JOURNAL_FILE = "svm_bank_approvals.json";

/** What a single-operation proposal acts on: two proposals with the same
 * subject must never both be approved while either can still execute. */
export function subjectOf(t: tx.Transaction): string {
  if (t.operations.length !== 1) return "";
  const op = t.operations[0];
  if (op.kind === "transfer") return "transfer:" + op.memo;
  if (op.kind === "account_update") return "account_update:" + op.account;
  return "";
}

export interface JournalEntry {
  subject: string;
  expiration: string;
}

export class ApprovalJournal {
  constructor(public approvals: Record<string, JournalEntry> = {}) {}

  /** Not rebuildable from the chain, so a corrupt file stops the signer. */
  static load(stateDir: string): ApprovalJournal {
    const file = path.join(stateDir, JOURNAL_FILE);
    let text: string;
    try {
      text = fs.readFileSync(file, "utf8");
    } catch (err: any) {
      if (err?.code === "ENOENT") return new ApprovalJournal();
      throw err;
    }
    let d: any;
    try {
      d = JSON.parse(text);
    } catch (err) {
      throw new Error(`${file} is corrupt (it is safe to delete once the signer has been stopped for over an hour): ${String(err)}`);
    }
    const a = d?.approvals ?? {};
    if (a === null || typeof a !== "object" || Array.isArray(a)) throw new Error(`${file} is corrupt`);
    return new ApprovalJournal({ ...a });
  }

  save(stateDir: string): void {
    atomicWrite(path.join(stateDir, JOURNAL_FILE), { approvals: this.approvals });
  }

  record(id: string, t: tx.Transaction): void {
    this.approvals[id] = { subject: subjectOf(t), expiration: t.expiration };
  }

  /** Whether a different proposal with the same subject was approved and can
   * still execute at Steem head time now (unparseable expiration = conflict). */
  conflicts(subject: string, id: string, nowMs: number): boolean {
    if (!subject) return false;
    for (const [other, e] of Object.entries(this.approvals)) {
      if (other === id || e?.subject !== subject) continue;
      try {
        if (nowMs < tx.parseTime(e.expiration)) return true;
      } catch {
        return true;
      }
    }
    return false;
  }

  prune(nowMs: number): void {
    const cut = nowMs - PRUNE_AFTER_MS;
    for (const [id, e] of Object.entries(this.approvals)) {
      try {
        if (tx.parseTime(e.expiration) < cut) delete this.approvals[id];
      } catch {
        // keep entries we can't date
      }
    }
  }
}

export function journalConflicts(j: ApprovalJournal | undefined, subject: string, id: string, nowMs: number): boolean {
  return j !== undefined && j.conflicts(subject, id, nowMs);
}
