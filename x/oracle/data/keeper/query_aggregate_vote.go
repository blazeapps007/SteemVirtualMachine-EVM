package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"steemvm/x/oracle/data/types"
)

func (q queryServer) AggregateVote(ctx context.Context, req *types.QueryAggregateVoteRequest) (*types.QueryAggregateVoteResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	valAddr, err := sdk.ValAddressFromBech32(req.Validator)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	vote, err := q.k.Vote.Get(ctx, valAddr.Bytes())
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return nil, status.Error(codes.NotFound, types.ErrNoAggregateVote.Error())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAggregateVoteResponse{AggregateVote: vote}, nil
}
