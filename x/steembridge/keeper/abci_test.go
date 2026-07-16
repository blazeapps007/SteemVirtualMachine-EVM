package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"steemvm/x/steembridge/keeper"
	"steemvm/x/steembridge/types"
)

func TestExpireDeposits(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridge(t, f)

	params, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	params.DepositTimeoutBlocks = 10
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))

	ms := keeper.NewMsgServerImpl(f.keeper)
	v1 := newTestValidator(t, 1)
	vOther := newTestValidator(t, 9) // never confirms; keeps v1 alone under threshold
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)
	f.stakingKeeper.setValidator(vOther.ValAddr, 100, true)

	txid := "8888000000000000000000000000000000000000"
	msg := baseDepositMsg(v1, txid, 0)
	_, err = ms.SubmitSteemDeposit(f.ctx, msg)
	require.NoError(t, err)

	genesis, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.False(t, genesis.DepositList[0].Minted, "v1 alone (100/200 = 50%) must not reach the 2/3 threshold")

	// Advance past the timeout window.
	sdkCtx := sdk.UnwrapSDKContext(f.ctx).WithBlockHeight(int64(params.DepositTimeoutBlocks) + 1)
	require.NoError(t, f.keeper.ExpireDeposits(sdkCtx))

	genesis, err = f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.Equal(t, types.DepositStatus_DEPOSIT_STATUS_EXPIRED, genesis.DepositList[0].Status)

	// The (txid, opIndex) key must be resubmittable fresh after expiry: a
	// brand new deposit record must be created rather than reusing/erroring
	// on the expired one.
	_, err = ms.SubmitSteemDeposit(sdkCtx, baseDepositMsg(v1, txid, 0))
	require.NoError(t, err)

	genesis, err = f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.Len(t, genesis.DepositList, 2, "expiry must not block a fresh resubmission of the same key")
}

func TestExpireDeposits_NotYetTimedOutSurvives(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridge(t, f)

	params, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	params.DepositTimeoutBlocks = 100
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))

	ms := keeper.NewMsgServerImpl(f.keeper)
	v1 := newTestValidator(t, 1)
	vOther := newTestValidator(t, 9) // never confirms; keeps v1 alone under threshold
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)
	f.stakingKeeper.setValidator(vOther.ValAddr, 100, true)

	txid := "9999000000000000000000000000000000000000"
	_, err = ms.SubmitSteemDeposit(f.ctx, baseDepositMsg(v1, txid, 0))
	require.NoError(t, err)

	sdkCtx := sdk.UnwrapSDKContext(f.ctx).WithBlockHeight(50)
	require.NoError(t, f.keeper.ExpireDeposits(sdkCtx))

	genesis, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.Equal(t, types.DepositStatus_DEPOSIT_STATUS_PENDING, genesis.DepositList[0].Status)
}
