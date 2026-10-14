// ballot.ts — the svm_ms object inside a validator's posting_json_metadata:
// its proposals and votes (oracle/multisigguide.md §2.1). Port of
// oracle/go/multisig/ballot.go. Other top-level keys are kept as their exact
// raw JSON text, so publishing a vote never reformats anything else there.

import * as tx from "./steemTx";

export const METADATA_KEY = "svm_ms";
export const PRUNE_AFTER_MS = 72 * 3600_000;
/** Keeps the serialized svm_ms well inside Steem's 64 KiB transaction limit. */
export const MAX_BALLOT_BYTES = 40 * 1024;

/** posting_json_metadata is not a JSON object: never overwrite it. */
export class InvalidMetadata extends Error {}
/** svm_ms exists but is malformed: readers ignore it; a signer never overwrites its own. */
export class MalformedBallot extends Error {}

export interface StoredProposal {
  t: string;
  tx: tx.Transaction;
  c: string;
}

export interface Vote {
  a: number; // 1 approve, 0 deny
  s: string; // signature over the tx digest, approvals only
  x: string; // the tx expiration, used for pruning
}

export const byteOrder = (a: string, b: string) => Buffer.compare(Buffer.from(a, "utf8"), Buffer.from(b, "utf8"));

export class Ballot {
  props = new Map<string, StoredProposal>();
  votes = new Map<string, Vote>();

  toJSON(): unknown {
    const props: Record<string, unknown> = {};
    for (const k of [...this.props.keys()].sort(byteOrder)) {
      const p = this.props.get(k)!;
      props[k] = { t: p.t, tx: p.tx.toJSON(), c: p.c };
    }
    const votes: Record<string, unknown> = {};
    for (const k of [...this.votes.keys()].sort(byteOrder)) {
      const v = this.votes.get(k)!;
      votes[k] = v.s ? { a: v.a, s: v.s, x: v.x } : { a: v.a, x: v.x };
    }
    return { props, votes };
  }

  encodedSize(): number {
    return Buffer.byteLength(JSON.stringify(this.toJSON()), "utf8");
  }

  /** Drops entries that expired more than PRUNE_AFTER before now, then (if
   * still too large) the oldest expired entries until it fits. Live entries
   * are never dropped. */
  prune(nowMs: number): void {
    const cut = nowMs - PRUNE_AFTER_MS;
    for (const [k, p] of [...this.props]) if (before(p.tx.expiration, cut)) this.props.delete(k);
    for (const [k, v] of [...this.votes]) if (before(v.x, cut)) this.votes.delete(k);
    while (this.encodedSize() > MAX_BALLOT_BYTES && this.dropOldest(nowMs)) {
      // keep dropping
    }
  }

  private dropOldest(nowMs: number): boolean {
    let oldest: { t: number; key: string; isProp: boolean } | undefined;
    const consider = (key: string, ts: string, isProp: boolean) => {
      let t: number;
      try {
        t = tx.parseTime(ts);
      } catch {
        return;
      }
      if (!(t < nowMs)) return;
      if (!oldest || t < oldest.t) oldest = { t, key, isProp };
    };
    for (const [k, p] of this.props) consider(k, p.tx.expiration, true);
    for (const [k, v] of this.votes) consider(k, v.x, false);
    if (!oldest) return false;
    if (oldest.isProp) this.props.delete(oldest.key);
    else this.votes.delete(oldest.key);
    return true;
  }
}

function before(ts: string, cut: number): boolean {
  try {
    return tx.parseTime(ts) < cut;
  } catch {
    return false;
  }
}

// ---------------------------------------------------------------------------
// raw JSON object splitting
// ---------------------------------------------------------------------------

const WS = " \t\n\r";

function skipWs(s: string, i: number): number {
  while (i < s.length && WS.includes(s[i])) i++;
  return i;
}

function scanString(s: string, i: number): number {
  for (let j = i + 1; j < s.length; j++) {
    if (s[j] === "\\") j++;
    else if (s[j] === '"') return j + 1;
  }
  throw new InvalidMetadata("unterminated string");
}

function scanValue(s: string, i: number): number {
  const c = s[i];
  if (c === '"') return scanString(s, i);
  if (c === "{" || c === "[") {
    let depth = 0;
    for (let j = i; j < s.length; ) {
      const d = s[j];
      if (d === '"') {
        j = scanString(s, j);
        continue;
      }
      if (d === "{" || d === "[") depth++;
      else if (d === "}" || d === "]") {
        depth--;
        if (depth === 0) return j + 1;
      }
      j++;
    }
    throw new InvalidMetadata("unterminated value");
  }
  let j = i;
  while (j < s.length && !",}] \t\n\r".includes(s[j])) j++;
  return j;
}

