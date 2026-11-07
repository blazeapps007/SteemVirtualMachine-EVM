// signer.ts — one validator's automated svm.bank signer: each pass loads the
// chain view, votes on live proposals, proposes due payouts / validator-set
// syncs, publishes everything in one account_update2 signed with the ACTIVE
// key, then executes proposals that reached the threshold. Port of
// oracle/go/multisig/signer.go.

import { spawn, type ChildProcess } from "node:child_process";
import fs from "node:fs";
import path from "node:path";

import { Ballot, encodeMetadata, parseMetadata, type StoredProposal, type Vote } from "./ballot";
import { broadcast, getAccounts, getDynamicProps, HistoryIndex } from "./chain";
import { ApprovalJournal } from "./journal";
import { defaultPolicyConfig, Policy, rankOf, type Decision, type PolicyConfig, type SVM, type SVMState } from "./policy";
import * as tx from "./steemTx";
import { selftest } from "./vectors";
import { buildView, isLive, keySatisfies, type View } from "./view";

export interface SignerLogger {
  info(msg: string, meta?: Record<string, unknown>): void;
  error(msg: string, meta?: Record<string, unknown>): void;
}

/** Only the validator's ACTIVE key belongs on the server: svm.bank's
 * owner/active/posting authorities resolve through validators' active keys,
 * so an owner key, posting key or master password is never needed. */
export const FORBIDDEN_KEY_VARS = [
  "ORACLE_STEEM_OWNER_WIF", "ORACLE_STEEM_OWNER", "ORACLE_STEEM_POSTING_WIF",
  "ORACLE_STEEM_MASTER_PASSWORD", "ORACLE_STEEM_PASSWORD", "OWNER", "POSTING", "MASTER_PASSWORD",
];

export const LOCK_FILE = "svm_bank_signer.lock";

export class SignerConfigError extends Error {}

export function checkForbiddenKeyVars(env: NodeJS.ProcessEnv): void {
  for (const k of FORBIDDEN_KEY_VARS) {
    if (env[k]) {
      throw new SignerConfigError(
        `refusing to start: ${k} is set — only ORACLE_STEEM_ACTIVE_WIF may be configured (an owner key, posting key or master password is never needed and must not be on the server)`,
      );
    }
  }
}

export interface SignerConfig {
  policy: PolicyConfig;
  activeWif: string;
  nodes: string[];
  intervalMs: number;
  stateDir: string;
  propose: boolean;
  execute: boolean;
  dryRun: boolean;
  rankDelayMs: number; // between members executing the same ready proposal
}

export function defaultSignerConfig(): SignerConfig {
  return {
    policy: defaultPolicyConfig(), activeWif: "", nodes: [], intervalMs: 10_000, stateDir: "/oracle-data",
    propose: true, execute: true, dryRun: false, rankDelayMs: 4_000,
  };
}

/** An exclusive flock on the state dir's lock file — the same lock the Go and
 * Python signers take — held by a flock(1) child that exits with this
 * process, so only one signer writes this validator's metadata. */
async function lockStateDir(stateDir: string, log: SignerLogger): Promise<ChildProcess | undefined> {
  fs.mkdirSync(stateDir, { recursive: true, mode: 0o700 });
  const file = path.join(stateDir, LOCK_FILE);
  fs.closeSync(fs.openSync(file, "a"));
  const holder = spawn("flock", ["-x", "-n", file, "sh", "-c", `while kill -0 ${process.pid} 2>/dev/null; do sleep 2; done`], {
    stdio: "ignore",
  });
  const outcome = await new Promise<"held" | "busy" | "unavailable">((resolve) => {
    holder.once("error", () => resolve("unavailable"));
    holder.once("exit", (code) => resolve(code === 1 ? "busy" : "unavailable"));
    setTimeout(() => resolve("held"), 300);
  });
  if (outcome === "busy") throw new SignerConfigError(`one signer per validator: another signer already holds ${file}`);
  if (outcome === "unavailable") {
    log.error("svm.bank signer: flock(1) unavailable, running without the one-signer lock", { file });
    return undefined;
  }
  holder.unref();
  return holder;
}

