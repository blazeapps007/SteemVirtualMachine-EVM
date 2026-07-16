package keeper

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"steemvm/x/steembridge/types"
)

// BridgeStatistics is the peg audit query: net_outstanding must equal the
// gateway account's Steem balance net of paid-out withdrawals, and exposing
// it on-chain is what makes the peg publicly reconcilable.
func (q queryServer) BridgeStatistics(ctx context.Context, req *types.QueryBridgeStatisticsRequest) (*types.QueryBridgeStatisticsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	totals, err := q.k.Totals.Get(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryBridgeStatisticsResponse{
		TotalMintedAsteem: totals.TotalMintedAsteem,
		TotalBurnedAsteem: totals.TotalBurnedAsteem,
		NetOutstanding:    totals.TotalMintedAsteem.Sub(totals.TotalBurnedAsteem),
	}, nil
}
