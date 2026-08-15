package keeper

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"steemvm/x/oracle/data/types"
)

func (q queryServer) ExchangeRates(ctx context.Context, req *types.QueryExchangeRatesRequest) (*types.QueryExchangeRatesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	rates := []types.ExchangeRate{}
	err := q.k.ExchangeRate.Walk(ctx, nil, func(_ string, elem types.ExchangeRate) (bool, error) {
		rates = append(rates, elem)
		return false, nil
	})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryExchangeRatesResponse{ExchangeRates: rates}, nil
}
