// spike.ts — throwaway live-broadcast spike (JS Milestone 2, see oracle/PROTOCOL.md §9).
//
// NOT part of the shipped client. Signs and broadcasts a real MsgAttestDeposit
// using the actual src/signer.ts + src/broadcast.ts code (not a hand-rolled
// one-off) against a throwaway single-validator devnet, to empirically confirm:
//   1. the account REST response shape (BaseAccount vs. something else),
//   2. the signature byte length/format actually accepted by a running chain,
//   3. that tx_response.code comes back 0 (or a clean logical rejection, not a
//      signature/parse failure).
//
// Usage: SPIKE_PRIVATE_KEY_HEX=<64 hex chars> npx tsx scripts/spike.ts
//
// Talks to a throwaway devnet on http://localhost:12317 (REST) with chain-id
// "steemvm", bridge_enabled=true, gateway_account="steemvm-gateway", and the
// signing key as the sole bonded genesis validator. See the JS live-broadcast
// subsection of oracle/PROTOCOL.md §9 for how that devnet was built.

import { EthSecp256k1DirectSigner } from "../src/signer";
import { buildRegistry, broadcastAttestations, fetchAccount, TYPE_URL_MSG_ATTEST_DEPOSIT } from "../src/broadcast";
import { MsgAttestDeposit } from "../src/proto/steemvm/steembridge/v1/tx";
import { BridgeAsset } from "../src/proto/steemvm/steembridge/v1/asset";
import Long from "long";
import { randomBytes } from "node:crypto";

async function main() {
  const hex = process.env.SPIKE_PRIVATE_KEY_HEX;
  if (!hex) {
    throw new Error("set SPIKE_PRIVATE_KEY_HEX to the devnet validator's raw 32-byte hex private key");
  }
  const restUrl = process.env.SPIKE_REST_URL ?? "http://localhost:12317";
  const chainId = process.env.SPIKE_CHAIN_ID ?? "steemvm";

  const privateKey = Buffer.from(hex.replace(/^0x/, ""), "hex");
  if (privateKey.length !== 32) {
    throw new Error(`SPIKE_PRIVATE_KEY_HEX must decode to 32 bytes, got ${privateKey.length}`);
  }

  const signer = EthSecp256k1DirectSigner.fromPrivateKey(privateKey);
  console.log("validator address:", signer.address);
  console.log("compressed pubkey:", Buffer.from(signer.compressedPubkey).toString("hex"));

  // Confirm the account REST response shape empirically before doing anything else.
  const account = await fetchAccount(restUrl, signer.address);
  console.log("account_number:", account.accountNumber.toString(), "sequence:", account.sequence.toString());

  const registry = buildRegistry();

  // steemTxidRegex (x/oracle/bridge/types/message_submit_steem_deposit.go) requires
  // exactly 40 lowercase hex characters, so a real 20-byte random value is used
  // rather than an arbitrary string.
  const msg: MsgAttestDeposit = {
    validator: signer.address,
    txid: Buffer.from(randomBytes(20)).toString("hex"),
    opIndex: 0,
    steemBlock: Long.fromNumber(12345678, true),
    steemTimestamp: new Date().toISOString().replace(/\.\d+Z$/, ""),
    steemSender: "alice",
    gatewayAccount: "steemvm-gateway",
    amountMillisteem: Long.fromNumber(1_000_000, true),
    memo: `svm-deposit ${signer.address}`,
    asset: BridgeAsset.BRIDGE_ASSET_STEEM,
  };

  console.log("MsgAttestDeposit:", JSON.stringify(msg, null, 2));

  const result = await broadcastAttestations({
    restUrl,
    chainId,
    signer,
    registry,
    msgs: [{ typeUrl: TYPE_URL_MSG_ATTEST_DEPOSIT, value: msg }],
  });

  console.log("---- tx_response ----");
  console.log("txHash:", result.txHash);
  console.log("code:", result.code);
  console.log("rawLog:", result.rawLog);
}

main().catch((err) => {
  console.error("SPIKE FAILED:", err);
  process.exit(1);
});
