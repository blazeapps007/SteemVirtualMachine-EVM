package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"steemvm/x/steembridge/keeper"
	"steemvm/x/steembridge/types"
)

func enableBridgeOut(t *testing.T, f *fixtureWithFakes) {
	t.Helper()
	params, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	params.BridgeOutEnabled = true
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))
}

func fundSender(t *testing.T, f *fixtureWithFakes, sender sdk.AccAddress, amount math.Int) {
	t.Helper()
	f.bankKeeper.balances[sender.String()] = sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, amount))
}

func TestBridgeOut_ToNullIsAnOrdinaryWithdrawal(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridgeOut(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	sender := sdk.AccAddress(make([]byte, 20))
	fundSender(t, f, sender, types.MillisteemToAsteem(5000))

	msg := &types.MsgBridgeOut{
		Sender:                  sender.String(),
		DestinationSteemAccount: "null",
		AmountAsteem:            types.MillisteemToAsteem(1000),
		Memo:                    "burn",
	}
	_, err := ms.BridgeOut(f.ctx, msg)
	require.NoError(t, err, "bridging out to null is the sanctioned provable-burn path and must not be special-cased/blocked")

	genesis, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.Len(t, genesis.WithdrawalList, 1)
	w := genesis.WithdrawalList[0]
	require.Equal(t, "null", w.DestinationSteemAccount)
	require.Equal(t, types.WithdrawalStatus_WITHDRAWAL_STATUS_PENDING, w.Status)
	require.Equal(t, uint64(1000), w.AmountMillisteem)
	require.True(t, types.MillisteemToAsteem(1000).Equal(genesis.TotalBurnedAsteem))
}

func TestBridgeOut_NonMultipleOf10e15Rejected(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridgeOut(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	sender := sdk.AccAddress(make([]byte, 20))
	fundSender(t, f, sender, types.MillisteemToAsteem(5000))

	msg := &types.MsgBridgeOut{
		Sender:                  sender.String(),
		DestinationSteemAccount: "alice",
		AmountAsteem:            types.MillisteemToAsteem(1000).AddRaw(1), // one atto-steem over a whole millisteem
		Memo:                    "",
	}
	_, err := ms.BridgeOut(f.ctx, msg)
	require.ErrorIs(t, err, types.ErrInvalidAmount)
}

func TestBridgeOut_Disabled(t *testing.T) {
	f := initFixtureWithFakes(t)
	// bridge out stays disabled (DefaultParams)
	ms := keeper.NewMsgServerImpl(f.keeper)

	sender := sdk.AccAddress(make([]byte, 20))
	msg := &types.MsgBridgeOut{
		Sender:                  sender.String(),
		DestinationSteemAccount: "alice",
		AmountAsteem:            types.MillisteemToAsteem(1000),
	}
	_, err := ms.BridgeOut(f.ctx, msg)
	require.ErrorIs(t, err, types.ErrBridgeOutDisabled)
}

func TestBridgeOut_InvalidDestinationAccountRejected(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridgeOut(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	sender := sdk.AccAddress(make([]byte, 20))
	fundSender(t, f, sender, types.MillisteemToAsteem(5000))

	msg := &types.MsgBridgeOut{
		Sender:                  sender.String(),
		DestinationSteemAccount: "AB", // too short and uppercase
		AmountAsteem:            types.MillisteemToAsteem(1000),
	}
	_, err := ms.BridgeOut(f.ctx, msg)
	require.Error(t, err)
}

func TestBridgeOut_InsufficientBalanceRejected(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridgeOut(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	sender := sdk.AccAddress(make([]byte, 20)) // funded with nothing

	msg := &types.MsgBridgeOut{
		Sender:                  sender.String(),
		DestinationSteemAccount: "alice",
		AmountAsteem:            types.MillisteemToAsteem(1000),
	}
	_, err := ms.BridgeOut(f.ctx, msg)
	require.Error(t, err)
}
