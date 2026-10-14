// chain.ts — Steem reads/broadcasts for the signer, the withdrawal memo
// format, and the persisted svm.bank history index. Port of
// oracle/go/multisig/chain.go; the history file is shared with the Go and
// Python clients (same state volume, same JSON).

import fs from "node:fs";
import path from "node:path";

import * as tx from "./steemTx";

export const HISTORY_FILE = "svm_bank_history.json";
const KEEP_EXECUTED_MS = 96 * 3600_000; // proposals live at most an hour
const ZERO_TX_ID = "0".repeat(40);
const U64_MAX = (1n << 64n) - 1n;

export interface Account {
  name: string;
  owner: tx.Authority;
  active: tx.Authority;
  posting: tx.Authority;
  memoKey: string;
  jsonMetadata: string;
  postingJsonMetadata: string;
  balance: string;
  sbdBalance: string;
}

export function newAccount(name: string, fields: Partial<Account> = {}): Account {
  return {
    name, owner: new tx.Authority(), active: new tx.Authority(), posting: new tx.Authority(), memoKey: "",
    jsonMetadata: "", postingJsonMetadata: "", balance: "", sbdBalance: "", ...fields,
  };
}

export function accountFromJSON(o: any): Account {
  const auth = (k: string) => (o[k] == null ? new tx.Authority() : tx.Authority.fromJSON(o[k]));
  return {
    name: String(o.name ?? ""), owner: auth("owner"), active: auth("active"), posting: auth("posting"),
    memoKey: String(o.memo_key ?? ""), jsonMetadata: String(o.json_metadata ?? ""),
    postingJsonMetadata: String(o.posting_json_metadata ?? ""), balance: String(o.balance ?? ""),
    sbdBalance: String(o.sbd_balance ?? ""),
  };
}

export function accountToJSON(a: Account): Record<string, unknown> {
  return {
    name: a.name, owner: a.owner.toJSON(), active: a.active.toJSON(), posting: a.posting.toJSON(), memo_key: a.memoKey,
    json_metadata: a.jsonMetadata, posting_json_metadata: a.postingJsonMetadata, balance: a.balance, sbd_balance: a.sbdBalance,
  };
}

export async function getAccounts(rpc: tx.SteemRpc, names: string[]): Promise<Account[]> {
  return ((await rpc.call("condenser_api.get_accounts", [names])) ?? []).map(accountFromJSON);
}

export async function getDynamicProps(rpc: tx.SteemRpc): Promise<tx.DynamicProps> {
  return tx.dynamicPropsFromJSON(await rpc.call("condenser_api.get_dynamic_global_properties", []));
}

/** Broadcasts synchronously; true when already on chain (a "Duplicate
 * transaction" rejection is not an error). */
export async function broadcast(rpc: tx.SteemRpc, t: tx.Transaction): Promise<boolean> {
  try {
    await rpc.call("condenser_api.broadcast_transaction_synchronous", [t.toJSON()]);
    return false;
  } catch (err) {
    if (err instanceof tx.ChainError && err.message.includes("Duplicate transaction")) return true;
    throw err;
  }
}

/** The exact memo a payout must carry: the format the bridge relays parse
 * (parseWithdrawalMemo in steemClient.ts and its Go/Python twins). */
export function withdrawalMemo(id: bigint): string {
  return `svm-withdrawal ${id}`;
}

/** Mirrors the bridge relays' lenient parse: anything the relay would attest
 * as a payout of <id> also counts as "already paid" here. */
export function paidMemoId(memo: string): bigint | undefined {
  const f = memo.trim().split(/\s+/);
  if (f.length !== 2 || f[0] !== "svm-withdrawal" || !/^[0-9]+$/.test(f[1])) return undefined;
  const id = BigInt(f[1]);
  return id <= U64_MAX ? id : undefined;
}

/** Only the canonical "svm-withdrawal <id>" form (what the signer approves). */
export function parseWithdrawalMemo(memo: string): bigint | undefined {
  const m = /^svm-withdrawal (0|[1-9][0-9]*)$/.exec(memo);
  if (!m) return undefined;
  const id = BigInt(m[1]);
  return id <= U64_MAX ? id : undefined;
}

