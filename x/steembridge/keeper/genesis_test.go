package keeper_test

import (
	"testing"

	"steemvm/x/steembridge/types"

	"github.com/stretchr/testify/require"
)

func TestGenesis(t *testing.T) {
	genesisState := types.GenesisState{
		Params:          types.DefaultParams(),
		DepositList:     []types.Deposit{{Id: 0}, {Id: 1}},
		DepositCount:    2,
		WithdrawalList:  []types.Withdrawal{{Id: 0}, {Id: 1}},
		WithdrawalCount: 2,
	}
	f := initFixture(t)
	err := f.keeper.InitGenesis(f.ctx, genesisState)
	require.NoError(t, err)
	got, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.NotNil(t, got)

	require.EqualExportedValues(t, genesisState.Params, got.Params)
	require.EqualExportedValues(t, genesisState.DepositList, got.DepositList)
	require.Equal(t, genesisState.DepositCount, got.DepositCount)
	require.EqualExportedValues(t, genesisState.WithdrawalList, got.WithdrawalList)
	require.Equal(t, genesisState.WithdrawalCount, got.WithdrawalCount)

}
