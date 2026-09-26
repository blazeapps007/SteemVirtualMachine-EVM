"""Whether a validator's duty txs can land on-chain.

Mirrors oracle/go/relayer/validatorgate.go -- see oracle/PROTOCOL.md SS10.
"""

from typing import Optional


def validator_can_submit(validator: Optional[dict]) -> tuple[bool, str]:
    """A validator's duty txs -- bridge attestations and price-feed
    prevotes/votes -- only land if it is bonded AND not jailed. Anything else
    is rejected by the chain, and price-feed txs are not fee-exempt, so
    broadcasting them anyway just spends gas on guaranteed failures.

    `validator` is the REST `/cosmos/staking/v1beta1/validators/{valoper}`
    response's "validator" object (None when the key isn't a validator).
    Returns (can_submit, reason-for-logs)."""
    if not validator:
        return False, "not a validator"
    if validator.get("jailed"):
        return False, "validator is jailed"
    status = validator.get("status", "")
    if status != "BOND_STATUS_BONDED":
        return False, f"validator is not bonded ({status})"
    return True, ""
