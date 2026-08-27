// signer.ts — a hand-written OfflineDirectSigner for SteemVM's eth_secp256k1
// key type. No existing JS library signs Cosmos SDK SIGN_MODE_DIRECT tx docs
// with this key type, so this is the genuinely novel piece the plan called
// out. Follows oracle/PROTOCOL.md §1–2 exactly:
//
//   - private key: 32 raw bytes.
//   - public key: compressed secp256k1 point, 33 bytes (wire form of
//     ethsecp256k1.PubKey.Key).
//   - address: Keccak256(uncompressed_pubkey[1:65])[12:32] — standard
//     Ethereum address derivation, no chain-specific twist — bech32-encoded
//     with HRP "steem".
//   - digest = Keccak256(marshal(SignDoc)).
//   - signature = go-ethereum-style crypto.Sign: 65 bytes, R(32)||S(32)||V(1),
//     V the raw recovery id (0/1, NOT Ethereum's 27/28), low-S canonical.
//
// Uses @noble/curves (secp256k1 sign/pubkey) + @noble/hashes (Keccak256) per
// the plan's explicit choice over `elliptic`/`secp256k1` npm packages.
//
// NOTE on @cosmjs/proto-signing's pubkey-Any nuance (see oracle/js/README or
// the plan): @cosmjs/proto-signing's `encodePubkey()` helper only recognizes
// a fixed table of standard pubkey types (secp256k1, ed25519, ...) and does
// NOT know `/cosmos.evm.crypto.v1.ethsecp256k1.PubKey`. This module never
// calls `encodePubkey()` at all — `pubkeyAny()` below builds the `Any`
// manually from the ts-proto-generated `PubKey` type. Everything else in
// @cosmjs/proto-signing's assembly path (`Registry`, `makeAuthInfoBytes`,
// `makeSignDoc`, `TxBody`/`SignDoc` construction) is used completely
// unmodified — `makeAuthInfoBytes` in particular already takes a
// pre-built `Any` per signer, so it never touches `encodePubkey()` either.

import { keccak_256 } from "@noble/hashes/sha3";
import { secp256k1 } from "@noble/curves/secp256k1";
import { toBech32 } from "@cosmjs/encoding";
import { Any } from "cosmjs-types/google/protobuf/any";
import { SignDoc } from "cosmjs-types/cosmos/tx/v1beta1/tx";
import type { AccountData, DirectSignResponse, OfflineDirectSigner } from "@cosmjs/proto-signing";

import { PubKey as EthSecp256k1PubKey } from "./proto/cosmos/evm/crypto/v1/ethsecp256k1/keys";

/** Type URL for this chain's pubkey type — see oracle/PROTOCOL.md §1. */
export const ETHSECP256K1_PUBKEY_TYPE_URL = "/cosmos.evm.crypto.v1.ethsecp256k1.PubKey";

/** Bech32 HRP for SteemVM account addresses (app/config.go). Oracle clients
 * only ever need this one — never the valoper/valcons HRPs. */
export const ACCOUNT_HRP = "steem";

/**
 * Derives a SteemVM bech32 account address from a compressed secp256k1
 * public key: standard Ethereum address derivation (Keccak256 of the
 * uncompressed pubkey's 64 X||Y bytes, last 20 bytes), then bech32-encoded
 * with HRP "steem" — see oracle/PROTOCOL.md §1.
 */
export function addressFromCompressedPubkey(compressedPubkey: Uint8Array): string {
  // secp256k1.getPublicKey only accepts a PRIVATE key, so decompression goes
  // through the curve's Point API directly: parse the compressed point, then
  // re-serialize it uncompressed.
  const point = secp256k1.Point.fromBytes(compressedPubkey);
  const uncompressed = point.toBytes(false);
  return addressFromUncompressedPubkey(uncompressed);
}

function addressFromUncompressedPubkey(uncompressed: Uint8Array): string {
  if (uncompressed.length !== 65 || uncompressed[0] !== 0x04) {
    throw new Error("expected a 65-byte uncompressed secp256k1 public key (0x04 prefix)");
  }
  const xy = uncompressed.subarray(1); // drop the 0x04 prefix: 64 bytes X||Y
  const hash = keccak_256(xy);
  const addressBytes = hash.subarray(12, 32); // last 20 bytes
  return toBech32(ACCOUNT_HRP, addressBytes);
}