export function atomicWrite(file: string, obj: unknown): void {
  const tmp = file + ".tmp";
  const fd = fs.openSync(tmp, "w", 0o600);
  try {
    fs.writeSync(fd, JSON.stringify(obj));
    fs.fsyncSync(fd);
  } finally {
    fs.closeSync(fd);
  }
  fs.renameSync(tmp, file);
}

/** A persisted, rebuildable index of svm.bank's account history: paid
 * withdrawals, executed trx IDs, the last owner change. A cache of public
 * chain data — deleting the file just triggers a full rescan. */
export class HistoryIndex {
  nextSeq = 0;
  paid: Record<string, string> = {};
  executed: Record<string, string> = {};
  lastOwnerChange = "";

  static load(stateDir: string): HistoryIndex {
    const h = new HistoryIndex();
    try {
      const d = JSON.parse(fs.readFileSync(path.join(stateDir, HISTORY_FILE), "utf8"));
      h.nextSeq = Number(d.next_seq ?? 0);
      h.paid = { ...(d.paid ?? {}) };
      h.executed = { ...(d.executed ?? {}) };
      h.lastOwnerChange = String(d.last_owner_change ?? "");
      if (!Number.isSafeInteger(h.nextSeq) || h.nextSeq < 0) return new HistoryIndex();
    } catch {
      return new HistoryIndex(); // absent or corrupt: rebuilt, not trusted
    }
    return h;
  }

  save(stateDir: string): void {
    atomicWrite(path.join(stateDir, HISTORY_FILE), {
      next_seq: this.nextSeq, paid: this.paid, executed: this.executed, last_owner_change: this.lastOwnerChange,
    });
  }

  isPaid(id: bigint): boolean {
    return Object.prototype.hasOwnProperty.call(this.paid, id.toString());
  }

  /** Applies the entries at or after nextSeq; reports progress. */
  private applyPage(account: string, page: Array<[number, any]>): boolean {
    let advanced = false;
    for (const [seqRaw, entry] of page) {
      const seq = Number(seqRaw);
      if (seq < this.nextSeq) continue;
      this.apply(account, entry);
      advanced = true;
      this.nextSeq = Math.max(this.nextSeq, seq + 1);
    }
    return advanced;
  }

  /** Brings the index up to the newest entry. The node returns sequences
   * [start-limit, start] and asserts start >= limit. The newest window is
   * requested with the head block number as start (clamped to the newest
   * entry; a start that changes every block can't be answered from a stale
   * cache — multisigguide.md §4.1). A gap behind it is paged forward with
   * start = nextSeq+100, i.e. [nextSeq, nextSeq+100] — immutable history. */
  async sync(rpc: tx.SteemRpc, account: string, headBlock: number, nowMs: number): Promise<void> {
    const call = async (start: number): Promise<Array<[number, any]>> =>
      (await rpc.call("condenser_api.get_account_history", [account, start, 100])) ?? [];
    const newest = await call(Math.max(headBlock, this.nextSeq + 100));
    if (newest.length > 0) {
      const oldest = Math.min(...newest.map(([s]) => Number(s)));
      while (this.nextSeq < oldest) {
        if (!this.applyPage(account, await call(this.nextSeq + 100))) break;
      }
      this.applyPage(account, newest);
    }
    this.pruneExecuted(nowMs);
  }

  apply(account: string, e: any): void {
    const trx = String(e?.trx_id ?? "");
    if (trx && trx !== ZERO_TX_ID) this.executed[trx] = String(e.timestamp ?? "");
    const op = e?.op;
    if (!Array.isArray(op) || op.length !== 2 || op[1] === null || typeof op[1] !== "object") return;
    const [type, body] = op;
    if (type === "transfer") {
      if (body.from === account && typeof body.memo === "string") {
        const id = paidMemoId(body.memo);
        if (id !== undefined) this.paid[id.toString()] = trx;
      }
    } else if (type === "account_update" || type === "account_update2") {
      if (body.account === account && body.owner != null) this.lastOwnerChange = String(e.timestamp ?? "");
    }
  }

  pruneExecuted(nowMs: number): void {
    const cut = nowMs - KEEP_EXECUTED_MS;
    for (const [trx, ts] of Object.entries(this.executed)) {
      try {
        if (tx.parseTime(ts) < cut) delete this.executed[trx];
      } catch {
        // keep entries we can't date
      }
    }
  }
}
