package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/types/query"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"steemvm/x/steembridge/types"
)

func (q queryServer) NamesByAddress(ctx context.Context, req *types.QueryNamesByAddressRequest) (*types.QueryNamesByAddressResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	addr, err := q.k.parseAddressArg(req.Address)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid address")
	}

	names, pageRes, err := query.CollectionPaginate(
		ctx,
		q.k.ActiveNameByAddress,
		req.Pagination,
		func(key collections.Pair[[]byte, string], _ collections.NoValue) (types.NameRecord, error) {
			return q.k.ActiveName.Get(ctx, key.K2())
		},
		query.WithCollectionPaginationPairPrefix[[]byte, string](addr),
	)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryNamesByAddressResponse{Names: names, Pagination: pageRes}, nil
}