const sleep = (ms: number, signal?: AbortSignal) =>
  new Promise<void>((resolve) => {
    const t = setTimeout(resolve, ms);
    signal?.addEventListener("abort", () => {
      clearTimeout(t);
      resolve();
    }, { once: true });
  });

export class Signer {
  readonly key: Uint8Array;
  readonly pub: string;
  readonly rpc: tx.SteemRpc;
  me = "";
  policy?: Policy;
  hist?: HistoryIndex;
  journal?: ApprovalJournal;
  firstSeen = new Map<string, number>();
  readySince = new Map<string, number>();
  logged = new Map<string, Decision>();
  private lock?: ChildProcess;
  private locked = false;

  constructor(public cfg: SignerConfig, public svm: SVM, private readonly log: SignerLogger,
    env: NodeJS.ProcessEnv = process.env, rpc?: tx.SteemRpc) {
    checkForbiddenKeyVars(env);
    try {
      selftest();
    } catch (err) {
      throw new SignerConfigError(`refusing to start: ${err instanceof Error ? err.message : String(err)}`);
    }
    if (!cfg.activeWif) throw new SignerConfigError("ORACLE_STEEM_ACTIVE_WIF is required when the multisig signer is enabled");
    try {
      this.key = tx.decodeWif(cfg.activeWif);
    } catch (err) {
      throw new SignerConfigError(`ORACLE_STEEM_ACTIVE_WIF: ${err instanceof Error ? err.message : String(err)}`);
    }
    if (!rpc && cfg.nodes.length === 0) throw new SignerConfigError("no Steem RPC nodes configured (ORACLE_STEEM_RPCS)");
    if (cfg.intervalMs <= 0) cfg.intervalMs = 10_000;
    if (cfg.rankDelayMs <= 0) cfg.rankDelayMs = 4_000;
    this.rpc = rpc ?? new tx.RPC(cfg.nodes);
    this.pub = tx.publicKeyOf(this.key);
  }

  /** Resolves this validator's Steem account and verifies the key. */
  async init(): Promise<void> {
    if (!this.locked) {
      this.lock = await lockStateDir(this.cfg.stateDir, this.log);
      this.locked = true;
    }
    const me = await this.svm.selfSteemAccount();
    const accts = await getAccounts(this.rpc, [me]);
    if (accts.length !== 1) throw new SignerConfigError(`steem account ${me} not found`);
    if (accts[0].owner.keyAuths.some(([k]) => k === this.pub)) {
      throw new SignerConfigError(`refusing to run: ORACLE_STEEM_ACTIVE_WIF is an OWNER key of @${me} — use the active key`);
    }
    if (!keySatisfies(accts[0].active, this.pub)) {
      throw new SignerConfigError(`ORACLE_STEEM_ACTIVE_WIF (${this.pub}) does not satisfy @${me}'s active authority`);
    }
    this.hist = HistoryIndex.load(this.cfg.stateDir);
    this.journal = ApprovalJournal.load(this.cfg.stateDir);
    this.me = me;
    this.policy = new Policy(this.cfg.policy, this.svm, me, this.journal);
    this.log.info("svm.bank signer ready", {
      validator: me, key: this.pub, dry_run: this.cfg.dryRun, propose: this.cfg.propose, execute: this.cfg.execute,
    });
  }

  close(): void {
    this.lock?.kill();
    this.lock = undefined;
    this.locked = false;
  }

  /** Initializes (retrying every 30s) and runs a pass every interval until aborted. */
  async run(signal: AbortSignal): Promise<void> {
    try {
      while (!signal.aborted) {
        try {
          await this.init();
          break;
        } catch (err) {
          this.log.error("svm.bank signer: init failed, retrying", { err: String(err instanceof Error ? err.message : err) });
          await sleep(30_000, signal);
        }
      }
      while (!signal.aborted) {
        try {
          await this.pass();
        } catch (err) {
          this.log.error("svm.bank signer: pass failed", { err: String(err instanceof Error ? err.message : err) });
        }
        await sleep(this.cfg.intervalMs, signal);
      }
    } finally {
      this.close();
    }
  }

