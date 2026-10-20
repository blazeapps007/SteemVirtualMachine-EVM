// steemTx.ts — Steem transaction core for the svm.bank multisig signer:
// JSON-RPC with node failover, the chain's binary serialization, transaction
// digest and ID, WIF/STM key handling, and canonical recoverable secp256k1
// signatures. A field-for-field port of oracle/go/steemtx, pinned
// byte-for-byte by oracle/multisig/vectors.json (selftest in vectors.ts).
// Amounts and IDs are BigInt — never floating point.

import { secp256k1 } from "@noble/curves/secp256k1";
import { ripemd160 } from "@noble/hashes/legacy";
import { sha256 } from "@noble/hashes/sha2";

export const CHAIN_ID = new Uint8Array(32); // Steem mainnet: 32 zero bytes

export class SteemTxError extends Error {}
export class UnsupportedOperation extends SteemTxError {}
/** A rejection from the Steem chain itself; never retried on another node. */
export class ChainError extends Error {}

// ---------------------------------------------------------------------------
// bytes
// ---------------------------------------------------------------------------

export function hex(b: Uint8Array): string {
  return Buffer.from(b).toString("hex");
}

export function fromHex(s: string): Uint8Array {
  if (typeof s !== "string" || s.length % 2 !== 0 || !/^[0-9a-fA-F]*$/.test(s)) throw new SteemTxError("bad hex");
  return new Uint8Array(Buffer.from(s, "hex"));
}

export function concat(...parts: Uint8Array[]): Uint8Array {
  return new Uint8Array(Buffer.concat(parts.map((p) => Buffer.from(p))));
}

function equalBytes(a: Uint8Array, b: Uint8Array): boolean {
  return a.length === b.length && a.every((x, i) => x === b[i]);
}

// ---------------------------------------------------------------------------
// JSON-RPC
// ---------------------------------------------------------------------------

const TRANSIENT = /upstream|circuit breaker|timeout|unavailable|too many requests|bad cast/i;

export interface SteemRpc {
  call(method: string, params: unknown[]): Promise<any>;
}

/** Calls Steem nodes in order, moving to the next only on transport errors. */
export class RPC implements SteemRpc {
  readonly nodes: string[];
  constructor(nodes: string[], private readonly timeoutMs = 20_000) {
    this.nodes = nodes.map((n) => n.trim()).filter(Boolean);
  }

  async call(method: string, params: unknown[]): Promise<any> {
    if (this.nodes.length === 0) throw new Error("steemtx: no Steem RPC nodes configured");
    const body = JSON.stringify({ jsonrpc: "2.0", id: 1, method, params });
    let last: unknown;
    for (const node of this.nodes) {
      let data: any;
      try {
        const resp = await fetch(node, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body,
          signal: AbortSignal.timeout(this.timeoutMs),
        });
        data = await resp.json();
      } catch (err) {
        last = err; // transport error or non-JSON reply: try the next node
        continue;
      }
      if (data && data.error != null) {
        const message = String(data.error?.message ?? data.error);
        if (TRANSIENT.test(message)) {
          last = new Error(`${node}: ${message}`);
          continue;
        }
        throw new ChainError(message);
      }
      return data?.result;
    }
    throw new Error(`steem rpc: every node failed: ${last instanceof Error ? last.message : String(last)}`);
  }
}

// ---------------------------------------------------------------------------
// time
// ---------------------------------------------------------------------------

const TIME_RE = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})$/;

/** Parses Steem's UTC timestamp exactly (Go's 2006-01-02T15:04:05) to ms. */
export function parseTime(s: string): number {
  const m = typeof s === "string" ? TIME_RE.exec(s) : null;
  if (!m) throw new SteemTxError(`bad time ${JSON.stringify(s)}`);
  const [y, mo, d, h, mi, se] = m.slice(1).map(Number);
  const t = Date.UTC(y, mo - 1, d, h, mi, se);
  const back = new Date(t);
  if (back.getUTCMonth() !== mo - 1 || back.getUTCDate() !== d || h > 23 || mi > 59 || se > 59) {
    throw new SteemTxError(`bad time ${JSON.stringify(s)}`);
  }
  return t;
}

