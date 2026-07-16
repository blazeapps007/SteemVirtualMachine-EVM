package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/types/query"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"steemvm/x/steembridge/types"
)

func (q queryServer) PendingDeposits(ctx context.Context, req *types.QueryPendingDepositsRequest) (*types.QueryPendingDepositsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	deposits, pageRes, err := query.CollectionPaginate(
		ctx,
		q.k.DepositByStatus,
		req.Pagination,
		func(key collections.Pair[int32, uint64], _ collections.NoValue) (types.Deposit, error) {
			return q.k.Deposit.Get(ctx, key.K2())
		},
		query.WithCollectionPaginationPairPrefix[int32, uint64](int32(types.DepositStatus_DEPOSIT_STATUS_PENDING)),
	)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryPendingDepositsResponse{Deposits: deposits, Pagination: pageRes}, nil
}
