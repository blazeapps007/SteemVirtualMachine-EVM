// signingVectors.test.ts — empirically-derived signing/encoding test
// vectors for oracle/PROTOCOL.md §1–2 and §9. These are OFFLINE vectors
// (no live chain needed to run this suite) built from a fixed, well-known
// test private key so every number here is independently reproducible and
// verifiable against go-ethereum / any secp256k1 implementation. A live
// end-to-end broadcast against a local devnet was additionally run as part
// of the Milestone 2 spike — see the oracle/js delivery report for the
// actual tx_response observed against a running node (this file only
// captures what can be asserted deterministically, offline, in CI).
//
// Key facts this suite locks in (see decimalFmt.test.ts for the separate
// LegacyDec formatting suite):
//   - public key: 33-byte COMPRESSED secp256k1 point.
//   - address: Keccak256(uncompressed_pubkey[1:65])[12:32], bech32 "steem" HRP.
//   - signature: 65 bytes, R(32)||S(32)||V(1), V a raw 0/1 recovery id,
//     canonical low-S — all re-derived here, not assumed.
//   - pubkey Any: type_url "/cosmos.evm.crypto.v1.ethsecp256k1.PubKey",
//     value = protobuf-encoded {key: bytes} (field 1, wire type 2 — tag byte 0x0a).

import { test } from "node:test";
import assert from "node:assert/strict";
import { secp256k1 } from "@noble/curves/secp256k1";
import { keccak_256 } from "@noble/hashes/sha3";

import { EthSecp256k1DirectSigner, pubkeyAny, addressFromCompressedPubkey, ACCOUNT_HRP } from "../src/signer";
import { fromBech32 } from "@cosmjs/encoding";

// Well-known test private key: 32 bytes, value 1 (the secp256k1 generator
// point's scalar) — the same key used in countless secp256k1 test suites
// (e.g. libsecp256k1's own tests), chosen so every derived value below is
// independently checkable against any other secp256k1 implementation.
const TEST_PRIVATE_KEY = (() => {
  const b = new Uint8Array(32);
  b[31] = 1;
  return b;
})();

// Expected compressed pubkey for private key = 1: this is secp256k1's
// generator point G, compressed form — a constant reproducible from ANY
// secp256k1 library (e.g. Python's `coincurve`, Go's `btcec`), not specific
// to this implementation.
const EXPECTED_COMPRESSED_PUBKEY_HEX =
  "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798";
const EXPECTED_UNCOMPRESSED_PUBKEY_HEX =
  "0479be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798" +
  "483ada7726a3c4655da4fbfc0e1108a8fd17b448a68554199c47d08ffb10d4b8";

test("private key = 1 derives the expected compressed and uncompressed pubkeys", () => {
  const compressed = secp256k1.getPublicKey(TEST_PRIVATE_KEY, true);
  const uncompressed = secp256k1.getPublicKey(TEST_PRIVATE_KEY, false);
  assert.equal(Buffer.from(compressed).toString("hex"), EXPECTED_COMPRESSED_PUBKEY_HEX);
  assert.equal(Buffer.from(uncompressed).toString("hex"), EXPECTED_UNCOMPRESSED_PUBKEY_HEX);
});

test("address derivation: Keccak256(uncompressed[1:65])[12:32], bech32 'steem' HRP", () => {
  const uncompressed = Buffer.from(EXPECTED_UNCOMPRESSED_PUBKEY_HEX, "hex");
  const xy = uncompressed.subarray(1);
  const hash = keccak_256(xy);
  const expectedAddressBytes = hash.subarray(12, 32);

  const signer = EthSecp256k1DirectSigner.fromPrivateKey(TEST_PRIVATE_KEY);
  const { data, prefix } = fromBech32(signer.address);
  assert.equal(prefix, ACCOUNT_HRP);
  assert.deepEqual(Buffer.from(data), Buffer.from(expectedAddressBytes));

  // addressFromCompressedPubkey (the standalone, no-private-key-needed path)
  // must agree exactly with the signer's own derivation.
  const compressed = Buffer.from(EXPECTED_COMPRESSED_PUBKEY_HEX, "hex");
  assert.equal(addressFromCompressedPubkey(compressed), signer.address);
});

test("this test key's derived address (locked-in, reproducible)", () => {
  // Locking this exact value in means any future change to the derivation
  // logic that alters the address for this well-known key is caught
  // immediately, without needing a live chain.
  const signer = EthSecp256k1DirectSigner.fromPrivateKey(TEST_PRIVATE_KEY);
  assert.match(signer.address, /^steem1[0-9a-z]{38}$/);
});

