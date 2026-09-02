package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"steemvm/x/oracle/bridge/keeper"
	"steemvm/x/oracle/bridge/types"
)

// TestBridgeOut_AppliesFee verifies the 0.25% bridge fee: the withdrawal records
// the NET payout + fee, the fee is routed to bridge_reward, and the net goes to
// the black hole (to be swept-burned).
func TestBridgeOut_AppliesFee(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridgeOut(t, f)
	params, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	params.BridgeFeeBps = 25 // re-enable the fee (the helper zeroes it)
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))

	ms := keeper.NewMsgServerImpl(f.keeper)
	sender := sdk.AccAddress(make([]byte, 20))
	fundSender(t, f, sender, types.MillisteemToAsteem(50000))

	_, err = ms.BridgeOut(f.ctx, &types.MsgBridgeOut{
		Sender:                  sender.String(),
		DestinationSteemAccount: "alice",
		AmountAsteem:            types.MillisteemToAsteem(10000),
		Memo:                    "svm-out",
	})
	require.NoError(t, err)

	genesis, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.Len(t, genesis.WithdrawalList, 1)
	w := genesis.WithdrawalList[0]
	require.Equal(t, uint64(9975), w.AmountMillisteem, "net payout = 99.75%")
	require.Equal(t, uint64(25), w.FeeMillisteem, "fee = 0.25%")

	require.True(t, types.MillisteemToAsteem(25).Equal(f.bankKeeper.balances[types.BridgeRewardModuleName].AmountOf(sdk.DefaultBondDenom)), "fee -> bridge_reward")
	require.True(t, types.MillisteemToAsteem(9975).Equal(f.bankKeeper.balances[types.BlackHoleModuleName].AmountOf(sdk.DefaultBondDenom)), "net -> black hole (pre-sweep)")
}

// TestAttestDeposit_SBD verifies an SBD deposit mints asbd (not asteem) and
// credits the asbd totals.
func TestAttestDeposit_SBD(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridge(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	v1 := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)

	msg := baseDepositMsg(v1, "5555000000000000000000000000000000000000", 0)
	msg.Asset = types.BridgeAsset_BRIDGE_ASSET_SBD
	_, err := ms.AttestDeposit(f.ctx, msg)
	require.NoError(t, err)

	genesis, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.Equal(t, types.DepositStatus_DEPOSIT_STATUS_MINTED, genesis.DepositList[0].Status)
	require.Equal(t, types.BridgeAsset_BRIDGE_ASSET_SBD, genesis.DepositList[0].Asset)

	totals, err := f.keeper.Totals.Get(f.ctx)
	require.NoError(t, err)
	require.True(t, types.MillisteemToAsteem(msg.AmountMillisteem).Equal(totals.TotalMintedAsbd), "SBD deposit credits asbd totals")
	require.True(t, totals.TotalMintedAsteem.IsZero(), "no asteem minted for an SBD deposit")
}

// TestAttestWithdrawalPayout_MarksProcessed verifies the 2/3 payout-attestation
// flow flips a REQUESTED withdrawal to PROCESSED with the Steem txid recorded.
func TestAttestWithdrawalPayout_MarksProcessed(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridgeOut(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	sender := sdk.AccAddress(make([]byte, 20))
	fundSender(t, f, sender, types.MillisteemToAsteem(5000))
	_, err := ms.BridgeOut(f.ctx, &types.MsgBridgeOut{
		Sender:                  sender.String(),
		DestinationSteemAccount: "alice",
		AmountAsteem:            types.MillisteemToAsteem(1000),
		Memo:                    "svm-out",
	})
	require.NoError(t, err) // creates withdrawal id 0, REQUESTED

	v1 := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true) // sole bonded => 100% >= 2/3

	const payoutTxid = "payouttxid00000000000000000000000000000000"
	_, err = ms.AttestWithdrawalPayout(f.ctx, &types.MsgAttestWithdrawalPayout{
		Validator:        v1.AccAddr,
		WithdrawalId:     0,
		SteemTxid:        payoutTxid,
		OpIndex:          0,
		SteemBlock:       123,
		SteemTimestamp:   "2026-01-01T00:00:00",
		AmountMillisteem: 1000,
		Asset:            types.BridgeAsset_BRIDGE_ASSET_STEEM,
	})
	require.NoError(t, err)

	genesis, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	w := genesis.WithdrawalList[0]
	require.Equal(t, types.WithdrawalStatus_WITHDRAWAL_STATUS_PROCESSED, w.Status)
	require.Equal(t, payoutTxid, w.SteemPayoutTxid)
	require.Len(t, w.ValidatorConfirmations, 1)
}

