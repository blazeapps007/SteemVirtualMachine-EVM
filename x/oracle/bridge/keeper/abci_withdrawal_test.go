package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"steemvm/x/oracle/bridge/keeper"
	"steemvm/x/oracle/bridge/types"
)

// TestRefundExpiredWithdrawals_AutoRefundsAfterTimeout verifies the
// no-attestation-required timeout refund: a REQUESTED withdrawal whose
// WithdrawalTimeoutBlocks window elapses without reaching the payout
// threshold is automatically re-minted and returned to the sender, and a
// payout attestation that arrives afterward is a benign no-op that can never
// flip the withdrawal back to PROCESSED or pay out twice.
func TestRefundExpiredWithdrawals_AutoRefundsAfterTimeout(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridgeOut(t, f) // fee = 0, so net == gross for clean assertions

	params, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	params.WithdrawalTimeoutBlocks = 10
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))

	ms := keeper.NewMsgServerImpl(f.keeper)
	sender := sdk.AccAddress(make([]byte, 20))
	fundSender(t, f, sender, types.MillisteemToAsteem(1000))

	_, err = ms.BridgeOut(f.ctx, &types.MsgBridgeOut{
		Sender:                  sender.String(),
		DestinationSteemAccount: "alice",
		AmountAsteem:            types.MillisteemToAsteem(1000),
		Memo:                    "svm-out",
	})
	require.NoError(t, err) // withdrawal id 0, REQUESTED
	require.True(t, f.bankKeeper.balances[sender.String()].AmountOf(sdk.DefaultBondDenom).IsZero(), "sender fully spent")

	// Not yet timed out: the sweep is a no-op.
	sdkCtx := sdk.UnwrapSDKContext(f.ctx).WithBlockHeight(5)
	require.NoError(t, f.keeper.RefundExpiredWithdrawals(sdkCtx))
	genesis, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.Equal(t, types.WithdrawalStatus_WITHDRAWAL_STATUS_REQUESTED, genesis.WithdrawalList[0].Status)

	// Advance past the timeout window.
	sdkCtx = sdk.UnwrapSDKContext(f.ctx).WithBlockHeight(int64(params.WithdrawalTimeoutBlocks) + 1)
	require.NoError(t, f.keeper.RefundExpiredWithdrawals(sdkCtx))

	genesis, err = f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	w := genesis.WithdrawalList[0]
	require.Equal(t, types.WithdrawalStatus_WITHDRAWAL_STATUS_REFUNDED, w.Status)
	require.NotZero(t, w.RefundedAt)
	require.True(t, types.MillisteemToAsteem(1000).Equal(f.bankKeeper.balances[sender.String()].AmountOf(sdk.DefaultBondDenom)),
		"sender refunded the full net amount, no attestation required")

	totals, err := f.keeper.Totals.Get(f.ctx)
	require.NoError(t, err)
	require.True(t, types.MillisteemToAsteem(1000).Equal(totals.TotalMintedAsteem), "a refund is tracked as a mint")

	// A late payout attestation after refund must be a benign no-op: no
	// error, no re-processing, no second payout, status stays REFUNDED.
	v1 := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)
	_, err = ms.AttestWithdrawalPayout(sdkCtx, &types.MsgAttestWithdrawalPayout{
		Validator:      v1.AccAddr,
		WithdrawalId:   0,
		SteemTxid:      "latepayouttxid0000000000000000000000000000",
		OpIndex:        0,
		SteemBlock:     123,
		SteemTimestamp: "2026-01-01T00:00:00",
	})
	require.NoError(t, err, "a late attestation after refund is benign, not an error")

	genesis, err = f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.Equal(t, types.WithdrawalStatus_WITHDRAWAL_STATUS_REFUNDED, genesis.WithdrawalList[0].Status,
		"status must stay REFUNDED, never flip to PROCESSED after the fact")
	require.True(t, types.MillisteemToAsteem(1000).Equal(f.bankKeeper.balances[sender.String()].AmountOf(sdk.DefaultBondDenom)),
		"no second payout from the late attestation")
}

// TestRefundExpiredWithdrawals_NotYetTimedOutSurvives mirrors
// TestExpireDeposits_NotYetTimedOutSurvives for withdrawals.
func TestRefundExpiredWithdrawals_NotYetTimedOutSurvives(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridgeOut(t, f)

	params, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	params.WithdrawalTimeoutBlocks = 100
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))

	ms := keeper.NewMsgServerImpl(f.keeper)
	sender := sdk.AccAddress(make([]byte, 20))
	fundSender(t, f, sender, types.MillisteemToAsteem(1000))
	_, err = ms.BridgeOut(f.ctx, &types.MsgBridgeOut{
		Sender:                  sender.String(),
		DestinationSteemAccount: "alice",
		AmountAsteem:            types.MillisteemToAsteem(1000),
		Memo:                    "svm-out",
	})
	require.NoError(t, err)

	sdkCtx := sdk.UnwrapSDKContext(f.ctx).WithBlockHeight(50)
	require.NoError(t, f.keeper.RefundExpiredWithdrawals(sdkCtx))

	genesis, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.Equal(t, types.WithdrawalStatus_WITHDRAWAL_STATUS_REQUESTED, genesis.WithdrawalList[0].Status)
}