  async pass(): Promise<void> {
    const props = await getDynamicProps(this.rpc);
    const nowMs = tx.parseTime(props.time);
    const bankName = this.cfg.policy.bankAccount;
    await this.hist!.sync(this.rpc, bankName, props.headBlockNumber, nowMs);
    try {
      this.hist!.save(this.cfg.stateDir);
    } catch (err) {
      this.log.error("svm.bank signer: saving history index", { err: String(err) });
    }

    const accts = await getAccounts(this.rpc, [bankName]);
    if (accts.length !== 1) throw new Error(`loading ${bankName}`);
    const bank = accts[0];
    const names = bank.owner.sorted().accountAuths.map(([n]) => n);
    const members = new Map<string, (typeof accts)[0]>();
    const ballots = new Map<string, Ballot>();
    if (names.length > 0) {
      for (const a of await getAccounts(this.rpc, names)) {
        members.set(a.name, a);
        try {
          ballots.set(a.name, parseMetadata(a.postingJsonMetadata).ballot);
        } catch {
          // unreadable metadata: this member's ballot is ignored
        }
      }
    }
    const view = buildView(nowMs, props, bank, members, ballots, this.hist!);

    const requested = new Map((await this.svm.requestedWithdrawals()).map((w) => [w.id, w] as const));
    const st: SVMState = { height: await this.svm.height(), timeout: await this.svm.withdrawalTimeoutBlocks(), requested };

    const mine = ballots.get(this.me) ?? new Ballot();
    if (view.isMember(this.me)) {
      try {
        await this.voteAndPropose(view, st, props, nowMs, mine);
      } catch (err) {
        this.log.error("svm.bank signer: vote/propose", { err: String(err instanceof Error ? err.message : err) });
      }
    }
    if (this.cfg.execute) await this.execute(view, st, mine);
  }

  private logDecision(key: string, d: Decision, meta: Record<string, unknown>): void {
    if (this.logged.get(key) === d) return;
    this.logged.set(key, d);
    this.log.info(`svm.bank: ${d}`, { key, dry_run: this.cfg.dryRun, ...meta });
  }

  private async voteAndPropose(view: View, st: SVMState, props: tx.DynamicProps, nowMs: number, mine: Ballot): Promise<void> {
    const myVotes = mine.votes;
    const newVotes = new Map<string, Vote>();
    const newProps = new Map<string, StoredProposal>();
    // A working copy so approvals cast earlier in this pass count for the
    // "one live approval per withdrawal" rule later in the same pass.
    const working = new Map(myVotes);

    for (const p of view.proposals) {
      if (!isLive(p)) continue;
      const mv = myVotes.get(p.id);
      if (mv && (mv.a === 0 || p.votes.get(this.me)?.valid)) continue; // already voted (re-vote only if our approval is invalid)
      const [d, reason] = await this.policy!.review(p, view, st, working);
      this.logDecision(p.id, d, { title: p.title, reason });
      if (d === "approve") {
        const v: Vote = { a: 1, s: tx.sign(this.key, p.digest), x: p.tx.expiration };
        newVotes.set(p.id, v);
        working.set(p.id, v);
        if (!this.cfg.dryRun) this.journal!.record(p.id, p.tx);
      } else if (d === "deny") {
        const v: Vote = { a: 0, s: "", x: p.tx.expiration };
        newVotes.set(p.id, v);
        working.set(p.id, v);
      }
    }

    if (this.cfg.propose) {
      const [intents, err] = await this.policy!.intents(view, st, this.firstSeen, Date.now());
      if (err) this.log.error("svm.bank: computing intents", { err });
      for (const intent of intents) {
        const t = tx.newTransaction(props, intent.ops, 58 * 60_000);
        const id = t.id();
        newProps.set(id, { t: intent.title, tx: t, c: props.time });
        const v: Vote = { a: 1, s: tx.sign(this.key, t.digest()), x: t.expiration };
        newVotes.set(id, v);
        working.set(id, v);
        if (this.cfg.dryRun) {
          // Each pass builds a fresh transaction (new ID), so log by intent.
          this.logDecision("intent " + intent.key, "approve", { title: intent.title });
          continue;
        }
        this.journal!.record(id, t);
        this.log.info("svm.bank: propose", { id, title: intent.title });
      }
    }
    this.pruneLocal(nowMs);

    if ((newVotes.size === 0 && newProps.size === 0) || this.cfg.dryRun) return;
    // Write-ahead: the journal must hold every approval before any signature
    // leaves this process. If it can't be saved, nothing is published.
    try {
      this.journal!.save(this.cfg.stateDir);
    } catch (err) {
      throw new Error(`saving the approval journal (nothing published): ${String(err)}`);
    }
    await this.publish(props, nowMs, newVotes, newProps);
  }

