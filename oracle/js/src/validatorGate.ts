// Whether a validator's duty txs can land on-chain.
// Mirrors oracle/go/relayer/validatorgate.go — see oracle/PROTOCOL.md §10.

/** The fields read from the REST `/cosmos/staking/v1beta1/validators/{valoper}` "validator" object. */
export interface ValidatorRecord {
  status?: string;
  jailed?: boolean;
}

/**
 * A validator's duty txs — bridge attestations and price-feed prevotes/votes —
 * only land if it is bonded AND not jailed. Anything else is rejected by the
 * chain, and price-feed txs are not fee-exempt, so broadcasting them anyway
 * just spends gas on guaranteed failures. `undefined` means the key isn't a
 * validator at all. `reason` is for logs.
 */
export function validatorCanSubmit(v: ValidatorRecord | undefined): { ok: boolean; reason: string } {
  if (!v) return { ok: false, reason: "not a validator" };
  if (v.jailed) return { ok: false, reason: "validator is jailed" };
  if (v.status !== "BOND_STATUS_BONDED") {
    return { ok: false, reason: `validator is not bonded (${v.status ?? ""})` };
  }
  return { ok: true, reason: "" };
}
