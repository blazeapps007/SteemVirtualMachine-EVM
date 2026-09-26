package relayer

import (
	"context"

	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// validatorCanSubmit reports whether a validator's duty txs — bridge
// attestations and price-feed prevotes/votes — can land on-chain: it must be
// bonded AND not jailed. Anything else is rejected by the chain, and price-feed
// txs are not fee-exempt, so broadcasting them anyway just spends gas on
// guaranteed failures. The reason is for logs. See oracle/PROTOCOL.md §10.
func validatorCanSubmit(v stakingtypes.Validator) (bool, string) {
	if v.IsJailed() {
		return false, "validator is jailed"
	}
	if !v.IsBonded() {
		return false, "validator is not bonded (" + v.GetStatus().String() + ")"
	}
	return true, ""
}

// queryValidatorCanSubmit looks the validator up and applies
// validatorCanSubmit. A lookup error (including "not a validator at all") is
// returned as-is; callers must treat it as "do not broadcast".
func queryValidatorCanSubmit(ctx context.Context, q stakingtypes.QueryClient, valoper string) (bool, string, error) {
	resp, err := q.Validator(ctx, &stakingtypes.QueryValidatorRequest{ValidatorAddr: valoper})
	if err != nil {
		return false, "validator lookup failed: " + err.Error(), err
	}
	ok, reason := validatorCanSubmit(resp.Validator)
	return ok, reason, nil
}