export function formatTime(ms: number): string {
  return new Date(ms).toISOString().slice(0, 19);
}

// ---------------------------------------------------------------------------
// writer
// ---------------------------------------------------------------------------

function utf8(s: string): Uint8Array {
  // TextEncoder turns lone surrogates into U+FFFD, as Go's JSON decoder does.
  return new TextEncoder().encode(s);
}

/** Steem's compact little-endian binary encoding. */
export class Writer {
  private readonly parts: number[] = [];

  bytes(): Uint8Array {
    return Uint8Array.from(this.parts);
  }

  raw(b: Uint8Array): void {
    for (const x of b) this.parts.push(x);
  }

  private uint(v: number | bigint, size: number): void {
    let n = BigInt(v);
    if (n < 0n || n >= 1n << BigInt(8 * size)) throw new SteemTxError(`uint${8 * size} out of range: ${v}`);
    for (let i = 0; i < size; i++) {
      this.parts.push(Number(n & 0xffn));
      n >>= 8n;
    }
  }

  u8(v: number): void { this.uint(v, 1); }
  u16(v: number): void { this.uint(v, 2); }
  u32(v: number): void { this.uint(v, 4); }

  i64(v: bigint): void {
    if (v < -(1n << 63n) || v >= 1n << 63n) throw new SteemTxError(`int64 out of range: ${v}`);
    this.uint(BigInt.asUintN(64, v), 8);
  }

  varint(v: number): void {
    if (!Number.isInteger(v) || v < 0 || v > 0xffffffff) throw new SteemTxError(`varint out of range: ${v}`);
    while (v >= 0x80) {
      this.parts.push((v & 0x7f) | 0x80);
      v = Math.floor(v / 128);
    }
    this.parts.push(v);
  }

  string(s: string): void {
    const b = utf8(s);
    this.varint(b.length);
    this.raw(b);
  }

  time(s: string): void {
    this.u32(parseTime(s) / 1000);
  }

  asset(a: Asset): void {
    this.i64(a.amount);
    this.u8(a.precision);
    const sym = new Uint8Array(7);
    sym.set(new TextEncoder().encode(a.symbol).slice(0, 7));
    this.raw(sym);
  }

  authority(a: Authority): void {
    this.u32(a.weightThreshold);
    this.varint(a.accountAuths.length);
    for (const [name, weight] of a.accountAuths) {
      this.string(name);
      this.u16(weight);
    }
    this.varint(a.keyAuths.length);
    for (const [key, weight] of a.keyAuths) {
      this.raw(decodePublicKey(key));
      this.u16(weight);
    }
  }

  optAuthority(a: Authority | undefined): void {
    if (a === undefined) {
      this.u8(0);
    } else {
      this.u8(1);
      this.authority(a);
    }
  }
}

// ---------------------------------------------------------------------------
// assets
// ---------------------------------------------------------------------------

const PRECISIONS: Record<string, number> = { STEEM: 3, SBD: 3, VESTS: 6 };

export interface Asset {
  amount: bigint;
  precision: number;
  symbol: string;
}

/** Parses "12.500 SBD" exactly: exactly the symbol's decimals, non-negative. */
export function parseAsset(s: string): Asset {
  if (typeof s !== "string") throw new SteemTxError(`bad asset ${JSON.stringify(s)}`);
  const f = s.trim().split(/\s+/);
  if (f.length !== 2) throw new SteemTxError(`bad asset ${JSON.stringify(s)}`);
  const prec = Object.prototype.hasOwnProperty.call(PRECISIONS, f[1]) ? PRECISIONS[f[1]] : undefined;
  if (prec === undefined) throw new SteemTxError(`unknown symbol in ${JSON.stringify(s)}`);
  const m = /^([0-9]+)\.([0-9]+)$/.exec(f[0]);
  if (!m || m[2].length !== prec) {
    throw new SteemTxError(`${JSON.stringify(s)} needs a non-negative amount with exactly ${prec} decimals`);
  }
  const n = BigInt(m[1] + m[2]);
  if (n >= 1n << 63n) throw new SteemTxError(`${JSON.stringify(s)} out of range`);
  return { amount: n, precision: prec, symbol: f[1] };
}

