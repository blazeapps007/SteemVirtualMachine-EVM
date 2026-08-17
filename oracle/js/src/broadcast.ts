// broadcast.ts — REST-based account/sequence lookup, tx assembly + signing,
// and broadcast/delivery-confirmation, mirroring oracle/go/relayer/broadcast.go.
// Uses the Cosmos gRPC-gateway REST API on :1317 rather than raw CometBFT
// RPC/gRPC (see oracle/PROTOCOL.md §4) — avoids a gRPC client dependency in
// this client, matching the Python client's approach.
//
// Critical correctness rule from PROTOCOL.md §2 step 7: body_bytes /
// auth_info_bytes in the final TxRaw MUST be the EXACT SAME serialized bytes
// used to build the SignDoc — never re-marshal after signing (protobuf
// re-encoding is not guaranteed byte-stable). This module marshals TxBody
// and AuthInfo exactly once each and reuses those bytes everywhere.

import { Registry } from "@cosmjs/proto-signing";
import { TxBody, AuthInfo, SignDoc, TxRaw, Fee, SignerInfo, ModeInfo } from "cosmjs-types/cosmos/tx/v1beta1/tx";
import { SignMode } from "cosmjs-types/cosmos/tx/signing/v1beta1/signing";
import { Coin } from "cosmjs-types/cosmos/base/v1beta1/coin";

import { EthSecp256k1DirectSigner, pubkeyAny } from "./signer";
import { parseLegacyDec } from "./decimalFmt";

import { MsgAttestDeposit, MsgAttestWithdrawalPayout, MsgSubmitNameRegistration } from "./proto/steemvm/steembridge/v1/tx";
import {
  MsgAggregateExchangeRatePrevote,
  MsgAggregateExchangeRateVote,
} from "./proto/steemvm/oracle/data/v1/tx";

// --- type URLs (oracle/PROTOCOL.md §2 "Message type URLs" table) ----------

export const TYPE_URL_MSG_ATTEST_DEPOSIT = "/steemvm.steembridge.v1.MsgAttestDeposit";
export const TYPE_URL_MSG_ATTEST_WITHDRAWAL_PAYOUT = "/steemvm.steembridge.v1.MsgAttestWithdrawalPayout";
export const TYPE_URL_MSG_SUBMIT_NAME_REGISTRATION = "/steemvm.steembridge.v1.MsgSubmitNameRegistration";
export const TYPE_URL_MSG_AGGREGATE_EXCHANGE_RATE_PREVOTE = "/steemvm.oracle.data.v1.MsgAggregateExchangeRatePrevote";
export const TYPE_URL_MSG_AGGREGATE_EXCHANGE_RATE_VOTE = "/steemvm.oracle.data.v1.MsgAggregateExchangeRateVote";

/** Builds a Registry with the two SteemVM-custom modules' message types
 * registered. cosmjs-types already covers everything standard (TxBody,
 * AuthInfo, Coin, ...); only these five need registering here. */
export function buildRegistry(): Registry {
  const registry = new Registry();
  registry.register(TYPE_URL_MSG_ATTEST_DEPOSIT, MsgAttestDeposit as any);
  registry.register(TYPE_URL_MSG_ATTEST_WITHDRAWAL_PAYOUT, MsgAttestWithdrawalPayout as any);
  registry.register(TYPE_URL_MSG_SUBMIT_NAME_REGISTRATION, MsgSubmitNameRegistration as any);
  registry.register(TYPE_URL_MSG_AGGREGATE_EXCHANGE_RATE_PREVOTE, MsgAggregateExchangeRatePrevote as any);
  registry.register(TYPE_URL_MSG_AGGREGATE_EXCHANGE_RATE_VOTE, MsgAggregateExchangeRateVote as any);
  return registry;
}

/** Gas budget constants — mirror oracle/go/relayer/broadcast.go exactly so
 * fee-exempt attestations and price-feed txs both stay within the same
 * on-chain expectations regardless of client language. */
export const GAS_PER_MSG = 400_000;
export const GAS_BASE = 200_000;
export const MAX_MSGS_PER_TX = 50;
export const PRICE_FEED_GAS_PER_MSG = 250_000;

export interface EncodeObject {
  readonly typeUrl: string;
  readonly value: unknown;
}

export interface AccountInfo {
  accountNumber: bigint;
  sequence: bigint;
}

/**
 * Fetches account_number/sequence via the REST gRPC-gateway. Empirically
 * verified (Milestone 2 spike, see test/signingVectors.test.ts and the
 * oracle/js report) that a funded or unfunded account on this chain returns
 * a plain `BaseAccount` at `account`, i.e.
 * `{ account: { "@type": "/cosmos.auth.v1beta1.BaseAccount", address,
 * pub_key, account_number, sequence } }` — account_number/sequence arrive as
 * decimal STRINGS (standard proto3 JSON uint64 mapping), not numbers.
 */
