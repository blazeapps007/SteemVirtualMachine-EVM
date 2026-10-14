// Shared fixtures for the svm.bank multisig tests (mirrors
// oracle/go/multisig/fixture_test.go and e2e_test.go): a 4-member bank, a
// fake SVM, and a fake Steem node that enforces the chain rules that matter.

import { sha256 } from "@noble/hashes/sha2";

import type { StakeEntry } from "../src/multisig/apportion";
import { Ballot, type StoredProposal } from "../src/multisig/ballot";
import { accountToJSON, HistoryIndex, newAccount, type Account } from "../src/multisig/chain";
import { defaultPolicyConfig, Policy, type SVM, type SVMState, type Withdrawal } from "../src/multisig/policy";
import * as tx from "../src/multisig/steemTx";
import { buildView, type Proposal, type View } from "../src/multisig/view";

export const BANK = "svm.bank";
export const NOW = Math.floor(Date.now() / 1000) * 1000;
export const WEIGHTS: Array<[string, number]> = [["alice", 4500], ["bob", 3000], ["carol", 1500], ["dave", 1000]];
export const MIN = 60_000;

export const key = (name: string) => sha256(new TextEncoder().encode(`svm.bank test key ${name}`));
export const pub = (name: string) => tx.publicKeyOf(key(name));
export function wif(name: string): string {
  const payload = tx.concat(Uint8Array.of(0x80), key(name));
  return tx.b58encode(tx.concat(payload, sha256(sha256(payload)).slice(0, 4)));
}
export const keyAuthority = (p: string) => new tx.Authority(1, [], [[p, 1]]);
export const bankAuthority = (weights = WEIGHTS) => new tx.Authority(7500, weights.map(([n, w]) => [n, w] as [string, number]), []).sorted();
export const stakes = (o: Record<string, number>): StakeEntry[] => Object.entries(o).map(([account, t]) => ({ account, tokens: BigInt(t) }));
export const ts = (ms: number) => tx.formatTime(ms);
export const transferOp = (to: string, amount: string, memo: string, from = BANK): tx.Operation => ({ kind: "transfer", from, to, amount, memo });
export const withdrawal = (id: number, destination: string, milli: number, symbol: string, createdAt: number, status = "REQUESTED"): Withdrawal =>
  ({ id: BigInt(id), destination, amountMilli: BigInt(milli), symbol, createdAt: BigInt(createdAt), status });

export class FakeSVM implements SVM {
  height_ = 100000n;
  timeout = 50400n;
  all = new Map<bigint, Withdrawal>();
  stakesAt = new Map<bigint, StakeEntry[]>();
  err?: Error;
  constructor(public me = "") {}
  as(me: string): FakeSVMView { return new FakeSVMView(this, me); }
  async height() { return this.height_; }
  async withdrawalTimeoutBlocks() { return this.timeout; }
  async requestedWithdrawals() { return [...this.all.values()].filter((w) => w.status === "REQUESTED"); }
  async withdrawal(id: bigint) {
    if (this.err) throw this.err;
    return this.all.get(id);
  }
  async validatorStakes(h: bigint) {
    const s = this.stakesAt.get(h);
    if (!s) throw new Error("height pruned");
    return s;
  }
  async selfSteemAccount() { return this.me; }
}

/** One validator's view of a shared FakeSVM. */
export class FakeSVMView implements SVM {
  constructor(public data: FakeSVM, public me: string) {}
  height() { return this.data.height(); }
  withdrawalTimeoutBlocks() { return this.data.withdrawalTimeoutBlocks(); }
  requestedWithdrawals() { return this.data.requestedWithdrawals(); }
  withdrawal(id: bigint) { return this.data.withdrawal(id); }
  validatorStakes(h: bigint) { return this.data.validatorStakes(h); }
  async selfSteemAccount() { return this.me; }
}

/** alice 4500, bob 3000, carol 1500, dave 1000 (threshold 7500); SVM at
 * 100000; stakes at the 99000 checkpoint reproduce the current weights. */
export class Fixture {
  bank: Account;
  members = new Map<string, Account>();
  props: tx.DynamicProps = { headBlockNumber: 100000, headBlockId: "000186a0" + "ab".repeat(16), time: ts(NOW) };
  nowMs = NOW;
  svm = new FakeSVM();
  hist = new HistoryIndex();
  ballots = new Map<string, Ballot>();

