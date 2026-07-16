package keeper_test

import (
	"context"
	"strconv"
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"steemvm/x/steembridge/keeper"
	"steemvm/x/steembridge/types"
)

func createNDeposit(keeper keeper.Keeper, ctx context.Context, n int) []types.Deposit {
	items := make([]types.Deposit, n)
	for i := range items {
		iu := uint64(i)
		items[i].Id = iu
		items[i].Txid = strconv.Itoa(i)
		items[i].OpIndex = uint32(i)
		items[i].SteemBlock = uint64(i)
		items[i].SteemTimestamp = strconv.Itoa(i)
		items[i].SteemSender = strconv.Itoa(i)
		items[i].GatewayAccount = strconv.Itoa(i)
		items[i].AmountMillisteem = uint64(i)
		items[i].Memo = strconv.Itoa(i)
		items[i].DerivedDestination = strconv.Itoa(i)
		items[i].DestinationType = types.DestinationType(i % 3)
		items[i].Status = types.DepositStatus(i % 4)
		items[i].Minted = true
		items[i].MintedAt = uint64(i)
		items[i].MintTxHash = strconv.Itoa(i)
		items[i].CreatedAt = uint64(i)
		_ = keeper.Deposit.Set(ctx, iu, items[i])
		_ = keeper.DepositSeq.Set(ctx, iu)
	}
	return items
}

func TestDepositQuerySingle(t *testing.T) {
	f := initFixture(t)
	qs := keeper.NewQueryServerImpl(f.keeper)
	msgs := createNDeposit(f.keeper, f.ctx, 2)
	tests := []struct {
		desc     string
		request  *types.QueryGetDepositRequest
		response *types.QueryGetDepositResponse
		err      error
	}{
		{
			desc:     "First",
			request:  &types.QueryGetDepositRequest{Id: msgs[0].Id},
			response: &types.QueryGetDepositResponse{Deposit: msgs[0]},
		},
		{
			desc:     "Second",
			request:  &types.QueryGetDepositRequest{Id: msgs[1].Id},
			response: &types.QueryGetDepositResponse{Deposit: msgs[1]},
		},
		{
			desc:    "KeyNotFound",
			request: &types.QueryGetDepositRequest{Id: uint64(len(msgs))},
			err:     sdkerrors.ErrKeyNotFound,
		},
		{
			desc: "InvalidRequest",
			err:  status.Error(codes.InvalidArgument, "invalid request"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			response, err := qs.GetDeposit(f.ctx, tc.request)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
			} else {
				require.NoError(t, err)
				require.EqualExportedValues(t, tc.response, response)
			}
		})
	}
}

func TestDepositQueryPaginated(t *testing.T) {
	f := initFixture(t)
	qs := keeper.NewQueryServerImpl(f.keeper)
	msgs := createNDeposit(f.keeper, f.ctx, 5)

	request := func(next []byte, offset, limit uint64, total bool) *types.QueryAllDepositRequest {
		return &types.QueryAllDepositRequest{
			Pagination: &query.PageRequest{
				Key:        next,
				Offset:     offset,
				Limit:      limit,
				CountTotal: total,
			},
		}
	}
	t.Run("ByOffset", func(t *testing.T) {
		step := 2
		for i := 0; i < len(msgs); i += step {
			resp, err := qs.ListDeposit(f.ctx, request(nil, uint64(i), uint64(step), false))
			require.NoError(t, err)
			require.LessOrEqual(t, len(resp.Deposit), step)
			require.Subset(t, msgs, resp.Deposit)
		}
	})
	t.Run("ByKey", func(t *testing.T) {
		step := 2
		var next []byte
		for i := 0; i < len(msgs); i += step {
			resp, err := qs.ListDeposit(f.ctx, request(next, 0, uint64(step), false))
			require.NoError(t, err)
			require.LessOrEqual(t, len(resp.Deposit), step)
			require.Subset(t, msgs, resp.Deposit)
			next = resp.Pagination.NextKey
		}
	})
	t.Run("Total", func(t *testing.T) {
		resp, err := qs.ListDeposit(f.ctx, request(nil, 0, 0, true))
		require.NoError(t, err)
		require.Equal(t, len(msgs), int(resp.Pagination.Total))
		require.EqualExportedValues(t, msgs, resp.Deposit)
	})
	t.Run("InvalidRequest", func(t *testing.T) {
		_, err := qs.ListDeposit(f.ctx, nil)
		require.ErrorIs(t, err, status.Error(codes.InvalidArgument, "invalid request"))
	})
}
