// apportion.ts — exact stake-proportional apportionment of svm.bank's
// authority weights (oracle/PROTOCOL.md, "svm.bank multisig signer"). Every
// signer must produce a byte-identical authority from the same input, so this
// is BigInt-only. Port of oracle/go/multisig/apportion.go.

import { Authority } from "./steemTx";

export const TOTAL_WEIGHT = 10000;
export const MAX_ACCOUNT_AUTHS = 40;

export interface StakeEntry {
  account: string;
  tokens: bigint; // bonded asteem
}

const byName = (a: string, b: string) => Buffer.compare(Buffer.from(a, "utf8"), Buffer.from(b, "utf8"));

/**
 * 1. drop zero stakes; sort by tokens desc then account asc; keep the top 40;
 * 2. q_i = floor(10000*t_i/T), r_i = (10000*t_i) mod T;
 * 3. give +1 to the (10000 - sum q_i) entries with the largest r_i, ties by
 *    account asc (largest remainder), so the weights sum to exactly 10000;
 * 4. minimum weight 1, taken from the largest weight (ties by account asc);
 * 5. threshold = ceil(10000 * thresholdPercent / 100);
 * 6. account_auths sorted by name; key_auths empty.
 */
export function apportion(entries: StakeEntry[], thresholdPercent: number): Authority {
  if (!Number.isInteger(thresholdPercent) || thresholdPercent < 1 || thresholdPercent > 100) {
    throw new Error(`threshold percent ${thresholdPercent} out of range 1..100`);
  }
  const seen = new Set<string>();
  let vals: StakeEntry[] = [];
  for (const e of entries) {
    if (e.tokens <= 0n) continue;
    if (seen.has(e.account)) throw new Error(`duplicate account ${JSON.stringify(e.account)} in stake input`);
    seen.add(e.account);
    vals.push(e);
  }
  if (vals.length === 0) throw new Error("no validators with stake");
  vals.sort((a, b) => (a.tokens !== b.tokens ? (a.tokens > b.tokens ? -1 : 1) : byName(a.account, b.account)));
  vals = vals.slice(0, MAX_ACCOUNT_AUTHS);

  const total = vals.reduce((s, e) => s + e.tokens, 0n);
  const scale = BigInt(TOTAL_WEIGHT);
  const rows = vals.map((e) => ({ account: e.account, weight: Number((scale * e.tokens) / total), rem: (scale * e.tokens) % total }));

  const deficit = TOTAL_WEIGHT - rows.reduce((s, r) => s + r.weight, 0);
  if (deficit < 0 || deficit >= rows.length) throw new Error(`impossible deficit ${deficit} for ${rows.length} validators`);
  const order = [...rows].sort((a, b) => (a.rem !== b.rem ? (a.rem > b.rem ? -1 : 1) : byName(a.account, b.account)));
  for (const r of order.slice(0, deficit)) r.weight++;

  // Minimum weight 1: zero-weight entries in account order, each unit taken
  // from the current largest weight (ties by account asc).
  const named = [...rows].sort((a, b) => byName(a.account, b.account));
  for (const r of named) {
    while (r.weight === 0) {
      r.weight = 1;
      let donor = named[0];
      for (const d of named) if (d.weight > donor.weight) donor = d;
      donor.weight--;
    }
  }

  if (rows.some((r) => r.weight < 1 || r.weight > 65535) || rows.reduce((s, r) => s + r.weight, 0) !== TOTAL_WEIGHT) {
    throw new Error("apportionment produced an invalid weight set");
  }
  return new Authority(
    Math.floor((TOTAL_WEIGHT * thresholdPercent + 99) / 100),
    rows.map((r) => [r.account, r.weight] as [string, number]),
    [],
  ).sorted();
}

/** Whether desired differs from current enough to warrant a validator-set
 * proposal: membership or threshold changed, or a weight moved by more than
 * tolerance points. */
export function exceedsTolerance(current: Authority, desired: Authority, tolerance: number): boolean {
  if (current.weightThreshold !== desired.weightThreshold || current.accountAuths.length !== desired.accountAuths.length) {
    return true;
  }
  for (const [name, weight] of desired.accountAuths) {
    const c = current.weightOf(name);
    if (c === 0 || Math.abs(c - weight) > tolerance) return true;
  }
  return false;
}