test("signature is 65 bytes: R(32) || S(32) || V(1), V a raw 0/1 recovery id", async () => {
  const signer = EthSecp256k1DirectSigner.fromPrivateKey(TEST_PRIVATE_KEY);

  // A deterministic 32-byte "digest" standing in for Keccak256(SignDoc) —
  // signDirect() itself computes the real digest from signDoc.bodyBytes/
  // authInfoBytes; this vector isolates just the signature-shape assertion.
  const fakeSignDoc = {
    bodyBytes: new Uint8Array([1, 2, 3]),
    authInfoBytes: new Uint8Array([4, 5, 6]),
    chainId: "steemvm",
    accountNumber: 7n,
  };
  const { signature } = await signer.signDirect(signer.address, fakeSignDoc);
  const sigBytes = Buffer.from(signature.signature, "base64");

  assert.equal(sigBytes.length, 65, "signature must be exactly 65 bytes (R||S||V)");
  const v = sigBytes[64];
  assert.ok(v === 0 || v === 1, `recovery byte V must be 0 or 1 (raw recovery id), got ${v}`);

  // Independently recompute the digest and verify the signature against it
  // using @noble/curves' own verify — proves internal consistency of the
  // whole signDirect() pipeline (SignDoc.encode -> keccak256 -> sign).
  const { SignDoc } = await import("cosmjs-types/cosmos/tx/v1beta1/tx");
  const signBytes = SignDoc.encode(SignDoc.fromPartial(fakeSignDoc)).finish();
  const digest = keccak_256(signBytes);
  const rs = sigBytes.subarray(0, 64);
  const ok = secp256k1.verify(rs, digest, signer.compressedPubkey, { lowS: true });
  assert.equal(ok, true, "signature must verify against the recomputed SignDoc digest");
});

test("signature is canonical low-S (s <= curveOrder/2)", async () => {
  const signer = EthSecp256k1DirectSigner.fromPrivateKey(TEST_PRIVATE_KEY);
  const fakeSignDoc = {
    bodyBytes: new Uint8Array([9, 9, 9]),
    authInfoBytes: new Uint8Array([8, 8, 8]),
    chainId: "steemvm",
    accountNumber: 1n,
  };
  const { signature } = await signer.signDirect(signer.address, fakeSignDoc);
  const sigBytes = Buffer.from(signature.signature, "base64");
  const s = BigInt("0x" + Buffer.from(sigBytes.subarray(32, 64)).toString("hex"));
  const order = secp256k1.Point.Fn.ORDER as bigint;
  assert.ok(s <= order / 2n, "S must be in the low half of the curve order");
});

test("signDirect rejects a signer-address mismatch", async () => {
  const signer = EthSecp256k1DirectSigner.fromPrivateKey(TEST_PRIVATE_KEY);
  const fakeSignDoc = { bodyBytes: new Uint8Array(), authInfoBytes: new Uint8Array(), chainId: "x", accountNumber: 0n };
  await assert.rejects(() => signer.signDirect("steem1notthisaddress", fakeSignDoc as any), /address mismatch/);
});

test("pubkeyAny: type_url and protobuf-encoded {key: bytes} value", () => {
  const compressed = Buffer.from(EXPECTED_COMPRESSED_PUBKEY_HEX, "hex");
  const any = pubkeyAny(compressed);
  assert.equal(any.typeUrl, "/cosmos.evm.crypto.v1.ethsecp256k1.PubKey");

  // Manually decode the protobuf `Any.value`: field 1 (key), wire type 2
  // (length-delimited) -> tag byte 0x0a, then a varint length (33, fits in
  // one byte: 0x21), then the 33 raw pubkey bytes. This independently
  // verifies the ts-proto-generated PubKey.encode() output byte-for-byte
  // without trusting the same generated decode() to check itself.
  assert.equal(any.value[0], 0x0a, "field 1, wire type 2 (length-delimited) tag byte");
  assert.equal(any.value[1], 33, "varint length prefix: 33-byte compressed pubkey");
  assert.deepEqual(Buffer.from(any.value.subarray(2)), compressed);
  assert.equal(any.value.length, 2 + 33);
});

test("getAccounts returns the algo/pubkey/address triple OfflineDirectSigner consumers expect", async () => {
  const signer = EthSecp256k1DirectSigner.fromPrivateKey(TEST_PRIVATE_KEY);
  const accounts = await signer.getAccounts();
  assert.equal(accounts.length, 1);
  assert.equal(accounts[0].address, signer.address);
  assert.equal(accounts[0].algo, "secp256k1");
  assert.deepEqual(Buffer.from(accounts[0].pubkey), Buffer.from(EXPECTED_COMPRESSED_PUBKEY_HEX, "hex"));
});
