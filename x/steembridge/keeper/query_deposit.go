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

func (q queryServer) ListDeposit(ctx context.Context, req *types.QueryAllDepositRequest) (*types.QueryAllDepositResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	deposits, pageRes, err := query.CollectionPaginate(
		ctx,
		q.k.Deposit,
		req.Pagination,
		func(_ uint64, value types.Deposit) (types.Deposit, error) {
			return value, nil
		},
	)

	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAllDepositResponse{Deposit: deposits, Pagination: pageRes}, nil
}

func (q queryServer) GetDeposit(ctx context.Context, req *types.QueryGetDepositRequest) (*types.QueryGetDepositResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	deposit, err := q.k.Deposit.Get(ctx, req.Id)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return nil, sdkerrors.ErrKeyNotFound
		}

		return nil, status.Error(codes.Internal, "internal error")
	}

	return &types.QueryGetDepositResponse{Deposit: deposit}, nil
}
