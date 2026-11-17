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

// The history is complete up to the node's head; a minute of margin covers a
// node a few blocks behind (a later block scan re-reads those blocks).
const HISTORY_HEAD_MARGIN = 20;
/** The most Steem blocks one scanBlocks call reads, and how many it fetches
 * at once (spread round-robin over the nodes). */
export const BLOCK_SCAN_BATCH = 200;
const BLOCK_SCAN_WORKERS = 4;

/** How far back (in 3s Steem blocks) a fresh index rebuilt from blocks
 * starts: far enough to see the payout of any withdrawal still awaiting one
 * (created at most withdrawalTimeout SVM blocks ago, counted at a
 * conservative 10s each), every executed transaction the index keeps
 * (KEEP_EXECUTED_MS) and the owner cooldown, plus an hour. */
export function blockLookback(withdrawalTimeoutSvmBlocks: number): number {
  const seconds = Math.max(withdrawalTimeoutSvmBlocks * 10, KEEP_EXECUTED_MS / 1000) + 3600;
  return Math.floor(seconds / 3);
}

// Every account's history holds at least its creation, so an empty page
// means the node has no account history (steemd.steemworld.org answers []
// instead of an error). Trusting it would hide past payouts.
const noHistory = (page: unknown): string | undefined =>
  Array.isArray(page) && page.length > 0 ? undefined : "empty account history (node has no account_history API)";
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

// A withdrawal payout is one Steem transaction with two operations, in this
// order: the transfer, carrying the memo the user gave when bridging out, and a
// custom_json naming the withdrawal. Only svm.bank's active authority can sign
// that custom_json, so it can't be forged, and the memo means nothing to the
// bridge: a user can put anything there, including "svm-withdrawal 5", without
// affecting any withdrawal.
export const PAYOUT_LINK_ID = "svm-withdrawal";

/** The exact custom_json body naming a withdrawal. */
export function payoutLinkJson(id: bigint): string {
  return `{"withdrawal_id":"${id}"}`;
}

/** The withdrawal a custom_json names, if it is exactly a payout link signed by
 * the bank's active authority alone. */
export function parsePayoutLink(c: tx.CustomJSON, bank: string): bigint | undefined {
  if (c.id !== PAYOUT_LINK_ID || c.requiredAuths.length !== 1 || c.requiredAuths[0] !== bank || c.requiredPostingAuths.length !== 0) return undefined;
  const m = /^\{"withdrawal_id":"(0|[1-9][0-9]*)"\}$/.exec(c.json);
  if (!m) return undefined;
  const id = BigInt(m[1]);
  return id <= U64_MAX ? id : undefined;
}

/** The two operations of a withdrawal payout. */
export function payoutOps(bank: string, to: string, amount: string, memo: string, id: bigint): tx.Operation[] {
  return [
    { kind: "transfer", from: bank, to, amount, memo },
    { kind: "custom_json", requiredAuths: [bank], requiredPostingAuths: [], id: PAYOUT_LINK_ID, json: payoutLinkJson(id) },
  ];
}

/** A transaction's transfer and the withdrawal it pays, if the transaction has
 * exactly the shape of a payout. */
