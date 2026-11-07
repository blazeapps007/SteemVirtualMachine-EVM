package keeper_test

import (
	"strings"
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"steemvm/x/oracle/bridge/keeper"
	"steemvm/x/oracle/bridge/types"
)

func enableBridgeOut(t *testing.T, f *fixtureWithFakes) {
	t.Helper()
	params, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	params.BridgeOutEnabled = true
	params.BridgeFeeBps = 0
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))
}

func fundSender(t *testing.T, f *fixtureWithFakes, sender sdk.AccAddress, amount math.Int) {
	t.Helper()
	f.bankKeeper.balances[sender.String()] = sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, amount))
	// The coins came in over the bridge, as every coin does on a backed chain.
	totals, err := f.keeper.Totals.Get(f.ctx)
	require.NoError(t, err)
	totals.TotalMintedAsteem = totals.TotalMintedAsteem.Add(amount)
	require.NoError(t, f.keeper.Totals.Set(f.ctx, totals))
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
	require.Equal(t, types.WithdrawalStatus_WITHDRAWAL_STATUS_REQUESTED, w.Status)
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

// The EVM precompile calls the msg server directly, so these rules must hold
// there, not only in ValidateBasic.
func TestBridgeOut_RefusesTheGatewayAndBadMemos(t *testing.T) {
	tests := map[string]struct {
		destination, memo string
	}{
		"gateway as destination": {types.GatewayAccount, "svm-deposit steem1anything"},
		"memo too long":          {"alice", strings.Repeat("m", types.MaxWithdrawalMemoBytes+1)},
		"control character":      {"alice", "tag" + string(rune(0x0a))},
		"invalid utf-8":          {"alice", string([]byte{0xff, 0xfe})},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := initFixtureWithFakes(t)
			enableBridgeOut(t, f)
			ms := keeper.NewMsgServerImpl(f.keeper)

			sender := sdk.AccAddress(make([]byte, 20))
			fundSender(t, f, sender, types.MillisteemToAsteem(1000))
			_, err := ms.BridgeOut(f.ctx, &types.MsgBridgeOut{
				Sender: sender.String(), DestinationSteemAccount: tc.destination,
				AmountAsteem: types.MillisteemToAsteem(1000), Memo: tc.memo,
			})
			require.Error(t, err)
		})
	}
}

// The memo is stored exactly as given: the Steem payout must carry the same one.
func TestBridgeOut_KeepsTheMemoExactly(t *testing.T) {
	for _, memo := range []string{"", "exchange-tag-42", "  spaced  ", "svm-withdrawal 7", "#tag", "日本語", strings.Repeat("m", types.MaxWithdrawalMemoBytes)} {
		f := initFixtureWithFakes(t)
		enableBridgeOut(t, f)
		ms := keeper.NewMsgServerImpl(f.keeper)

		sender := sdk.AccAddress(make([]byte, 20))
		fundSender(t, f, sender, types.MillisteemToAsteem(1000))
		_, err := ms.BridgeOut(f.ctx, &types.MsgBridgeOut{
			Sender: sender.String(), DestinationSteemAccount: "alice",
			AmountAsteem: types.MillisteemToAsteem(1000), Memo: memo,
		})
		require.NoError(t, err)
		w, err := f.keeper.Withdrawal.Get(f.ctx, 0)
		require.NoError(t, err)
		require.Equal(t, memo, w.Memo)
	}
}

// Holding coins isn't enough: bridge-out can never pay out more than came in.
func TestBridgeOut_LimitedToWhatWasBridgedIn(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridgeOut(t, f)
	params, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	params.BridgeFeeBps = 0
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))
	ms := keeper.NewMsgServerImpl(f.keeper)

	// The sender holds 5,000 but only 1,000 was ever bridged in.
	sender := sdk.AccAddress(make([]byte, 20))
	f.bankKeeper.balances[sender.String()] = sdk.NewCoins(
		sdk.NewCoin(sdk.DefaultBondDenom, types.MillisteemToAsteem(5000)),
		sdk.NewCoin(types.DenomForAsset(types.BridgeAsset_BRIDGE_ASSET_SBD), types.MillisteemToAsteem(5000)),
	)
	totals, err := f.keeper.Totals.Get(f.ctx)
	require.NoError(t, err)
	totals.TotalMintedAsteem = types.MillisteemToAsteem(1000)
	require.NoError(t, f.keeper.Totals.Set(f.ctx, totals))

	out := func(milli uint64, asset types.BridgeAsset) error {
		_, err := ms.BridgeOut(f.ctx, &types.MsgBridgeOut{
			Sender: sender.String(), DestinationSteemAccount: "alice",
			AmountAsteem: types.MillisteemToAsteem(milli), Asset: asset,
		})
		return err
	}

	require.ErrorIs(t, out(1001, types.BridgeAsset_BRIDGE_ASSET_STEEM), types.ErrExceedsBridgedAmount)
	require.True(t, types.MillisteemToAsteem(5000).Equal(f.bankKeeper.balances[sender.String()].AmountOf(sdk.DefaultBondDenom)), "a refused bridge-out takes nothing")

	require.NoError(t, out(1000, types.BridgeAsset_BRIDGE_ASSET_STEEM), "exactly what came in is allowed")
	require.ErrorIs(t, out(1, types.BridgeAsset_BRIDGE_ASSET_STEEM), types.ErrExceedsBridgedAmount, "and then nothing more")

	// STEEM bridged in says nothing about SBD.
	require.ErrorIs(t, out(1, types.BridgeAsset_BRIDGE_ASSET_SBD), types.ErrExceedsBridgedAmount)
}

// A refund puts coins back in circulation, so they can be bridged out again.
func TestBridgeOut_RefundRestoresTheLimit(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridgeOut(t, f)
	params, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	params.WithdrawalTimeoutBlocks = 10
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))
	ms := keeper.NewMsgServerImpl(f.keeper)

	sender := sdk.AccAddress(make([]byte, 20))
	fundSender(t, f, sender, types.MillisteemToAsteem(1000))
	out := func(ctx sdk.Context, milli math.Int) error {
		_, err := ms.BridgeOut(ctx, &types.MsgBridgeOut{Sender: sender.String(), DestinationSteemAccount: "alice", AmountAsteem: milli})
		return err
	}
	require.NoError(t, out(sdk.UnwrapSDKContext(f.ctx), types.MillisteemToAsteem(1000)))

	later := sdk.UnwrapSDKContext(f.ctx).WithBlockHeight(int64(params.WithdrawalTimeoutBlocks) + 1)
	require.NoError(t, f.keeper.RefundExpiredWithdrawals(later))

	balance := f.bankKeeper.balances[sender.String()].AmountOf(sdk.DefaultBondDenom)
	require.True(t, balance.IsPositive())
	require.NoError(t, out(later, balance), "the refunded coins can go out again")
}
