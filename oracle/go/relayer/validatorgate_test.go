package relayer

import (
	"context"
	"errors"
	"testing"

	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeStakingQuery struct {
	stakingtypes.QueryClient
	resp *stakingtypes.QueryValidatorResponse
	err  error
}

func (f fakeStakingQuery) Validator(context.Context, *stakingtypes.QueryValidatorRequest, ...grpc.CallOption) (*stakingtypes.QueryValidatorResponse, error) {
	return f.resp, f.err
}

func TestQueryValidatorCanSubmit(t *testing.T) {
	ctx := context.Background()

	// Not a validator: a definitive answer (no error), same as Python/JS.
	ok, reason, err := queryValidatorCanSubmit(ctx, fakeStakingQuery{err: status.Error(codes.NotFound, "validator not found")}, "v")
	if ok || reason != "not a validator" || err != nil {
		t.Fatalf("NotFound: got (%v, %q, %v), want (false, \"not a validator\", nil)", ok, reason, err)
	}

	// Node unreachable: not definitive — the error comes back so callers retry.
	ok, _, err = queryValidatorCanSubmit(ctx, fakeStakingQuery{err: errors.New("connection refused")}, "v")
	if ok || err == nil {
		t.Fatalf("lookup failure: got (%v, _, %v), want (false, _, non-nil)", ok, err)
	}

	// Jailed: definitive, no error.
	jailed := &stakingtypes.QueryValidatorResponse{Validator: stakingtypes.Validator{Status: stakingtypes.Unbonded, Jailed: true}}
	ok, reason, err = queryValidatorCanSubmit(ctx, fakeStakingQuery{resp: jailed}, "v")
	if ok || reason != "validator is jailed" || err != nil {
		t.Fatalf("jailed: got (%v, %q, %v)", ok, reason, err)
	}
}

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
