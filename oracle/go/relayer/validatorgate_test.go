package relayer

import (
	"testing"

	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

func TestValidatorCanSubmit(t *testing.T) {
	cases := []struct {
		name   string
		status stakingtypes.BondStatus
		jailed bool
		want   bool
		reason string
	}{
		{"bonded, not jailed", stakingtypes.Bonded, false, true, ""},
		{"jailed while still marked bonded", stakingtypes.Bonded, true, false, "validator is jailed"},
		{"jailed and unbonding (the usual jailed state)", stakingtypes.Unbonding, true, false, "validator is jailed"},
		{"unbonding, not jailed", stakingtypes.Unbonding, false, false, "validator is not bonded (BOND_STATUS_UNBONDING)"},
		{"unbonded, not jailed", stakingtypes.Unbonded, false, false, "validator is not bonded (BOND_STATUS_UNBONDED)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := validatorCanSubmit(stakingtypes.Validator{Status: tc.status, Jailed: tc.jailed})
			if got != tc.want || reason != tc.reason {
				t.Fatalf("validatorCanSubmit = (%v, %q), want (%v, %q)", got, reason, tc.want, tc.reason)
			}
		})
	}
}
