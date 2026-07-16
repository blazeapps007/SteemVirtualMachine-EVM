package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"steemvm/x/steembridge/types"
)

// ValidatorIdentity returns the Steem identity embedded in a validator's staking
// Description.details. The address argument accepts the operator
// (steemvaloper...), account (steem1...), or 0x EVM form — all the same bytes.
func (q queryServer) ValidatorIdentity(ctx context.Context, req *types.QueryValidatorIdentityRequest) (*types.QueryValidatorIdentityResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	addr, err := q.k.parseAddressArg(req.ValidatorAddress)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid validator address")
	}

	identity, err := q.k.GetValidatorIdentity(ctx, sdk.ValAddress(addr))
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}

	return &types.QueryValidatorIdentityResponse{
		SteemUsername: identity.SteemUsername,
		OwnerPubkey:   identity.OwnerPubkey,
		ActivePubkey:  identity.ActivePubkey,
		PostingPubkey: identity.PostingPubkey,
	}, nil
}