export function assetString(a: Asset): string {
  const p = 10n ** BigInt(a.precision);
  return `${a.amount / p}.${(a.amount % p).toString().padStart(a.precision, "0")} ${a.symbol}`;
}

/** 12500n, "SBD" -> "12.500 SBD". */
export function formatMilli(milli: bigint, symbol: string): string {
  return `${milli / 1000n}.${(milli % 1000n).toString().padStart(3, "0")} ${symbol}`;
}

// ---------------------------------------------------------------------------
// authorities
// ---------------------------------------------------------------------------

function byteCompare(a: string, b: string): number {
  return Buffer.compare(Buffer.from(a, "utf8"), Buffer.from(b, "utf8"));
}

export class Authority {
  constructor(
    public weightThreshold = 0,
    public accountAuths: Array<[string, number]> = [],
    public keyAuths: Array<[string, number]> = [],
  ) {}

  /** account_auths in chain order (byte order of the name); key_auths as given. */
  sorted(): Authority {
    return new Authority(this.weightThreshold, [...this.accountAuths].sort((a, b) => byteCompare(a[0], b[0])), [...this.keyAuths]);
  }

  total(): number {
    return [...this.accountAuths, ...this.keyAuths].reduce((s, [, w]) => s + w, 0);
  }

  weightOf(name: string): number {
    const e = this.accountAuths.find(([n]) => n === name);
    return e ? e[1] : 0;
  }

  serialize(): Uint8Array {
    const w = new Writer();
    w.authority(this);
    return w.bytes();
  }

  /** Whether the two serialize identically. */
  equal(other: Authority): boolean {
    try {
      return equalBytes(this.serialize(), other.serialize());
    } catch {
      return false;
    }
  }

  toJSON(): unknown {
    return {
      weight_threshold: this.weightThreshold,
      account_auths: this.accountAuths.map(([n, w]) => [n, w]),
      key_auths: this.keyAuths.map(([k, w]) => [k, w]),
    };
  }

  static fromJSON(obj: any): Authority {
    if (obj === null || typeof obj !== "object" || Array.isArray(obj)) throw new SteemTxError("authority must be an object");
    const pairs = (key: string): Array<[string, number]> => {
      const raw = obj[key];
      if (raw == null) return [];
      if (!Array.isArray(raw)) throw new SteemTxError(`${key} must be a list`);
      return raw.map((p: any) => {
        if (!Array.isArray(p) || p.length !== 2 || typeof p[0] !== "string") throw new SteemTxError(`bad ${key} entry`);
        return [p[0], uint(p[1], 16)] as [string, number];
      });
    };
    return new Authority(uint(obj.weight_threshold ?? 0, 32), pairs("account_auths"), pairs("key_auths"));
  }
}

function uint(v: any, bits: number): number {
  if (typeof v !== "number" || !Number.isInteger(v) || v < 0 || v >= 2 ** bits) {
    throw new SteemTxError(`expected a uint${bits}, got ${JSON.stringify(v)}`);
  }
  return v;
}

function str(obj: any, key: string): string {
  const v = obj[key];
  if (v == null) return "";
  if (typeof v !== "string") throw new SteemTxError(`${key} must be a string`);
  return v;
}

function optAuth(obj: any, key: string): Authority | undefined {
  return obj[key] == null ? undefined : Authority.fromJSON(obj[key]);
}

// ---------------------------------------------------------------------------
// operations
// ---------------------------------------------------------------------------

export interface Transfer { kind: "transfer"; from: string; to: string; amount: string; memo: string }
export interface TransferToVesting { kind: "transfer_to_vesting"; from: string; to: string; amount: string }
export interface WithdrawVesting { kind: "withdraw_vesting"; account: string; vestingShares: string }
export interface AccountUpdate {
  kind: "account_update"; account: string; owner?: Authority; active?: Authority; posting?: Authority;
  memoKey: string; jsonMetadata: string;
}
export interface ChangeRecoveryAccount { kind: "change_recovery_account"; accountToRecover: string; newRecoveryAccount: string }
export interface AccountUpdate2 {
  kind: "account_update2"; account: string; owner?: Authority; active?: Authority; posting?: Authority;
  memoKey: string; jsonMetadata: string; postingJsonMetadata: string;
}
export type Operation = Transfer | TransferToVesting | WithdrawVesting | AccountUpdate | ChangeRecoveryAccount | AccountUpdate2;

