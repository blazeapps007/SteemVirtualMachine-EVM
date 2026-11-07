// Steem transaction core: shared vectors, canonical signing, strict parsing.
import { test } from "node:test";
import assert from "node:assert/strict";
import { sha256 } from "@noble/hashes/sha2";

import * as tx from "../src/multisig/steemTx";
import { loadVectors, selftest } from "../src/multisig/vectors";

test("selftest passes the shared vectors", () => {
  selftest();
});

test("sign/recover round trips are canonical", () => {
  const priv = tx.decodeWif(loadVectors().keys[0].wif);
  const want = tx.publicKeyOf(priv);
  for (let i = 0; i < 2000; i++) {
    const digest = sha256(new TextEncoder().encode(`round trip ${i}`));
    const sig = tx.sign(priv, digest);
    const raw = tx.fromHex(sig);
    assert.ok(raw[0] === 0x1f || raw[0] === 0x20);
    assert.ok(tx.isCanonical(raw.slice(1)));
    assert.equal(tx.recoverSigner(digest, sig), want);
  }
});

test("non-canonical signatures are rejected", () => {
  const digest = sha256(new TextEncoder().encode("x"));
  const sig = tx.fromHex(tx.sign(tx.decodeWif(loadVectors().keys[0].wif), digest));
  sig[1] |= 0x80;
  assert.throws(() => tx.recoverSigner(digest, tx.hex(sig)));
  assert.throws(() => tx.recoverSigner(digest, "00".repeat(10)));
});

test("parseAsset is strict", () => {
  for (const bad of ["1 STEEM", "1.0 STEEM", "1.0000 STEEM", "-1.000 STEEM", "+1.000 STEEM", "1.000 steem", "1.000", ".000 STEEM",
    "1,000 STEEM", "１.000 STEEM", "1.00a STEEM", "9223372036854775.808 STEEM", "1.000 toString", "1.000 __proto__"]) {
    assert.throws(() => tx.parseAsset(bad), tx.SteemTxError, bad);
  }
  assert.equal(tx.formatMilli(12345n, "STEEM"), "12.345 STEEM");
  assert.equal(tx.assetString(tx.parseAsset("0.001 SBD")), "0.001 SBD");
  assert.equal(tx.parseAsset("123456789012.000000 VESTS").amount, 123456789012000000n);
});

test("account_auths sort in byte order", () => {
  const a = new tx.Authority(1, [["b", 1], ["a.b", 1], ["a-b", 1], ["a", 1]], []);
  assert.deepEqual(a.sorted().accountAuths.map(([n]) => n), ["a", "a-b", "a.b", "b"]);
});

test("transaction JSON round trip keeps the id", () => {
  for (const tv of loadVectors().transactions) {
    const t = tx.Transaction.fromJSON(tv.tx);
    assert.equal(tx.Transaction.fromJSON(JSON.parse(JSON.stringify(t.toJSON()))).id(), tv.id);
  }
});

test("transaction parse rejects bad shapes", () => {
  for (const bad of [
    { ref_block_num: 70000, ref_block_prefix: 1, expiration: "2026-01-01T00:00:00", operations: [] },
    { ref_block_num: true, ref_block_prefix: 1, expiration: "2026-01-01T00:00:00", operations: [] },
    { ref_block_num: 1, ref_block_prefix: 1, expiration: "2026-01-01T00:00:00", operations: [["vote", {}]] },
  ]) {
    assert.throws(() => tx.Transaction.fromJSON(bad));
  }
});

test("time layout is exact", () => {
  assert.throws(() => tx.parseTime("2026-1-2T3:04:05"));
  assert.throws(() => tx.parseTime("2026-01-02T03:04:05Z"));
  assert.throws(() => tx.parseTime("2026-02-30T00:00:00"));
  assert.equal(tx.formatTime(tx.parseTime("2026-10-11T01:40:57")), "2026-10-11T01:40:57");
});

test("base58 round trips with leading zeros", () => {
  for (const b of [new Uint8Array(0), Uint8Array.of(0), Uint8Array.of(0, 0, 1), sha256(new Uint8Array(1))]) {
    assert.deepEqual(tx.b58decode(tx.b58encode(b)), b);
  }
});
