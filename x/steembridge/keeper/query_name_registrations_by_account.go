package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/types/query"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"steemvm/x/steembridge/types"
)

func (q queryServer) NameRegistrationsByAccount(ctx context.Context, req *types.QueryNameRegistrationsByAccountRequest) (*types.QueryNameRegistrationsByAccountResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	registrations, pageRes, err := query.CollectionPaginate(
		ctx,
		q.k.NameRegistrationByAccount,
		req.Pagination,
		func(key collections.Pair[string, uint64], _ collections.NoValue) (types.NameRegistration, error) {
			return q.k.NameRegistration.Get(ctx, key.K2())
		},
		query.WithCollectionPaginationPairPrefix[string, uint64](req.SteemAccount),
	)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryNameRegistrationsByAccountResponse{Registrations: registrations, Pagination: pageRes}, nil
}
