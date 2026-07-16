package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/types/query"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"steemvm/x/steembridge/types"
)

func (q queryServer) AwaitingNameRegistrations(ctx context.Context, req *types.QueryAwaitingNameRegistrationsRequest) (*types.QueryAwaitingNameRegistrationsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	registrations, pageRes, err := query.CollectionPaginate(
		ctx,
		q.k.NameRegistrationByStatus,
		req.Pagination,
		func(key collections.Pair[int32, uint64], _ collections.NoValue) (types.NameRegistration, error) {
			return q.k.NameRegistration.Get(ctx, key.K2())
		},
		query.WithCollectionPaginationPairPrefix[int32, uint64](int32(types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_AWAITING_CONFIRMATION)),
	)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAwaitingNameRegistrationsResponse{Registrations: registrations, Pagination: pageRes}, nil
}