export async function fetchAccount(restUrl: string, address: string): Promise<AccountInfo> {
  const url = `${trimSlash(restUrl)}/cosmos/auth/v1beta1/accounts/${address}`;
  const resp = await fetch(url);
  if (resp.status === 404) {
    throw new Error(
      `account ${address} not found on chain (needs at least one prior tx or a genesis balance to exist)`,
    );
  }
  if (!resp.ok) {
    throw new Error(`fetching account ${address}: HTTP ${resp.status}: ${await safeText(resp)}`);
  }
  const body = (await resp.json()) as { account?: Record<string, unknown> };
  const account = body.account;
  if (!account || typeof account.account_number !== "string" || typeof account.sequence !== "string") {
    throw new Error(
      `unexpected account response shape for ${address}: ${JSON.stringify(body)} ` +
        `(expected a plain BaseAccount with string account_number/sequence — see oracle/PROTOCOL.md §4)`,
    );
  }
  return { accountNumber: BigInt(account.account_number), sequence: BigInt(account.sequence) };
}

/** Parses a Cosmos SDK gas-prices string (e.g. "1000000000asteem", or
 * multi-denom "1000000000asteem,1uatom") into per-denom LegacyDec-scaled
 * bigints, using the same fixed-point parser as decimalFmt.ts (never
 * floating point) so a fractional gas price never loses precision. */
function parseGasPrices(gasPrices: string): { denom: string; scaledAmount: bigint }[] {
  return gasPrices
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s.length > 0)
    .map((entry) => {
      const match = /^([0-9.]+)([a-zA-Z][a-zA-Z0-9/:._-]*)$/.exec(entry);
      if (!match) {
        throw new Error(`invalid gas price entry ${JSON.stringify(entry)} in ${JSON.stringify(gasPrices)}`);
      }
      const [, amount, denom] = match;
      return { denom, scaledAmount: parseLegacyDec(amount) };
    });
}

/** fee = ceil(gasPrice * gasLimit) per denom — matches the Cosmos SDK
 * client's fee computation (DecCoins.Ceil after multiplying by gas). */
function computeFee(gasPrices: string, gasLimit: bigint): Coin[] {
  const LEGACY_DEC_ONE = 10n ** 18n;
  return parseGasPrices(gasPrices).map(({ denom, scaledAmount }) => {
    const scaledFee = scaledAmount * gasLimit; // still scaled by 1e18
    const amount = (scaledFee + LEGACY_DEC_ONE - 1n) / LEGACY_DEC_ONE; // ceil
    return { denom, amount: amount.toString() };
  });
}

export interface BroadcastResult {
  txHash: string;
  code: number;
  rawLog: string;
}

/**
 * Signs and broadcasts msgs as one SIGN_MODE_DIRECT tx (BROADCAST_MODE_SYNC),
 * then polls for delivery. gasPrices="" means zero-fee (valid only for
 * fee-exempt attestation messages — see PROTOCOL.md §3); non-empty pays a fee.
 */
export async function signAndBroadcastTx(opts: {
  restUrl: string;
  chainId: string;
  signer: EthSecp256k1DirectSigner;
  registry: Registry;
  msgs: EncodeObject[];
  gas: number;
  gasPrices: string; // "" => zero fee
}): Promise<BroadcastResult> {
  const { restUrl, chainId, signer, registry, msgs, gas, gasPrices } = opts;
  if (msgs.length === 0) {
    throw new Error("signAndBroadcastTx: no messages to send");
  }

  const account = await fetchAccount(restUrl, signer.address);

  const bodyBytes = TxBody.encode(
    TxBody.fromPartial({
      messages: msgs.map((m) => registry.encodeAsAny(m)),
      memo: "",
      timeoutHeight: 0n,
    }),
  ).finish();

  const gasLimit = BigInt(gas);
  const feeAmount = gasPrices ? computeFee(gasPrices, gasLimit) : [];

  const authInfoBytes = AuthInfo.encode(
    AuthInfo.fromPartial({
      signerInfos: [
        SignerInfo.fromPartial({
          publicKey: pubkeyAny(signer.compressedPubkey),
          modeInfo: ModeInfo.fromPartial({ single: { mode: SignMode.SIGN_MODE_DIRECT } }),
          sequence: account.sequence,
        }),
      ],
      fee: Fee.fromPartial({ amount: feeAmount, gasLimit, payer: "", granter: "" }),
    }),
  ).finish();

  const signDoc = SignDoc.fromPartial({
    bodyBytes,
    authInfoBytes,
    chainId,
    accountNumber: account.accountNumber,
  });

  const { signature } = await signer.signDirect(signer.address, signDoc);
  const signatureBytes = Buffer.from(signature.signature, "base64");

  // Reuse bodyBytes/authInfoBytes verbatim — never re-marshal (PROTOCOL.md §2 step 7).
  const txRawBytes = TxRaw.encode(
    TxRaw.fromPartial({ bodyBytes, authInfoBytes, signatures: [signatureBytes] }),
  ).finish();

  const broadcastResp = await broadcastTxSync(restUrl, txRawBytes);
  if (broadcastResp.code !== 0) {
    return broadcastResp; // CheckTx rejection — caller decides whether that's expected
  }

  return waitForDelivery(restUrl, broadcastResp.txHash);
}

