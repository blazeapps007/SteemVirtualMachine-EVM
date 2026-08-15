package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"steemvm/x/oracle/data/types"
)

func (q queryServer) ExchangeRate(ctx context.Context, req *types.QueryExchangeRateRequest) (*types.QueryExchangeRateResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	rate, err := q.k.ExchangeRate.Get(ctx, req.Pair)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return nil, sdkerrors.ErrKeyNotFound
		}
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryExchangeRateResponse{ExchangeRate: rate}, nil
}
