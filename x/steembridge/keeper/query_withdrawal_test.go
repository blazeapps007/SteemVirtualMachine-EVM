package keeper_test

import (
	"context"
	"strconv"
	"testing"

	"cosmossdk.io/math"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"steemvm/x/steembridge/keeper"
	"steemvm/x/steembridge/types"
)

func createNWithdrawal(keeper keeper.Keeper, ctx context.Context, n int) []types.Withdrawal {
	items := make([]types.Withdrawal, n)
	for i := range items {
		iu := uint64(i)
		items[i].Id = iu
		items[i].Sender = strconv.Itoa(i)
		items[i].DestinationSteemAccount = strconv.Itoa(i)
		items[i].AmountAsteem = math.NewInt(int64(i))
		items[i].AmountMillisteem = uint64(i)
		items[i].Memo = strconv.Itoa(i)
		items[i].BurnTxHash = strconv.Itoa(i)
		items[i].Status = types.WithdrawalStatus(i % 4)
		items[i].CreatedAt = uint64(i)
		_ = keeper.Withdrawal.Set(ctx, iu, items[i])
		_ = keeper.WithdrawalSeq.Set(ctx, iu)
	}
	return items
}

func TestWithdrawalQuerySingle(t *testing.T) {
	f := initFixture(t)
	qs := keeper.NewQueryServerImpl(f.keeper)
	msgs := createNWithdrawal(f.keeper, f.ctx, 2)
	tests := []struct {
		desc     string
		request  *types.QueryGetWithdrawalRequest
		response *types.QueryGetWithdrawalResponse
		err      error
	}{
		{
			desc:     "First",
			request:  &types.QueryGetWithdrawalRequest{Id: msgs[0].Id},
			response: &types.QueryGetWithdrawalResponse{Withdrawal: msgs[0]},
		},
		{
			desc:     "Second",
			request:  &types.QueryGetWithdrawalRequest{Id: msgs[1].Id},
			response: &types.QueryGetWithdrawalResponse{Withdrawal: msgs[1]},
		},
		{
			desc:    "KeyNotFound",
			request: &types.QueryGetWithdrawalRequest{Id: uint64(len(msgs))},
			err:     sdkerrors.ErrKeyNotFound,
		},
		{
			desc: "InvalidRequest",
			err:  status.Error(codes.InvalidArgument, "invalid request"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			response, err := qs.GetWithdrawal(f.ctx, tc.request)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
			} else {
				require.NoError(t, err)
				require.EqualExportedValues(t, tc.response, response)
			}
		})
	}
}

func TestWithdrawalQueryPaginated(t *testing.T) {
	f := initFixture(t)
	qs := keeper.NewQueryServerImpl(f.keeper)
	msgs := createNWithdrawal(f.keeper, f.ctx, 5)

	request := func(next []byte, offset, limit uint64, total bool) *types.QueryAllWithdrawalRequest {
		return &types.QueryAllWithdrawalRequest{
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
			resp, err := qs.ListWithdrawal(f.ctx, request(nil, uint64(i), uint64(step), false))
			require.NoError(t, err)
			require.LessOrEqual(t, len(resp.Withdrawal), step)
			require.Subset(t, msgs, resp.Withdrawal)
		}
	})
	t.Run("ByKey", func(t *testing.T) {
		step := 2
		var next []byte
		for i := 0; i < len(msgs); i += step {
			resp, err := qs.ListWithdrawal(f.ctx, request(next, 0, uint64(step), false))
			require.NoError(t, err)
			require.LessOrEqual(t, len(resp.Withdrawal), step)
			require.Subset(t, msgs, resp.Withdrawal)
			next = resp.Pagination.NextKey
		}
	})
	t.Run("Total", func(t *testing.T) {
		resp, err := qs.ListWithdrawal(f.ctx, request(nil, 0, 0, true))
		require.NoError(t, err)
		require.Equal(t, len(msgs), int(resp.Pagination.Total))
		require.EqualExportedValues(t, msgs, resp.Withdrawal)
	})
	t.Run("InvalidRequest", func(t *testing.T) {
		_, err := qs.ListWithdrawal(f.ctx, nil)
		require.ErrorIs(t, err, status.Error(codes.InvalidArgument, "invalid request"))
	})
}
