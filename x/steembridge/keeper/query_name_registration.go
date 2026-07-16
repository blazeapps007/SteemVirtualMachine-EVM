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

func (q queryServer) NameRegistration(ctx context.Context, req *types.QueryNameRegistrationRequest) (*types.QueryNameRegistrationResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	registration, err := q.k.NameRegistration.Get(ctx, req.Id)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return nil, sdkerrors.ErrKeyNotFound
		}
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryNameRegistrationResponse{Registration: registration}, nil
}
