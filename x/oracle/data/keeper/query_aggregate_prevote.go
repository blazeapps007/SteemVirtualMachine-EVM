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

func (q queryServer) AggregatePrevote(ctx context.Context, req *types.QueryAggregatePrevoteRequest) (*types.QueryAggregatePrevoteResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	valAddr, err := sdk.ValAddressFromBech32(req.Validator)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	prevote, err := q.k.Prevote.Get(ctx, valAddr.Bytes())
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return nil, status.Error(codes.NotFound, types.ErrNoAggregatePrevote.Error())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAggregatePrevoteResponse{AggregatePrevote: prevote}, nil
}