/** Splits a JSON object into key -> raw value text, validating all of it. */
function splitObject(s: string): Map<string, string> {
  const out = new Map<string, string>();
  let i = skipWs(s, 0);
  if (s[i] !== "{") throw new InvalidMetadata("posting_json_metadata is not a JSON object");
  i = skipWs(s, i + 1);
  if (s[i] === "}") {
    i++;
  } else {
    for (;;) {
      if (s[i] !== '"') throw new InvalidMetadata("expected a key");
      const kEnd = scanString(s, i);
      let key: string;
      try {
        key = JSON.parse(s.slice(i, kEnd));
      } catch (err) {
        throw new InvalidMetadata(`bad key: ${String(err)}`);
      }
      i = skipWs(s, kEnd);
      if (s[i] !== ":") throw new InvalidMetadata("expected ':'");
      i = skipWs(s, i + 1);
      const vEnd = scanValue(s, i);
      const text = s.slice(i, vEnd);
      try {
        JSON.parse(text);
      } catch (err) {
        throw new InvalidMetadata(`bad value for ${JSON.stringify(key)}: ${String(err)}`);
      }
      out.set(key, text);
      i = skipWs(s, vEnd);
      if (s[i] === ",") {
        i = skipWs(s, i + 1);
        continue;
      }
      if (s[i] === "}") {
        i++;
        break;
      }
      throw new InvalidMetadata("expected ',' or '}'");
    }
  }
  if (skipWs(s, i) !== s.length) throw new InvalidMetadata("trailing data after the JSON object");
  return out;
}

function toVote(o: any): Vote {
  if (o === null) return { a: 0, s: "", x: "" };
  if (typeof o !== "object" || Array.isArray(o)) throw new MalformedBallot("a vote must be an object");
  const a = o.a ?? 0;
  const s = o.s ?? "";
  const x = o.x ?? "";
  if (typeof a !== "number" || !Number.isInteger(a)) throw new MalformedBallot("vote.a must be an integer");
  if (typeof s !== "string" || typeof x !== "string") throw new MalformedBallot("vote.s and vote.x must be strings");
  return { a, s, x };
}

/** Splits posting_json_metadata into the untouched raw entries and the parsed
 * svm_ms ballot. An empty string is an empty object. Throws InvalidMetadata
 * (never overwrite) or MalformedBallot (svm_ms unusable). */
export function parseMetadata(posting: string): { raw: Map<string, string>; ballot: Ballot } {
  const text = (posting || "").trim();
  const raw = text && text !== "null" ? splitObject(text) : new Map<string, string>();
  const ballot = new Ballot();
  const msText = raw.get(METADATA_KEY);
  if (msText !== undefined && msText !== "null") {
    const ms = JSON.parse(msText);
    if (ms === null || typeof ms !== "object" || Array.isArray(ms)) throw new MalformedBallot("svm_ms must be an object");
    const props = ms.props ?? {};
    const votes = ms.votes ?? {};
    const isObj = (o: unknown) => o !== null && typeof o === "object" && !Array.isArray(o);
    if (!isObj(props) || !isObj(votes)) throw new MalformedBallot("svm_ms.props and svm_ms.votes must be objects");
    for (const [k, v] of Object.entries(votes)) ballot.votes.set(k, toVote(v));
    for (const [k, p] of Object.entries<any>(props)) {
      try {
        if (!isObj(p) || typeof (p.t ?? "") !== "string" || typeof (p.c ?? "") !== "string") continue;
        ballot.props.set(k, { t: p.t ?? "", tx: tx.Transaction.fromJSON(p.tx), c: p.c ?? "" });
      } catch {
        // unsupported operations or a bad shape: unverifiable, skipped
      }
    }
  }
  return { raw, ballot };
}

/** Replaces only svm_ms; other keys keep their exact text; sorted keys. */
export function encodeMetadata(raw: Map<string, string>, ballot: Ballot): string {
  const out = new Map(raw);
  out.set(METADATA_KEY, JSON.stringify(ballot.toJSON()));
  const keys = [...out.keys()].sort(byteOrder);
  return "{" + keys.map((k) => `${JSON.stringify(k)}:${out.get(k)!.trim()}`).join(",") + "}";
}
