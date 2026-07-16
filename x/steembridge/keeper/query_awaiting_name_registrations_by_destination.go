package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/types/query"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"steemvm/x/steembridge/types"
)

func (q queryServer) AwaitingNameRegistrationsByDestination(ctx context.Context, req *types.QueryAwaitingNameRegistrationsByDestinationRequest) (*types.QueryAwaitingNameRegistrationsByDestinationResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	addr, err := q.k.parseAddressArg(req.Address)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid address")
	}

	registrations, pageRes, err := query.CollectionPaginate(
		ctx,
		q.k.NameRegistrationAwaitingByDest,
		req.Pagination,
		func(key collections.Pair[[]byte, uint64], _ collections.NoValue) (types.NameRegistration, error) {
			return q.k.NameRegistration.Get(ctx, key.K2())
		},
		query.WithCollectionPaginationPairPrefix[[]byte, uint64](addr),
	)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAwaitingNameRegistrationsByDestinationResponse{Registrations: registrations, Pagination: pageRes}, nil
}
