package types_test

import (
	"testing"

	"steemvm/x/steembridge/types"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"
)

func TestGenesisState_Validate(t *testing.T) {
	tests := []struct {
		desc     string
		genState *types.GenesisState
		valid    bool
	}{
		{
			desc:     "default is valid",
			genState: types.DefaultGenesis(),
			valid:    true,
		},
		{
			desc: "valid genesis state",
			genState: &types.GenesisState{
				Params:            types.DefaultParams(),
				DepositList:       []types.Deposit{{Id: 0, Txid: "a"}, {Id: 1, Txid: "b"}},
				DepositCount:      2,
				WithdrawalList:    []types.Withdrawal{{Id: 0, AmountAsteem: math.ZeroInt()}, {Id: 1, AmountAsteem: math.ZeroInt()}},
				WithdrawalCount:   2,
				TotalMintedAsteem: math.ZeroInt(),
				TotalBurnedAsteem: math.ZeroInt(),
			},
			valid: true,
		}, {
			desc: "duplicated deposit",
			genState: &types.GenesisState{
				DepositList: []types.Deposit{
					{
						Id: 0,
					},
					{
						Id: 0,
					},
				},
				WithdrawalList: []types.Withdrawal{{Id: 0}, {Id: 1}}, WithdrawalCount: 2,
			}, valid: false,
		}, {
			desc: "invalid deposit count",
			genState: &types.GenesisState{
				DepositList: []types.Deposit{
					{
						Id: 1,
					},
				},
				DepositCount:   0,
				WithdrawalList: []types.Withdrawal{{Id: 0}, {Id: 1}}, WithdrawalCount: 2,
			}, valid: false,
		}, {
			desc: "duplicated withdrawal",
			genState: &types.GenesisState{
				WithdrawalList: []types.Withdrawal{
					{
						Id: 0,
					},
					{
						Id: 0,
					},
				},
			},
			valid: false,
		}, {
			desc: "invalid withdrawal count",
			genState: &types.GenesisState{
				WithdrawalList: []types.Withdrawal{
					{
						Id: 1,
					},
				},
				WithdrawalCount: 0,
			},
			valid: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			err := tc.genState.Validate()
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