/** Builds the manually-constructed pubkey `Any` this chain's AuthInfo
 * expects — the bypass around `@cosmjs/proto-signing`'s `encodePubkey()`
 * described in the module doc comment above. */
export function pubkeyAny(compressedPubkey: Uint8Array): Any {
  const value = EthSecp256k1PubKey.encode(
    EthSecp256k1PubKey.fromPartial({ key: Buffer.from(compressedPubkey) }),
  ).finish();
  return Any.fromPartial({ typeUrl: ETHSECP256K1_PUBKEY_TYPE_URL, value });
}

/**
 * A single-key OfflineDirectSigner for SteemVM's eth_secp256k1 key type.
 * Construct with `EthSecp256k1DirectSigner.fromPrivateKey(bytes)`.
 */
export class EthSecp256k1DirectSigner implements OfflineDirectSigner {
  private readonly privateKey: Uint8Array;
  readonly compressedPubkey: Uint8Array;
  readonly address: string;

  private constructor(privateKey: Uint8Array) {
    if (privateKey.length !== 32) {
      throw new Error(`eth_secp256k1 private key must be 32 bytes, got ${privateKey.length}`);
    }
    this.privateKey = privateKey;
    this.compressedPubkey = secp256k1.getPublicKey(privateKey, /* isCompressed */ true);
    const uncompressed = secp256k1.getPublicKey(privateKey, /* isCompressed */ false);
    this.address = addressFromUncompressedPubkey(uncompressed);
  }

  static fromPrivateKey(privateKey: Uint8Array): EthSecp256k1DirectSigner {
    return new EthSecp256k1DirectSigner(privateKey);
  }

  async getAccounts(): Promise<readonly AccountData[]> {
    return [{ address: this.address, algo: "secp256k1", pubkey: this.compressedPubkey }];
  }

  async signDirect(signerAddress: string, signDoc: SignDoc): Promise<DirectSignResponse> {
    if (signerAddress !== this.address) {
      throw new Error(`signDirect: address mismatch (want ${this.address}, got ${signerAddress})`);
    }

    const signBytes = SignDoc.encode(signDoc).finish();
    const digest = keccak_256(signBytes);

    // @noble/curves' secp256k1 defaults to low-S canonical signatures (see
    // src/secp256k1.ts: `lowS: true` in the curve config) and always
    // computes the recovery bit for a non-prehashed sign() call — matches
    // go-ethereum's crypto.Sign exactly. Empirically re-verified in
    // test/signingVectors.test.ts rather than assumed (see
    // oracle/PROTOCOL.md §2's explicit "verify, don't assume" flag).
    const sig = secp256k1.sign(digest, this.privateKey);
    if (sig.recovery === undefined) {
      throw new Error("signDirect: signature is missing a recovery bit");
    }

    // 65 bytes: R(32) || S(32) || V(1), V the raw recovery id (0/1) — NOT
    // @noble/curves' built-in 'recovered' format, which prepends the
    // recovery byte instead of appending it (V||R||S), the opposite of the
    // go-ethereum-style layout this chain expects.
    const compact = sig.toBytes("compact"); // R(32) || S(32)
    const signature = new Uint8Array(65);
    signature.set(compact, 0);
    signature[64] = sig.recovery;

    return {
      signed: signDoc,
      signature: {
        // This chain doesn't use the amino StdSignature round-trip (we
        // build TxRaw directly from the raw 65-byte signature in
        // broadcast.ts), so pub_key here is informational only — shaped
        // consistently with the OfflineDirectSigner interface contract
        // rather than any amino pubkey table entry (this key type has none).
        pub_key: { type: "cosmos.evm.crypto.v1.ethsecp256k1.PubKey", value: toBase64(this.compressedPubkey) },
        signature: toBase64(signature),
      },
    };
  }
}

function toBase64(bytes: Uint8Array): string {
  return Buffer.from(bytes).toString("base64");
}