  constructor() {
    const auth = bankAuthority();
    this.bank = newAccount(BANK, { owner: auth, active: auth.sorted(), posting: auth.sorted(), memoKey: pub("memo"), balance: "1000.000 STEEM", sbdBalance: "50.000 SBD" });
    for (const [n] of WEIGHTS) {
      this.members.set(n, newAccount(n, { active: keyAuthority(pub(n)) }));
      this.ballots.set(n, new Ballot());
    }
    this.svm.stakesAt.set(99000n, stakes({ alice: 45, bob: 30, carol: 15, dave: 10 }));
  }

  withdrawal(id: number, dest: string, milli: number, sym: string, created: number): void {
    this.svm.all.set(BigInt(id), withdrawal(id, dest, milli, sym, created));
  }

  propose(creator: string, title: string, ops: tx.Operation[], ttlMs = 58 * MIN, created = ""): string {
    const t = tx.newTransaction(this.props, ops, ttlMs);
    const id = t.id();
    if (!this.ballots.has(creator)) this.ballots.set(creator, new Ballot());
    this.ballots.get(creator)!.props.set(id, { t: title, tx: t, c: created || this.props.time });
    return id;
  }

  stored(id: string): StoredProposal {
    for (const b of this.ballots.values()) if (b.props.has(id)) return b.props.get(id)!;
    throw new Error("no stored proposal " + id);
  }

  vote(voter: string, id: string, approve: boolean, signer = ""): void {
    const sp = this.stored(id);
    this.ballots.get(voter)!.votes.set(id, approve
      ? { a: 1, s: tx.sign(key(signer || voter), sp.tx.digest()), x: sp.tx.expiration }
      : { a: 0, s: "", x: sp.tx.expiration });
  }

  view(): View {
    return buildView(this.nowMs, this.props, this.bank, this.members, this.ballots, this.hist);
  }

  async state(): Promise<SVMState> {
    return { height: this.svm.height_, timeout: this.svm.timeout, requested: new Map((await this.svm.requestedWithdrawals()).map((w) => [w.id, w])) };
  }

  policy(me: string): Policy {
    return new Policy(defaultPolicyConfig(), this.svm, me);
  }

  find(v: View, id: string): Proposal {
    const p = v.proposals.find((q) => q.id === id);
    if (!p) throw new Error("proposal not in view: " + id);
    return p;
  }

  async review(pol: Policy, id: string, myVotes = new Map<string, any>()) {
    const v = this.view();
    return pol.review(this.find(v, id), v, await this.state(), myVotes);
  }
}

/** Serves the condenser_api calls the signer uses and applies broadcasts
 * under the chain's rules: signature authority (active satisfies posting), no
 * unnecessary signatures, expiry, duplicates, one owner change per hour, and
 * history paging that asserts start >= limit. */
export class FakeSteem implements tx.SteemRpc {
  head = 100000;
  nowMs = NOW;
  accounts = new Map<string, Account>();
  history = new Map<string, any[]>();
  seen = new Set<string>();
  lastOwner = new Map<string, number>();
  transfers: tx.Transfer[] = [];
  rejected: string[] = [];
  starts: number[] = []; // get_account_history start params, in order

  advance(blocks = 1): void {
    this.head += blocks;
    this.nowMs += 3000 * blocks;
  }

  addHistory(account: string, trx: string, timestamp: string, type: string, body: unknown): void {
    if (!this.history.has(account)) this.history.set(account, []);
    this.history.get(account)!.push({ trx_id: trx, timestamp, op: [type, body] });
  }

  async call(method: string, params: any[]): Promise<any> {
    switch (method) {
      case "condenser_api.get_dynamic_global_properties":
        return { head_block_number: this.head, head_block_id: this.head.toString(16).padStart(8, "0") + "cd".repeat(16), time: ts(this.nowMs) };
      case "condenser_api.get_accounts":
        return (params[0] as string[]).filter((n) => this.accounts.has(n)).map((n) => accountToJSON(this.accounts.get(n)!));
      case "condenser_api.get_account_history": {
        const [account, start, limit] = params as [string, number, number];
        this.starts.push(start);
        if (start < limit) throw new tx.ChainError("Assert Exception:args.start >= args.limit: start must be greater than limit");
        const h = this.history.get(account) ?? [];
        if (h.length === 0) return [];
        const top = Math.min(start, h.length - 1);
        const out: any[] = [];
        for (let seq = Math.max(0, top - limit); seq <= top; seq++) out.push([seq, h[seq]]);
        return out;
      }
      case "condenser_api.broadcast_transaction_synchronous":
        try {
          return this.apply(tx.Transaction.fromJSON(params[0]));
        } catch (err) {
          if (!(err instanceof Error && err.message.includes("Duplicate transaction"))) this.rejected.push(String(err));
          throw err;
        }
    }
    throw new Error("unexpected method " + method);
  }

