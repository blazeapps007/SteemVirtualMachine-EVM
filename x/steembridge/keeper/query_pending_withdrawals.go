package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/types/query"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"steemvm/x/steembridge/types"
)

func (q queryServer) PendingWithdrawals(ctx context.Context, req *types.QueryPendingWithdrawalsRequest) (*types.QueryPendingWithdrawalsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	withdrawals, pageRes, err := query.CollectionPaginate(
		ctx,
		q.k.WithdrawalByStatus,
		req.Pagination,
		func(key collections.Pair[int32, uint64], _ collections.NoValue) (types.Withdrawal, error) {
			return q.k.Withdrawal.Get(ctx, key.K2())
		},
		query.WithCollectionPaginationPairPrefix[int32, uint64](int32(types.WithdrawalStatus_WITHDRAWAL_STATUS_PENDING)),
	)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryPendingWithdrawalsResponse{Withdrawals: withdrawals, Pagination: pageRes}, nil
}
