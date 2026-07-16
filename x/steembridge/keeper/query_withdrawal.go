package keeper

import (
	"context"
	"errors"

	"steemvm/x/steembridge/types"

	"cosmossdk.io/collections"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/types/query"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (q queryServer) ListWithdrawal(ctx context.Context, req *types.QueryAllWithdrawalRequest) (*types.QueryAllWithdrawalResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	withdrawals, pageRes, err := query.CollectionPaginate(
		ctx,
		q.k.Withdrawal,
		req.Pagination,
		func(_ uint64, value types.Withdrawal) (types.Withdrawal, error) {
			return value, nil
		},
	)

	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAllWithdrawalResponse{Withdrawal: withdrawals, Pagination: pageRes}, nil
}

func (q queryServer) GetWithdrawal(ctx context.Context, req *types.QueryGetWithdrawalRequest) (*types.QueryGetWithdrawalResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	withdrawal, err := q.k.Withdrawal.Get(ctx, req.Id)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return nil, sdkerrors.ErrKeyNotFound
		}

		return nil, status.Error(codes.Internal, "internal error")
	}

	return &types.QueryGetWithdrawalResponse{Withdrawal: withdrawal}, nil
}
