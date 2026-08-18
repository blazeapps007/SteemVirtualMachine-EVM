// router.ts — memo classification + message building, mirroring
// oracle/go/relayer/router.go field-for-field. See oracle/PROTOCOL.md §5.

import Long from "long";

import { BridgeAsset } from "./proto/steemvm/steembridge/v1/asset";
import type { Transfer } from "./steemClient";
import {
  TYPE_URL_MSG_ATTEST_DEPOSIT,
  TYPE_URL_MSG_SUBMIT_NAME_REGISTRATION,
  type EncodeObject,
} from "./broadcast";

/**
 * GATEWAY_ACCOUNT is a hardcoded chain constant, never read from the chain's
 * params query — mirrors x/oracle/bridge/types.GatewayAccount ("svm.bank"),
 * which is NOT governance-settable. Params.gateway_account still exists on
 * the wire for backward-compat display but consensus logic ignores it, so
 * this client must never trust that field either.
 */
export const GATEWAY_ACCOUNT = "svm.bank";

export enum Intent {
  /** Mints bridged STEEM ("svm-deposit <address>" or a bare address memo —
   * the historical deposit format). */
  Deposit = "deposit",
  /** Links the sender's Steem account name to the memo address
   * ("svm-register <address>"). */
  Register = "register",
}

/**
 * Classifies a gateway transfer by its memo prefix. Everything that is not
 * explicitly a name registration is attested as a deposit — the chain
 * itself decides claimability from the memo, and unparseable-memo deposits
 * become UNCLAIMABLE with a full audit trail rather than being silently
 * dropped by validators.
 */
export function routeMemo(memo: string): Intent {
  const trimmed = memo.trim();
  if (trimmed === "svm-register" || trimmed.startsWith("svm-register ") || trimmed.startsWith("svm-register\t")) {
    return Intent.Register;
  }
  return Intent.Deposit;
}

/**
 * Converts a scanned transfer into the attestation EncodeObject this
 * validator should broadcast. All Steem-side facts are passed through
 * verbatim (including the memo — the chain strips intent prefixes itself,
 * so every validator's submission stays byte-identical).
 */
export function buildMsg(t: Transfer, intent: Intent, validator: string, gateway: string): EncodeObject {
  // The name service is STEEM-only; an SBD transfer is always a deposit even
  // if its memo looks like a registration.
  const effectiveIntent = t.asset === BridgeAsset.BRIDGE_ASSET_SBD ? Intent.Deposit : intent;

  if (effectiveIntent === Intent.Register) {
    return {
      typeUrl: TYPE_URL_MSG_SUBMIT_NAME_REGISTRATION,
      value: {
        validator,
        txid: t.txid,
        opIndex: t.opIndex,
        steemBlock: Long.fromNumber(t.steemBlock, true),
        steemTimestamp: t.steemTimestamp,
        steemAccount: t.from,
        gatewayAccount: gateway,
        amountMillisteem: Long.fromString(t.amountMillisteem.toString(), true),
        memo: t.memo,
      },
    };
  }
  return {
    typeUrl: TYPE_URL_MSG_ATTEST_DEPOSIT,
    value: {
      validator,
      txid: t.txid,
      opIndex: t.opIndex,
      steemBlock: Long.fromNumber(t.steemBlock, true),
      steemTimestamp: t.steemTimestamp,
      steemSender: t.from,
      gatewayAccount: gateway,
      amountMillisteem: Long.fromString(t.amountMillisteem.toString(), true),
      memo: t.memo,
      asset: t.asset,
    },
  };
}
