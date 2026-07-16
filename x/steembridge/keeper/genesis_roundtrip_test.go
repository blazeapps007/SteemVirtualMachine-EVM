package keeper_test

import (
	"errors"
	"testing"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	"steemvm/x/steembridge/types"
)

// TestGenesisRoundTrip_SecondaryIndexes verifies that InitGenesis/ExportGenesis
// round-trip not just the raw deposit/withdrawal lists (already covered by
// the scaffolded genesis_test.go) but also the hand-written secondary
// indexes that aren't part of the proto GenesisState: the (txid, opIndex)
// dedup index, the per-validator confirmation dedup set, and the status
// indexes used by the Pending/Minted queries and the expiry sweep.
func TestGenesisRoundTrip_SecondaryIndexes(t *testing.T) {
	f := initFixtureWithFakes(t)

	v1 := newTestValidator(t, 1)
	genesisState := types.GenesisState{
		Params: types.DefaultParams(),
		DepositList: []types.Deposit{
			{
				Id:               0,
				Txid:             "aaaa000000000000000000000000000000000000",
				OpIndex:          0,
				AmountMillisteem: 1000,
				Status:           types.DepositStatus_DEPOSIT_STATUS_PENDING,
				ValidatorConfirmations: []*types.Confirmation{
					{ValidatorAddress: v1.ValAddr.String(), Timestamp: 100},
				},
			},
			{
				Id:      1,
				Txid:    "bbbb000000000000000000000000000000000000",
				OpIndex: 0,
				Status:  types.DepositStatus_DEPOSIT_STATUS_MINTED,
				Minted:  true,
			},
		},
		DepositCount: 2,
		WithdrawalList: []types.Withdrawal{
			{Id: 0, AmountAsteem: math.NewInt(5), Status: types.WithdrawalStatus_WITHDRAWAL_STATUS_PENDING},
		},
		WithdrawalCount:   1,
		TotalMintedAsteem: math.NewInt(1_000_000_000_000_000),
		TotalBurnedAsteem: math.NewInt(5),
	}

	require.NoError(t, f.keeper.InitGenesis(f.ctx, genesisState))

	t.Run("dedup index resolves txid+opIndex to the right deposit", func(t *testing.T) {
		deposit, found, err := lookupDepositByTxidForTest(f, "aaaa000000000000000000000000000000000000", 0)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, uint64(0), deposit.Id)

		deposit2, found2, err := lookupDepositByTxidForTest(f, "bbbb000000000000000000000000000000000000", 0)
		require.NoError(t, err)
		require.True(t, found2)
		require.Equal(t, uint64(1), deposit2.Id)
	})

	t.Run("confirmation dedup set is rebuilt", func(t *testing.T) {
		has, err := f.keeper.DepositConfirmedBy.Has(f.ctx, collections.Join3(
			"aaaa000000000000000000000000000000000000", uint32(0), []byte(v1.ValAddr),
		))
		require.NoError(t, err)
		require.True(t, has, "the confirmation embedded in genesis must be reflected in the dedup index")
	})

	t.Run("status indexes are rebuilt", func(t *testing.T) {
		hasPending, err := f.keeper.DepositByStatus.Has(f.ctx, collections.Join(int32(types.DepositStatus_DEPOSIT_STATUS_PENDING), uint64(0)))
		require.NoError(t, err)
		require.True(t, hasPending)

		hasMinted, err := f.keeper.DepositByStatus.Has(f.ctx, collections.Join(int32(types.DepositStatus_DEPOSIT_STATUS_MINTED), uint64(1)))
		require.NoError(t, err)
		require.True(t, hasMinted)

		hasWithdrawalPending, err := f.keeper.WithdrawalByStatus.Has(f.ctx, collections.Join(int32(types.WithdrawalStatus_WITHDRAWAL_STATUS_PENDING), uint64(0)))
		require.NoError(t, err)
		require.True(t, hasWithdrawalPending)
	})

	exported, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.True(t, genesisState.TotalMintedAsteem.Equal(exported.TotalMintedAsteem))
	require.True(t, genesisState.TotalBurnedAsteem.Equal(exported.TotalBurnedAsteem))
	require.Len(t, exported.DepositList, 2)
	require.Len(t, exported.WithdrawalList, 1)
}

func lookupDepositByTxidForTest(f *fixtureWithFakes, txid string, opIndex uint32) (types.Deposit, bool, error) {
	id, err := f.keeper.DepositByTxid.Get(f.ctx, collections.Join(txid, opIndex))
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return types.Deposit{}, false, nil
		}
		return types.Deposit{}, false, err
	}
	deposit, err := f.keeper.Deposit.Get(f.ctx, id)
	if err != nil {
		return types.Deposit{}, false, err
	}
	return deposit, true, nil
}