// TestAttestWithdrawalPayout_AmountMismatch verifies a validator reporting the
// wrong observed amount is a benign no-op: no confirmation recorded, the
// withdrawal stays REQUESTED, and the mismatch is auditable via the emitted
// event — this is what stops a wrong-amount manual relay on Steem from ever
// being confirmed as a valid payout.
func TestAttestWithdrawalPayout_AmountMismatch(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridgeOut(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	sender := sdk.AccAddress(make([]byte, 20))
	fundSender(t, f, sender, types.MillisteemToAsteem(5000))
	_, err := ms.BridgeOut(f.ctx, &types.MsgBridgeOut{
		Sender:                  sender.String(),
		DestinationSteemAccount: "alice",
		AmountAsteem:            types.MillisteemToAsteem(1000),
		Memo:                    "svm-out",
	})
	require.NoError(t, err) // creates withdrawal id 0, REQUESTED, expects 1000 millisteem STEEM

	v1 := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)

	_, err = ms.AttestWithdrawalPayout(f.ctx, &types.MsgAttestWithdrawalPayout{
		Validator:        v1.AccAddr,
		WithdrawalId:     0,
		SteemTxid:        "payouttxid00000000000000000000000000000000",
		OpIndex:          0,
		SteemBlock:       123,
		SteemTimestamp:   "2026-01-01T00:00:00",
		AmountMillisteem: 1, // wrong: withdrawal expects 1000
		Asset:            types.BridgeAsset_BRIDGE_ASSET_STEEM,
	})
	require.NoError(t, err, "a mismatch is a benign no-op for the tx, not a hard failure")

	genesis, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	w := genesis.WithdrawalList[0]
	require.Equal(t, types.WithdrawalStatus_WITHDRAWAL_STATUS_REQUESTED, w.Status, "stays REQUESTED")
	require.Empty(t, w.SteemPayoutTxid, "payout facts never fixed by a mismatched attestation")
	require.Empty(t, w.ValidatorConfirmations, "mismatch doesn't count toward confirmation")
}

// TestAttestWithdrawalPayout_AssetMismatch mirrors the amount-mismatch case
// for the asset field — a validator reporting SBD paid out for a
// STEEM-denominated withdrawal (or vice versa) is equally rejected.
func TestAttestWithdrawalPayout_AssetMismatch(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridgeOut(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	sender := sdk.AccAddress(make([]byte, 20))
	fundSender(t, f, sender, types.MillisteemToAsteem(5000))
	_, err := ms.BridgeOut(f.ctx, &types.MsgBridgeOut{
		Sender:                  sender.String(),
		DestinationSteemAccount: "alice",
		AmountAsteem:            types.MillisteemToAsteem(1000),
		Memo:                    "svm-out",
		Asset:                   types.BridgeAsset_BRIDGE_ASSET_STEEM,
	})
	require.NoError(t, err) // creates withdrawal id 0, REQUESTED, expects STEEM

	v1 := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)

	_, err = ms.AttestWithdrawalPayout(f.ctx, &types.MsgAttestWithdrawalPayout{
		Validator:        v1.AccAddr,
		WithdrawalId:     0,
		SteemTxid:        "payouttxid00000000000000000000000000000000",
		OpIndex:          0,
		SteemBlock:       123,
		SteemTimestamp:   "2026-01-01T00:00:00",
		AmountMillisteem: 1000,
		Asset:            types.BridgeAsset_BRIDGE_ASSET_SBD, // wrong: withdrawal expects STEEM
	})
	require.NoError(t, err, "a mismatch is a benign no-op for the tx, not a hard failure")

	genesis, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	w := genesis.WithdrawalList[0]
	require.Equal(t, types.WithdrawalStatus_WITHDRAWAL_STATUS_REQUESTED, w.Status, "stays REQUESTED")
	require.Empty(t, w.ValidatorConfirmations, "mismatch doesn't count toward confirmation")
}
