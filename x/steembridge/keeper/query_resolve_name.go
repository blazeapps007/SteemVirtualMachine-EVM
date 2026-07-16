package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"steemvm/x/steembridge/types"
)

func (q queryServer) ResolveName(ctx context.Context, req *types.QueryResolveNameRequest) (*types.QueryResolveNameResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	record, err := q.k.ActiveName.Get(ctx, req.SteemAccount)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return nil, sdkerrors.ErrKeyNotFound
		}
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryResolveNameResponse{Name: record}, nil
}
