package keeper

import (
	"context"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"steemvm/x/steembridge/types"
)

func (q queryServer) NameRegistrationByTxid(ctx context.Context, req *types.QueryNameRegistrationByTxidRequest) (*types.QueryNameRegistrationByTxidResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	registration, found, err := q.k.lookupNameRegistrationByTxid(ctx, req.Txid, req.OpIndex)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if !found {
		return nil, sdkerrors.ErrKeyNotFound
	}

	return &types.QueryNameRegistrationByTxidResponse{Registration: registration}, nil
}