  /** Read-modify-write of our own svm_ms in one account_update2, signed with
   * the active key (active authority satisfies posting). */
  private async publish(props: tx.DynamicProps, nowMs: number, votes: Map<string, Vote>, newProps: Map<string, StoredProposal>): Promise<void> {
    const accts = await getAccounts(this.rpc, [this.me]);
    if (accts.length !== 1) throw new Error(`reloading @${this.me}`);
    let parsed: ReturnType<typeof parseMetadata>;
    try {
      parsed = parseMetadata(accts[0].postingJsonMetadata);
    } catch (err) {
      throw new Error(`refusing to overwrite @${this.me}'s posting_json_metadata: ${String(err)}`);
    }
    const { raw, ballot } = parsed;
    for (const [k, v] of votes) ballot.votes.set(k, v);
    for (const [k, p] of newProps) ballot.props.set(k, p);
    ballot.prune(nowMs);
    const t = tx.newTransaction(props, [{
      kind: "account_update2", account: this.me, memoKey: "", jsonMetadata: "", postingJsonMetadata: encodeMetadata(raw, ballot),
    }], 5 * 60_000);
    t.signatures = [tx.sign(this.key, t.digest())];
    await broadcast(this.rpc, t);
    this.log.info("svm.bank: published", { votes: votes.size, proposals: newProps.size });
  }

  /** Broadcasts ready proposals this signer's own policy still approves, with
   * only the minimal signature set. The creator goes first; others wait by
   * rank (a duplicate broadcast is harmless). */
  private async execute(view: View, st: SVMState, mine: Ballot): Promise<void> {
    const members = view.sortedMembers();
    for (const p of view.proposals) {
      if (p.status !== "ready") {
        this.readySince.delete(p.id);
        continue;
      }
      if (!this.readySince.has(p.id)) this.readySince.set(p.id, Date.now());
      if (p.expirationMs - 30_000 <= Date.now()) continue;
      let delay = 0;
      if (p.creator !== this.me) {
        let rank = rankOf(members, this.me, 0);
        if (rank < 0) rank = members.length;
        delay = this.cfg.rankDelayMs * (rank + 1);
      }
      if (Date.now() - this.readySince.get(p.id)! < delay) continue;
      const votes = new Map([...mine.votes].filter(([k]) => k !== p.id));
      const [d, reason] = await this.policy!.review(p, view, st, votes);
      if (d !== "approve") {
        this.logDecision("exec " + p.id, d, { title: p.title, reason: "not executing ready proposal: " + reason });
        continue;
      }
      const sigs = view.minimalSignatures(p);
      if (!sigs) continue;
      const t = new tx.Transaction(p.tx.refBlockNum, p.tx.refBlockPrefix, p.tx.expiration, p.tx.operations, sigs);
      if (this.cfg.dryRun) {
        this.logDecision("exec " + p.id, "approve", { title: p.title, would_execute_with_signatures: sigs.length });
        continue;
      }
      try {
        const dup = await broadcast(this.rpc, t);
        this.log.info(dup ? "svm.bank: already executed" : "svm.bank: executed", { id: p.id, title: p.title, signatures: sigs.length });
      } catch (err) {
        this.log.error("svm.bank: execute failed", { id: p.id, title: p.title, err: String(err instanceof Error ? err.message : err) });
      }
    }
  }

  private pruneLocal(nowMs: number): void {
    if (this.logged.size > 2000) this.logged.clear();
    if (this.firstSeen.size > 1000) this.firstSeen.clear();
    this.journal?.prune(nowMs);
  }
}