export function opToJSON(op: Operation): [string, Record<string, unknown>] {
  const auths = (o: { owner?: Authority; active?: Authority; posting?: Authority }) => {
    const out: Record<string, unknown> = {};
    if (o.owner) out.owner = o.owner.toJSON();
    if (o.active) out.active = o.active.toJSON();
    if (o.posting) out.posting = o.posting.toJSON();
    return out;
  };
  switch (op.kind) {
    case "transfer":
      return [op.kind, { from: op.from, to: op.to, amount: op.amount, memo: op.memo }];
    case "transfer_to_vesting":
      return [op.kind, { from: op.from, to: op.to, amount: op.amount }];
    case "withdraw_vesting":
      return [op.kind, { account: op.account, vesting_shares: op.vestingShares }];
    case "account_update":
      return [op.kind, { account: op.account, ...auths(op), memo_key: op.memoKey, json_metadata: op.jsonMetadata }];
    case "change_recovery_account":
      return [op.kind, { account_to_recover: op.accountToRecover, new_recovery_account: op.newRecoveryAccount, extensions: [] }];
    case "account_update2":
      return [op.kind, {
        account: op.account, ...auths(op), ...(op.memoKey ? { memo_key: op.memoKey } : {}),
        json_metadata: op.jsonMetadata, posting_json_metadata: op.postingJsonMetadata, extensions: [],
      }];
  }
}

export function opFromJSON(raw: any): Operation {
  if (!Array.isArray(raw) || raw.length !== 2 || typeof raw[0] !== "string") throw new SteemTxError("an operation is [type, {fields}]");
  const t: string = raw[0];
  const b = raw[1] ?? {};
  if (typeof b !== "object" || Array.isArray(b)) throw new SteemTxError("operation fields must be an object");
  switch (t) {
    case "transfer":
      return { kind: t, from: str(b, "from"), to: str(b, "to"), amount: str(b, "amount"), memo: str(b, "memo") };
    case "transfer_to_vesting":
      return { kind: t, from: str(b, "from"), to: str(b, "to"), amount: str(b, "amount") };
    case "withdraw_vesting":
      return { kind: t, account: str(b, "account"), vestingShares: str(b, "vesting_shares") };
    case "account_update":
      return {
        kind: t, account: str(b, "account"), owner: optAuth(b, "owner"), active: optAuth(b, "active"),
        posting: optAuth(b, "posting"), memoKey: str(b, "memo_key"), jsonMetadata: str(b, "json_metadata"),
      };
    case "change_recovery_account":
      return { kind: t, accountToRecover: str(b, "account_to_recover"), newRecoveryAccount: str(b, "new_recovery_account") };
    case "account_update2":
      return {
        kind: t, account: str(b, "account"), owner: optAuth(b, "owner"), active: optAuth(b, "active"),
        posting: optAuth(b, "posting"), memoKey: str(b, "memo_key"), jsonMetadata: str(b, "json_metadata"),
        postingJsonMetadata: str(b, "posting_json_metadata"),
      };
    default:
      throw new UnsupportedOperation(`unsupported operation ${JSON.stringify(t)}`);
  }
}

function writeOp(w: Writer, op: Operation): void {
  switch (op.kind) {
    case "transfer":
      w.varint(2); w.string(op.from); w.string(op.to); w.asset(parseAsset(op.amount)); w.string(op.memo);
      return;
    case "transfer_to_vesting":
      w.varint(3); w.string(op.from); w.string(op.to); w.asset(parseAsset(op.amount));
      return;
    case "withdraw_vesting":
      w.varint(4); w.string(op.account); w.asset(parseAsset(op.vestingShares));
      return;
    case "account_update":
      w.varint(10); w.string(op.account);
      w.optAuthority(op.owner); w.optAuthority(op.active); w.optAuthority(op.posting);
      w.raw(decodePublicKey(op.memoKey)); w.string(op.jsonMetadata);
      return;
    case "change_recovery_account":
      w.varint(26); w.string(op.accountToRecover); w.string(op.newRecoveryAccount); w.varint(0);
      return;
    case "account_update2":
      w.varint(43); w.string(op.account);
      w.optAuthority(op.owner); w.optAuthority(op.active); w.optAuthority(op.posting);
      if (op.memoKey === "") {
        w.u8(0);
      } else {
        w.u8(1);
        w.raw(decodePublicKey(op.memoKey));
      }
      w.string(op.jsonMetadata); w.string(op.postingJsonMetadata); w.varint(0);
      return;
  }
}