async function broadcastTxSync(restUrl: string, txBytes: Uint8Array): Promise<BroadcastResult> {
  const url = `${trimSlash(restUrl)}/cosmos/tx/v1beta1/txs`;
  const resp = await fetch(url, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      tx_bytes: Buffer.from(txBytes).toString("base64"),
      mode: "BROADCAST_MODE_SYNC",
    }),
  });
  if (!resp.ok) {
    throw new Error(`broadcast HTTP ${resp.status}: ${await safeText(resp)}`);
  }
  const body = (await resp.json()) as { tx_response?: { txhash: string; code: number; raw_log: string } };
  const txResponse = body.tx_response;
  if (!txResponse) {
    throw new Error(`broadcast: unexpected response shape: ${JSON.stringify(body)}`);
  }
  return { txHash: txResponse.txhash, code: txResponse.code, rawLog: txResponse.raw_log };
}

/** Polls the node until the tx is found in a block, erroring on delivery
 * failure or timeout. Matches the Go client's ~2s poll / ~45s timeout
 * cadence — the caller must not advance any scan cursor past blocks whose
 * attestations were never actually executed, only broadcast-accepted. */
async function waitForDelivery(restUrl: string, txHash: string): Promise<BroadcastResult> {
  const pollEveryMs = 2_000;
  const maxWaitMs = 45_000;
  const deadline = Date.now() + maxWaitMs;

  while (true) {
    await sleep(pollEveryMs);

    const url = `${trimSlash(restUrl)}/cosmos/tx/v1beta1/txs/${txHash}`;
    const resp = await fetch(url);
    if (resp.ok) {
      const body = (await resp.json()) as { tx_response?: { txhash: string; code: number; raw_log: string } };
      if (body.tx_response) {
        return {
          txHash: body.tx_response.txhash,
          code: body.tx_response.code,
          rawLog: body.tx_response.raw_log,
        };
      }
    }
    if (Date.now() > deadline) {
      throw new Error(`tx ${txHash} not observed in a block within ${maxWaitMs}ms`);
    }
  }
}

/** Broadcasts fee-exempt bridge attestation messages (see PROTOCOL.md §3). */
export function broadcastAttestations(opts: {
  restUrl: string;
  chainId: string;
  signer: EthSecp256k1DirectSigner;
  registry: Registry;
  msgs: EncodeObject[];
}): Promise<BroadcastResult> {
  const { msgs } = opts;
  if (msgs.length > MAX_MSGS_PER_TX) {
    throw new Error(`too many messages in one tx: ${msgs.length} > ${MAX_MSGS_PER_TX}`);
  }
  return signAndBroadcastTx({ ...opts, gas: GAS_BASE + GAS_PER_MSG * msgs.length, gasPrices: "" });
}

/** Broadcasts price-feed messages (a reveal, a fresh prevote, or both) —
 * NOT fee-exempt; gasPrices must be a non-empty DecCoins string. */
export function broadcastPriceFeedMsgs(opts: {
  restUrl: string;
  chainId: string;
  signer: EthSecp256k1DirectSigner;
  registry: Registry;
  msgs: EncodeObject[];
  gasPrices: string;
}): Promise<BroadcastResult> {
  if (!opts.gasPrices) {
    throw new Error("price feed txs are not fee-exempt: ORACLE_GAS_PRICES must be set");
  }
  return signAndBroadcastTx({ ...opts, gas: PRICE_FEED_GAS_PER_MSG * opts.msgs.length });
}

function trimSlash(url: string): string {
  return url.endsWith("/") ? url.slice(0, -1) : url;
}

async function safeText(resp: Response): Promise<string> {
  try {
    return await resp.text();
  } catch {
    return "<unreadable body>";
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