  private satisfies(auth: tx.Authority, keys: Set<string>, used: Set<string>, depth = 0): boolean {
    let total = 0;
    for (const [k, w] of auth.keyAuths) {
      if (keys.has(k)) {
        used.add(k);
        total += w;
        if (total >= auth.weightThreshold) return true;
      }
    }
    if (depth < 2) {
      for (const [name, w] of auth.sorted().accountAuths) {
        const acc = this.accounts.get(name);
        if (acc && this.satisfies(acc.active, keys, used, depth + 1)) {
          total += w;
          if (total >= auth.weightThreshold) return true;
        }
      }
    }
    return total >= auth.weightThreshold;
  }

  apply(t: tx.Transaction): unknown {
    const id = t.id();
    if (this.seen.has(id)) throw new tx.ChainError("Duplicate transaction check failed");
    const exp = tx.parseTime(t.expiration);
    if (!(this.nowMs < exp) || exp - this.nowMs > 3600_000) throw new tx.ChainError("transaction expiration out of range");
    if (t.operations.length !== 1) throw new tx.ChainError("test node applies single-operation transactions only");
    const digest = t.digest();
    const keys = new Set<string>();
    for (const s of t.signatures) {
      const p = tx.recoverSigner(digest, s);
      if (keys.has(p)) throw new tx.ChainError("duplicate signature");
      keys.add(p);
    }
    const used = new Set<string>();
    const op = t.operations[0];
    let acc: Account | undefined;
    let account: string;
    let ok: boolean;
    if (op.kind === "account_update2") {
      account = op.account;
      acc = this.accounts.get(account);
      ok = !!acc && !op.owner && (this.satisfies(acc.posting, keys, used) || this.satisfies(acc.active, keys, used) || this.satisfies(acc.owner, keys, used));
    } else if (op.kind === "transfer") {
      account = op.from;
      acc = this.accounts.get(account);
      ok = !!acc && this.satisfies(acc.active, keys, used);
    } else if (op.kind === "account_update") {
      account = op.account;
      acc = this.accounts.get(account);
      ok = !!acc && this.satisfies(op.owner ? acc.owner : acc.active, keys, used);
    } else {
      throw new tx.ChainError("operation not supported by the test node");
    }
    if (!ok) throw new tx.ChainError("Missing Authority");
    if (used.size !== keys.size) throw new tx.ChainError("Unnecessary signature(s) detected");

    if (op.kind === "account_update2") {
      if (op.postingJsonMetadata) acc!.postingJsonMetadata = op.postingJsonMetadata;
    } else if (op.kind === "transfer") {
      this.move(op.from, op.to, tx.parseAsset(op.amount));
      this.transfers.push(op);
    } else if (op.kind === "account_update") {
      if (op.owner) {
        const last = this.lastOwner.get(account);
        if (last !== undefined && this.nowMs - last < 3600_000) throw new tx.ChainError("Owner authority can only be updated once an hour");
        acc!.owner = op.owner.sorted();
        this.lastOwner.set(account, this.nowMs);
      }
      if (op.active) acc!.active = op.active.sorted();
      if (op.posting) acc!.posting = op.posting.sorted();
      acc!.memoKey = op.memoKey;
    }
    this.seen.add(id);
    this.addHistory(account, id, ts(this.nowMs), op.kind, tx.opToJSON(op)[1]);
    return { id, block_num: this.head + 1 };
  }

  private move(from: string, to: string, amt: tx.Asset): void {
    const field = amt.symbol === "SBD" ? "sbdBalance" : "balance";
    const src = this.accounts.get(from)!;
    const dst = this.accounts.get(to)!;
    const have = tx.parseAsset(src[field]);
    if (have.amount < amt.amount) throw new tx.ChainError("insufficient funds");
    const got = tx.parseAsset(dst[field]);
    src[field] = tx.formatMilli(have.amount - amt.amount, amt.symbol);
    dst[field] = tx.formatMilli(got.amount + amt.amount, amt.symbol);
  }
}

export const quietLog = { info: () => {}, error: () => {} };