// ---------------------------------------------------------------------------
// transactions
// ---------------------------------------------------------------------------

export interface DynamicProps {
  headBlockNumber: number;
  headBlockId: string;
  time: string;
}

export function dynamicPropsFromJSON(o: any): DynamicProps {
  return { headBlockNumber: Number(o.head_block_number), headBlockId: String(o.head_block_id), time: String(o.time) };
}

export class Transaction {
  constructor(
    public refBlockNum = 0,
    public refBlockPrefix = 0,
    public expiration = "",
    public operations: Operation[] = [],
    public signatures: string[] = [],
  ) {}

  /** The transaction without signatures. */
  serialize(): Uint8Array {
    const w = new Writer();
    w.u16(this.refBlockNum);
    w.u32(this.refBlockPrefix);
    w.time(this.expiration);
    w.varint(this.operations.length);
    for (const op of this.operations) writeOp(w, op);
    w.varint(0); // extensions
    return w.bytes();
  }

  /** What validators sign: sha256(chain_id || serialized tx). */
  digest(): Uint8Array {
    return sha256(concat(CHAIN_ID, this.serialize()));
  }

  /** The Steem trx_id and the proposal ID: hex(sha256(serialized)[:20]). */
  id(): string {
    return hex(sha256(this.serialize()).slice(0, 20));
  }

  toJSON(): Record<string, unknown> {
    const out: Record<string, unknown> = {
      ref_block_num: this.refBlockNum,
      ref_block_prefix: this.refBlockPrefix,
      expiration: this.expiration,
      operations: this.operations.map(opToJSON),
      extensions: [],
    };
    if (this.signatures.length > 0) out.signatures = [...this.signatures];
    return out;
  }

  static fromJSON(obj: any): Transaction {
    if (obj === null || typeof obj !== "object" || Array.isArray(obj)) throw new SteemTxError("a transaction must be an object");
    const ops = obj.operations ?? [];
    if (!Array.isArray(ops)) throw new SteemTxError("operations must be a list");
    const sigs = obj.signatures ?? [];
    if (!Array.isArray(sigs) || !sigs.every((s: unknown) => typeof s === "string")) throw new SteemTxError("signatures must be strings");
    return new Transaction(
      uint(obj.ref_block_num ?? 0, 16),
      uint(obj.ref_block_prefix ?? 0, 32),
      str(obj, "expiration"),
      ops.map(opFromJSON),
      [...sigs],
    );
  }
}

/** References the head block and expires ttlMs after the head block time.
 * Proposals use 58 minutes; Steem rejects anything over 60. */
export function newTransaction(props: DynamicProps, ops: Operation[], ttlMs: number): Transaction {
  const id = fromHex(props.headBlockId);
  if (id.length < 8) throw new SteemTxError("head_block_id too short");
  const prefix = Buffer.from(id).readUInt32LE(4);
  return new Transaction(props.headBlockNumber & 0xffff, prefix, formatTime(parseTime(props.time) + ttlMs), ops);
}

// ---------------------------------------------------------------------------
// keys
// ---------------------------------------------------------------------------

const B58 = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz";

export function b58encode(b: Uint8Array): string {
  let n = BigInt("0x" + (hex(b) || "0"));
  let out = "";
  while (n > 0n) {
    out = B58[Number(n % 58n)] + out;
    n /= 58n;
  }
  let pad = 0;
  while (pad < b.length && b[pad] === 0) pad++;
  return "1".repeat(pad) + out;
}

export function b58decode(s: string): Uint8Array {
  let n = 0n;
  for (const c of s) {
    const i = B58.indexOf(c);
    if (i < 0) throw new SteemTxError("invalid base58");
    n = n * 58n + BigInt(i);
  }
  let h = n === 0n ? "" : n.toString(16);
  if (h.length % 2) h = "0" + h;
  let pad = 0;
  while (pad < s.length && s[pad] === "1") pad++;
  return concat(new Uint8Array(pad), fromHex(h));
}

