// vectors.ts — the shared conformance vectors (oracle/multisig/vectors.json)
// and the startup selftest. The signer refuses to start unless every vector
// passes. The Docker image ships the file next to the compiled module
// (dist/multisig/vectors.json); a source checkout reads the repo copy.

import fs from "node:fs";
import path from "node:path";
import { sha256 } from "@noble/hashes/sha2";

import { apportion } from "./apportion";
import * as tx from "./steemTx";

export function vectorsPath(): string {
  const packaged = path.join(__dirname, "vectors.json");
  if (fs.existsSync(packaged)) return packaged;
  for (let dir = __dirname; ; dir = path.dirname(dir)) {
    for (const candidate of [path.join(dir, "multisig", "vectors.json"), path.join(dir, "oracle", "multisig", "vectors.json")]) {
      if (fs.existsSync(candidate) && path.dirname(candidate) !== __dirname) return candidate;
    }
    if (path.dirname(dir) === dir) break;
  }
  throw new Error("oracle/multisig/vectors.json not found (packaged or in the repo)");
}

let cached: any;
export function loadVectors(): any {
  if (cached === undefined) cached = JSON.parse(fs.readFileSync(vectorsPath(), "utf8"));
  return cached;
}

/** Runs the transaction, key, signing, asset and apportionment vectors. */
export function selftest(v: any = loadVectors()): void {
  const txs: any[] = v.transactions ?? [];
  const keys: any[] = v.keys ?? [];
  if (txs.length === 0 || keys.length === 0) throw new Error("selftest: vectors file has no transactions or keys");

  for (const tv of txs) {
    const t = tx.Transaction.fromJSON(tv.tx);
    const ser = tx.hex(t.serialize());
    if (ser !== tv.serialized) throw new Error(`selftest ${JSON.stringify(tv.name)}: serialized ${ser}, want ${tv.serialized}`);
    const digest = t.digest();
    if (tx.hex(digest) !== tv.digest) throw new Error(`selftest ${JSON.stringify(tv.name)}: digest ${tx.hex(digest)}, want ${tv.digest}`);
    if (t.id() !== tv.id) throw new Error(`selftest ${JSON.stringify(tv.name)}: id ${t.id()}, want ${tv.id}`);
    // The stored JSON must round-trip to the same ID (other clients recompute
    // proposal IDs from the JSON).
    const again = tx.Transaction.fromJSON(JSON.parse(JSON.stringify(t.toJSON())));
    if (again.id() !== tv.id) throw new Error(`selftest ${JSON.stringify(tv.name)}: JSON round-trip changed the id to ${again.id()}`);
    for (const [i, s] of (tv.signatures ?? []).entries()) {
      const pub = tx.recoverSigner(digest, s.sig);
      if (pub !== s.pubkey) throw new Error(`selftest ${JSON.stringify(tv.name)}: signature ${i} recovers to ${pub}, want ${s.pubkey}`);
    }
  }

  for (const kv of keys) {
    const priv = tx.decodeWif(kv.wif);
    const pub = tx.publicKeyOf(priv);
    if (pub !== kv.pubkey) throw new Error(`selftest key: pubkey ${pub}, want ${kv.pubkey}`);
    if (tx.hex(tx.decodePublicKey(kv.pubkey)) !== kv.compressed) throw new Error("selftest key: decodePublicKey mismatch");
    for (let i = 0; i < 64; i++) {
      const digest = sha256(new TextEncoder().encode(`svm.bank selftest ${i}`));
      const sig = tx.sign(priv, digest);
      if (sig.slice(0, 2) !== "1f" && sig.slice(0, 2) !== "20") throw new Error(`selftest sign ${i}: header ${sig.slice(0, 2)}`);
      if (tx.recoverSigner(digest, sig) !== kv.pubkey) throw new Error(`selftest sign ${i}: does not recover to ${kv.pubkey}`);
    }
  }

  for (const av of v.assets ?? []) {
    const milli = BigInt(av.millisteem);
    const got = tx.formatMilli(milli, av.symbol);
    if (got !== av.string) throw new Error(`selftest asset: formatMilli(${milli}, ${av.symbol}) = ${got}, want ${av.string}`);
    const a = tx.parseAsset(av.string);
    if (a.amount !== milli || a.symbol !== av.symbol || tx.assetString(a) !== av.string) {
      throw new Error(`selftest asset: parseAsset(${JSON.stringify(av.string)})`);
    }
  }

  const cases: any[] = v.apportionment ?? [];
  if (cases.length === 0) throw new Error("selftest apportionment: no cases");
  for (const c of cases) {
    const got = apportion(c.validators.map((x: any) => ({ account: x.account, tokens: BigInt(x.tokens) })), c.threshold_percent);
    const want = tx.Authority.fromJSON({ ...c.expected, key_auths: [] });
    if (!got.equal(want)) {
      throw new Error(`selftest apportionment ${JSON.stringify(c.name)}:\n got  ${JSON.stringify(got)}\n want ${JSON.stringify(want)}`);
    }
  }
}