export function payoutOf(t: tx.Transaction, bank: string): [tx.Transfer, bigint] | undefined {
  if (t.operations.length !== 2) return undefined;
  const [first, second] = t.operations;
  if (first.kind !== "transfer" || second.kind !== "custom_json") return undefined;
  const id = parsePayoutLink(second, bank);
  return id === undefined ? undefined : [first, id];
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
  /** Every svm.bank op in Steem blocks <= scannedBlock is applied. */
  scannedBlock = 0;

  static load(stateDir: string): HistoryIndex {
    const h = new HistoryIndex();
    try {
      const d = JSON.parse(fs.readFileSync(path.join(stateDir, HISTORY_FILE), "utf8"));
      h.nextSeq = Number(d.next_seq ?? 0);
      h.paid = { ...(d.paid ?? {}) };
      h.executed = { ...(d.executed ?? {}) };
      h.lastOwnerChange = String(d.last_owner_change ?? "");
      h.scannedBlock = Number(d.scanned_block ?? 0);
      if (!Number.isSafeInteger(h.nextSeq) || h.nextSeq < 0) return new HistoryIndex();
      if (!Number.isSafeInteger(h.scannedBlock) || h.scannedBlock < 0) h.scannedBlock = 0;
    } catch {
      return new HistoryIndex(); // absent or corrupt: rebuilt, not trusted
    }
    return h;
  }

  save(stateDir: string): void {
    atomicWrite(path.join(stateDir, HISTORY_FILE), {
      next_seq: this.nextSeq, paid: this.paid, executed: this.executed, last_owner_change: this.lastOwnerChange,
      scanned_block: this.scannedBlock,
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
      await rpc.call("condenser_api.get_account_history", [account, start, 100], noHistory);
    const newest = await call(Math.max(headBlock, this.nextSeq + 100));
    if (newest.length > 0) {
      const oldest = Math.min(...newest.map(([s]) => Number(s)));
      while (this.nextSeq < oldest) {
        if (!this.applyPage(account, await call(this.nextSeq + 100))) break;
      }
      this.applyPage(account, newest);
    }
    this.pruneExecuted(nowMs);
    if (headBlock > HISTORY_HEAD_MARGIN) this.scannedBlock = Math.max(this.scannedBlock, headBlock - HISTORY_HEAD_MARGIN);
  }

  /** The fallback for when no node serves svm.bank's account history
   * (multisigguide.md §4.1): reads raw blocks from scannedBlock+1 (or from
   * headBlock-lookback when the index is fresh or older than that), at most
   * BLOCK_SCAN_BATCH per call, and applies every operation that names
   * account — the same entries get_account_history would list. Returns
   * whether the index now covers headBlock; until it does, the signer must
   * not act on the index. */
  async scanBlocks(rpc: tx.SteemRpc, account: string, headBlock: number, lookback: number, nowMs: number): Promise<boolean> {
    let start = this.scannedBlock + 1;
    if (headBlock > lookback && start < headBlock - lookback) start = headBlock - lookback;
    if (start > headBlock) return true;
    const end = Math.min(headBlock, start + BLOCK_SCAN_BATCH - 1);
    const nums = Array.from({ length: end - start + 1 }, (_, i) => start + i);
    let nextIdx = 0;
    const fetchOne = (num: number) =>
      rpc.call("condenser_api.get_block", [num], (b) => (b ? undefined : `node has no block ${num}`));
    // BLOCK_SCAN_WORKERS fetches in flight at once; results are applied in order.
    const settled: Array<{ ok: boolean; value: any }> = new Array(nums.length);
    await Promise.all(Array.from({ length: Math.min(BLOCK_SCAN_WORKERS, nums.length) }, async () => {
      while (nextIdx < nums.length) {
        const i = nextIdx++;
        try {
          settled[i] = { ok: true, value: await fetchOne(nums[i]) };
        } catch (err) {
          settled[i] = { ok: false, value: err };
        }
      }
    }));
    let error: unknown;
    for (let i = 0; i < nums.length; i++) {
      if (!settled[i].ok) {
        error = settled[i].value; // a gap stops the scan; the rest is fetched next time
        break;
      }
      this.applyBlock(account, settled[i].value);
      this.scannedBlock = nums[i];
    }
    this.pruneExecuted(nowMs);
    if (error !== undefined && this.scannedBlock < start) throw error;
    return this.scannedBlock >= headBlock;
  }

  /** Applies every operation in block whose body names account (a quoted
   * "svm.bank" anywhere: transfer from/to, custom_json auths,
   * account_update). A false match is harmless: apply only records payout
   * links signed by account and owner changes of account. */
  applyBlock(account: string, block: any): void {
    const needle = JSON.stringify(account);
    const ids: string[] = Array.isArray(block?.transaction_ids) ? block.transaction_ids : [];
    const txs: any[] = Array.isArray(block?.transactions) ? block.transactions : [];
    txs.forEach((t, i) => {
      const trx = String(t?.transaction_id || ids[i] || "");
      for (const op of Array.isArray(t?.operations) ? t.operations : []) {
        if (Array.isArray(op) && op.length === 2 && JSON.stringify(op[1]).includes(needle)) {
          this.apply(account, { trx_id: trx, timestamp: block.timestamp ?? "", op });
        }
      }
    });
  }

  apply(account: string, e: any): void {
    const trx = String(e?.trx_id ?? "");
    if (trx && trx !== ZERO_TX_ID) this.executed[trx] = String(e.timestamp ?? "");
    const op = e?.op;
    if (!Array.isArray(op) || op.length !== 2 || op[1] === null || typeof op[1] !== "object") return;
    const [type, body] = op;
    if (type === "custom_json") {
      let value: tx.Operation;
      try {
        value = tx.opFromJSON(op);
      } catch {
        return;
      }
      if (value.kind === "custom_json") {
        const id = parsePayoutLink(value, account);
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