/** base58(0x80 || 32-byte key || first 4 bytes of sha256(sha256(0x80 || key))). */
export function decodeWif(wif: string): Uint8Array {
  const raw = b58decode(wif.trim());
  if (raw.length !== 37 || raw[0] !== 0x80) throw new SteemTxError("not a WIF private key");
  if (!equalBytes(sha256(sha256(raw.slice(0, 33))).slice(0, 4), raw.slice(33))) throw new SteemTxError("bad WIF checksum");
  const key = raw.slice(1, 33);
  if (!secp256k1.utils.isValidPrivateKey(key)) throw new SteemTxError("invalid private key");
  return key;
}

/** "STM" + base58(33-byte key || first 4 bytes of ripemd160(key)) -> 33 bytes. */
export function decodePublicKey(s: string): Uint8Array {
  if (typeof s !== "string" || !s.startsWith("STM")) throw new SteemTxError("public key must start with STM");
  const raw = b58decode(s.slice(3));
  if (raw.length !== 37) throw new SteemTxError("bad public key length");
  if (!equalBytes(ripemd160(raw.slice(0, 33)).slice(0, 4), raw.slice(33))) throw new SteemTxError("bad public key checksum");
  return raw.slice(0, 33);
}

export function encodePublicKey(compressed: Uint8Array): string {
  return "STM" + b58encode(concat(compressed, ripemd160(compressed).slice(0, 4)));
}

export function publicKeyOf(priv: Uint8Array): string {
  return encodePublicKey(secp256k1.getPublicKey(priv, true));
}

// ---------------------------------------------------------------------------
// signatures
// ---------------------------------------------------------------------------

/** Steem's byte rule on r (rs[0:32]) and s (rs[32:64]). */
export function isCanonical(rs: Uint8Array): boolean {
  return !(rs[0] & 0x80) && !(rs[0] === 0 && !(rs[1] & 0x80)) && !(rs[32] & 0x80) && !(rs[32] === 0 && !(rs[33] & 0x80));
}

/** A Steem compact recoverable signature of digest, as hex: header byte
 * (27 + 4 + recovery id, so 0x1f or 0x20) then r then s. Retries with RFC 6979
 * extra entropy until the byte rule passes (about half fail it). */
export function sign(priv: Uint8Array, digest: Uint8Array): string {
  if (digest.length !== 32) throw new Error("digest must be 32 bytes");
  for (let attempt = 0; attempt < 256; attempt++) {
    const ctr = new Uint8Array(4);
    new DataView(ctr.buffer).setUint32(0, attempt, true);
    const extraEntropy = attempt === 0 ? undefined : sha256(concat(digest, ctr));
    const sig = secp256k1.sign(digest, priv, { lowS: true, extraEntropy, prehash: false });
    const rs = sig.toCompactRawBytes();
    if (isCanonical(rs)) return hex(concat(Uint8Array.of(27 + 4 + sig.recovery), rs));
  }
  throw new Error("steemtx: no canonical signature found");
}

/** The STM public key that produced sigHex over digest; non-canonical
 * signatures are rejected, exactly as the chain does. */
export function recoverSigner(digest: Uint8Array, sigHex: string): string {
  let sig: Uint8Array;
  try {
    sig = fromHex(sigHex);
  } catch {
    throw new SteemTxError("signature must be 65 bytes of hex");
  }
  if (sig.length !== 65) throw new SteemTxError("signature must be 65 bytes of hex");
  if (!isCanonical(sig.slice(1))) throw new SteemTxError("signature is not canonical");
  const header = sig[0];
  if (header < 27 || header > 34) throw new SteemTxError("bad signature header");
  try {
    const s = secp256k1.Signature.fromCompact(sig.slice(1)).addRecoveryBit((header - 27) & 3);
    return encodePublicKey(s.recoverPublicKey(digest).toRawBytes(true));
  } catch (err) {
    throw new SteemTxError(`cannot recover signer: ${err instanceof Error ? err.message : String(err)}`);
  }
}