// ---------------------------------------------------------------------------
// environment (shared by the Go, Python and JS oracles; oracle/PROTOCOL.md)
// ---------------------------------------------------------------------------

const TRUE = new Set(["1", "t", "T", "TRUE", "true", "True"]);
const FALSE = new Set(["0", "f", "F", "FALSE", "false", "False"]);

export function envBool(env: NodeJS.ProcessEnv, key: string, def: boolean): boolean {
  const v = (env[key] || "").trim();
  if (!v) return def;
  if (TRUE.has(v)) return true;
  if (FALSE.has(v)) return false;
  throw new SignerConfigError(`${key} must be true or false`);
}

function parseDurationMs(key: string, v: string): number {
  const m = /^([0-9]+(?:\.[0-9]+)?)(ms|s|m|h)$/.exec(v);
  if (!m) throw new SignerConfigError(`${key} is not a valid duration (e.g. "10s")`);
  return Number(m[1]) * { ms: 1, s: 1_000, m: 60_000, h: 3_600_000 }[m[2] as "ms" | "s" | "m" | "h"];
}

/** undefined when ORACLE_MULTISIG_ENABLED is not true. */
export function buildSignerConfig(env: NodeJS.ProcessEnv, steemRpc: string, stateDir: string): SignerConfig | undefined {
  if (!envBool(env, "ORACLE_MULTISIG_ENABLED", false)) return undefined;
  const cfg = defaultSignerConfig();
  cfg.activeWif = (env.ORACLE_STEEM_ACTIVE_WIF || "").trim();
  cfg.stateDir = stateDir;
  cfg.nodes = ((env.ORACLE_STEEM_RPCS || "").trim() || steemRpc).split(",").map((n) => n.trim()).filter(Boolean);
  const uint = (key: string): number | undefined => {
    const v = (env[key] || "").trim();
    if (!v) return undefined;
    if (!/^[0-9]+$/.test(v)) throw new SignerConfigError(`${key} must be an unsigned integer`);
    return Number(v);
  };
  const interval = (env.ORACLE_MULTISIG_INTERVAL || "").trim();
  if (interval) cfg.intervalMs = parseDurationMs("ORACLE_MULTISIG_INTERVAL", interval);
  const pct = uint("ORACLE_MULTISIG_THRESHOLD_PERCENT");
  if (pct !== undefined) {
    if (pct < 1 || pct > 100) throw new SignerConfigError("ORACLE_MULTISIG_THRESHOLD_PERCENT must be 1..100");
    cfg.policy.thresholdPercent = pct;
  }
  const tol = uint("ORACLE_MULTISIG_REWEIGHT_TOLERANCE");
  if (tol !== undefined) {
    if (tol > 10000) throw new SignerConfigError("ORACLE_MULTISIG_REWEIGHT_TOLERANCE must be 0..10000");
    cfg.policy.reweightTolerance = tol;
  }
  const margin = (env.ORACLE_MULTISIG_PAYOUT_MARGIN_BLOCKS || "").trim();
  if (margin) {
    if (!/^[0-9]+$/.test(margin)) throw new SignerConfigError("ORACLE_MULTISIG_PAYOUT_MARGIN_BLOCKS must be an unsigned integer");
    cfg.policy.payoutMarginBlocks = BigInt(margin);
  }
  cfg.propose = envBool(env, "ORACLE_MULTISIG_PROPOSE", true);
  cfg.execute = envBool(env, "ORACLE_MULTISIG_EXECUTE", true);
  cfg.dryRun = envBool(env, "ORACLE_MULTISIG_DRY_RUN", false);
  for (const sym of ["STEEM", "SBD"]) {
    const v = (env[`ORACLE_MULTISIG_MAX_PER_TX_${sym}`] || "").trim();
    if (!v) continue;
    try {
      cfg.policy.maxPerTxMilli[sym] = tx.parseAsset(`${v} ${sym}`).amount;
    } catch (err) {
      throw new SignerConfigError(`ORACLE_MULTISIG_MAX_PER_TX_${sym}: ${err instanceof Error ? err.message : String(err)}`);
    }
  }
  return cfg;
}
