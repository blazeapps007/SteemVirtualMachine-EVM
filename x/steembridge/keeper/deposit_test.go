package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"steemvm/x/steembridge/keeper"
	"steemvm/x/steembridge/types"
)

func enableBridge(t *testing.T, f *fixtureWithFakes) {
	t.Helper()
	params := types.DefaultParams()
	params.BridgeEnabled = true
	params.GatewayAccount = "gateway-account"
	params.BridgeConfirmationThreshold = math.LegacyMustNewDecFromStr("0.666666666666666667")
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))
}

func baseDepositMsg(validator testValidator, txid string, opIndex uint32) *types.MsgSubmitSteemDeposit {
	return &types.MsgSubmitSteemDeposit{
		Validator:        validator.AccAddr,
		Txid:             txid,
		OpIndex:          opIndex,
		SteemBlock:       100,
		SteemTimestamp:   "2024-01-01T00:00:00",
		SteemSender:      "alice",
		GatewayAccount:   "gateway-account",
		AmountMillisteem: 1000, // 1 STEEM
		Memo:             sdk.AccAddress(make([]byte, 20)).String(),
	}
}

func TestSubmitSteemDeposit_DedupCreatesOneDepositAcrossConfirmers(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridge(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	v1 := newTestValidator(t, 1)
	v2 := newTestValidator(t, 2)
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)
	f.stakingKeeper.setValidator(v2.ValAddr, 100, true)

	msg1 := baseDepositMsg(v1, "aaaa000000000000000000000000000000000000", 0)
	_, err := ms.SubmitSteemDeposit(f.ctx, msg1)
	require.NoError(t, err)

	msg2 := baseDepositMsg(v2, "aaaa000000000000000000000000000000000000", 0)
	_, err = ms.SubmitSteemDeposit(f.ctx, msg2)
	require.NoError(t, err)

	genesis, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.Len(t, genesis.DepositList, 1, "both confirmations must resolve to a single deposit record")
	require.Len(t, genesis.DepositList[0].ValidatorConfirmations, 2)
}

func TestSubmitSteemDeposit_DuplicateConfirmationRejected(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridge(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	v1 := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)

	msg := baseDepositMsg(v1, "bbbb000000000000000000000000000000000000", 0)
	_, err := ms.SubmitSteemDeposit(f.ctx, msg)
	require.NoError(t, err)

	_, err = ms.SubmitSteemDeposit(f.ctx, msg)
	require.ErrorIs(t, err, types.ErrDuplicateConfirmation)
}

func TestSubmitSteemDeposit_MismatchLeavesDepositUntouched(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridge(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	v1 := newTestValidator(t, 1)
	v2 := newTestValidator(t, 2)
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)
	f.stakingKeeper.setValidator(v2.ValAddr, 100, true)

	txid := "cccc000000000000000000000000000000000000"
	msg1 := baseDepositMsg(v1, txid, 0)
	_, err := ms.SubmitSteemDeposit(f.ctx, msg1)
	require.NoError(t, err)

	msg2 := baseDepositMsg(v2, txid, 0)
	msg2.AmountMillisteem = 9999 // conflicting raw fact

	resp, err := ms.SubmitSteemDeposit(f.ctx, msg2)
	require.NoError(t, err, "a mismatch is a benign no-op, not a tx error")
	require.NotNil(t, resp)

	genesis, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.Len(t, genesis.DepositList, 1)
	require.Equal(t, uint64(1000), genesis.DepositList[0].AmountMillisteem, "original pending deposit must survive unchanged")
	require.Len(t, genesis.DepositList[0].ValidatorConfirmations, 1, "the mismatching validator's confirmation must not be recorded")
}

func TestSubmitSteemDeposit_ThresholdMintsToDerivedDestination(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridge(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	v1 := newTestValidator(t, 1)
	v2 := newTestValidator(t, 2)
	v3 := newTestValidator(t, 3)
	// total bonded = 300. v1 alone (100) is well under 2/3; v1+v2 (250) is
	// well over, avoiding the exact-2/3 boundary (the module's default
	// threshold 0.666666666666666667 is deliberately calibrated to require
	// STRICTLY MORE than exact 2/3, matching BFT's classic safety margin, so
	// an exact 200/300 tie is not a reliable "should mint" fixture).
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)
	f.stakingKeeper.setValidator(v2.ValAddr, 150, true)
	f.stakingKeeper.setValidator(v3.ValAddr, 50, true)

	dest := sdk.AccAddress([]byte("destination_20_bytes")[:20])
	txid := "dddd000000000000000000000000000000000000"

	msg1 := baseDepositMsg(v1, txid, 0)
	msg1.Memo = dest.String()
	_, err := ms.SubmitSteemDeposit(f.ctx, msg1)
	require.NoError(t, err)

	genesis, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.False(t, genesis.DepositList[0].Minted, "100/300 (33%) must not reach the 2/3 threshold")

	msg2 := baseDepositMsg(v2, txid, 0)
	msg2.Memo = dest.String()
	_, err = ms.SubmitSteemDeposit(f.ctx, msg2)
	require.NoError(t, err)

	genesis, err = f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	deposit := genesis.DepositList[0]
	require.True(t, deposit.Minted, "250/300 (83%) must reach the 2/3 threshold")
	require.Equal(t, types.DepositStatus_DEPOSIT_STATUS_MINTED, deposit.Status)
	require.Equal(t, dest.String(), deposit.DerivedDestination)
	require.Equal(t, types.DestinationType_DESTINATION_TYPE_COSMOS, deposit.DestinationType)

	wantMinted := types.MillisteemToAsteem(1000) // baseDepositMsg's AmountMillisteem
	require.True(t, wantMinted.Equal(genesis.TotalMintedAsteem))
	require.Equal(t, sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, wantMinted)), f.bankKeeper.balances[dest.String()])
}

// lastConfirmedRatio returns the confirmed_ratio attribute of the most
// recently emitted DepositConfirmed event.
func lastConfirmedRatio(t *testing.T, ctx sdk.Context) string {
	t.Helper()
	events := ctx.EventManager().Events()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != types.EventTypeDepositConfirmed {
			continue
		}
		for _, attr := range events[i].Attributes {
			if attr.Key == types.AttributeKeyConfirmedRatio {
				return attr.Value
			}
		}
	}
	t.Fatal("no DepositConfirmed event found")
	return ""
}

func TestSubmitSteemDeposit_ThresholdRecomputesLiveNotSnapshotted(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridge(t, f)

	// Set a threshold no combination in this test reaches, so minting never
	// kicks in and the emitted confirmed_ratio can be inspected in isolation
	// at each step.
	params, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	params.BridgeConfirmationThreshold = math.LegacyMustNewDecFromStr("0.999999999999999999")
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))

	ms := keeper.NewMsgServerImpl(f.keeper)
	sdkCtx := sdk.UnwrapSDKContext(f.ctx)

	// vBig never confirms; it only inflates the bonded pool so a plain
	// numerator-only bug (still counting an unbonded confirmer) is
	// distinguishable from correct behavior in the final ratio.
	vBig := newTestValidator(t, 0xB1)
	v1 := newTestValidator(t, 1)
	v2 := newTestValidator(t, 2)
	v3 := newTestValidator(t, 3)
	f.stakingKeeper.setValidator(vBig.ValAddr, 800, true)
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)
	f.stakingKeeper.setValidator(v2.ValAddr, 100, true)
	f.stakingKeeper.setValidator(v3.ValAddr, 100, true)

	txid := "eeee000000000000000000000000000000000000"

	// total bonded = vBig(800)+v1(100)+v2(100)+v3(100) = 1100 throughout,
	// until v2 unbonds below.
	_, err = ms.SubmitSteemDeposit(f.ctx, baseDepositMsg(v1, txid, 0))
	require.NoError(t, err)
	// confirmed = v1(100); total = 1100
	require.Equal(t, math.LegacyNewDec(100).QuoInt64(1100).String(), lastConfirmedRatio(t, sdkCtx))

	_, err = ms.SubmitSteemDeposit(f.ctx, baseDepositMsg(v2, txid, 0))
	require.NoError(t, err)
	// confirmed = v1+v2 = 200; total unchanged = 1100
	require.Equal(t, math.LegacyNewDec(200).QuoInt64(1100).String(), lastConfirmedRatio(t, sdkCtx))

	// v2 unbonds. Both its recorded confirmation (numerator) and its stake
	// (denominator) must stop counting - voting power is recomputed live
	// from current bonded stake on every confirmation, never snapshotted.
	f.stakingKeeper.setValidator(v2.ValAddr, 100, false)

	_, err = ms.SubmitSteemDeposit(f.ctx, baseDepositMsg(v3, txid, 0))
	require.NoError(t, err)
	// confirmed = v1(100, bonded) + v2(EXCLUDED, unbonded) + v3(100, bonded) = 200
	// total = vBig(800) + v1(100) + v3(100) = 1000 (v2 excluded from total too)
	// A bug that still counted v2 would give 300/1000 = 0.3, not 0.2.
	require.Equal(t, math.LegacyNewDec(200).QuoInt64(1000).String(), lastConfirmedRatio(t, sdkCtx))
}

func TestSubmitSteemDeposit_UnparseableMemoBecomesUnclaimable(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridge(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	v1 := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)

	msg := baseDepositMsg(v1, "2222000000000000000000000000000000000000", 0)
	msg.Memo = "not-an-address"
	_, err := ms.SubmitSteemDeposit(f.ctx, msg)
	require.NoError(t, err)

	genesis, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.Equal(t, types.DepositStatus_DEPOSIT_STATUS_UNCLAIMABLE, genesis.DepositList[0].Status)
	require.False(t, genesis.DepositList[0].Minted)
	require.True(t, genesis.TotalMintedAsteem.IsZero())
}

func TestSubmitSteemDeposit_OutOfRangeAmountBecomesUnclaimable(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridge(t, f)

	params, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	params.MaximumBridgeAmount = 500
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))

	ms := keeper.NewMsgServerImpl(f.keeper)
	v1 := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)

	msg := baseDepositMsg(v1, "3333000000000000000000000000000000000000", 0)
	msg.AmountMillisteem = 1000 // exceeds the 500 max
	_, err = ms.SubmitSteemDeposit(f.ctx, msg)
	require.NoError(t, err)

	genesis, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.Equal(t, types.DepositStatus_DEPOSIT_STATUS_UNCLAIMABLE, genesis.DepositList[0].Status)
}

func TestSubmitSteemDeposit_AlreadyMintedRejectsResubmission(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridge(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	v1 := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)

	txid := "4444000000000000000000000000000000000000"
	msg := baseDepositMsg(v1, txid, 0)
	_, err := ms.SubmitSteemDeposit(f.ctx, msg)
	require.NoError(t, err)

	genesis, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.True(t, genesis.DepositList[0].Minted, "sole bonded validator confirming alone must reach 100% >= 2/3")

	v2 := newTestValidator(t, 2)
	f.stakingKeeper.setValidator(v2.ValAddr, 100, true)
	msg2 := baseDepositMsg(v2, txid, 0)
	_, err = ms.SubmitSteemDeposit(f.ctx, msg2)
	require.ErrorIs(t, err, types.ErrDepositAlreadyMinted)
}

func TestSubmitSteemDeposit_GatewayMismatchRejected(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridge(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	v1 := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)

	msg := baseDepositMsg(v1, "5555000000000000000000000000000000000000", 0)
	msg.GatewayAccount = "wrong-gateway"
	_, err := ms.SubmitSteemDeposit(f.ctx, msg)
	require.ErrorIs(t, err, types.ErrInvalidGatewayAccount)
}

func TestSubmitSteemDeposit_BridgeDisabledRejected(t *testing.T) {
	f := initFixtureWithFakes(t)
	// bridge stays disabled (DefaultParams)
	ms := keeper.NewMsgServerImpl(f.keeper)

	v1 := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)

	msg := baseDepositMsg(v1, "6666000000000000000000000000000000000000", 0)
	_, err := ms.SubmitSteemDeposit(f.ctx, msg)
	require.ErrorIs(t, err, types.ErrBridgeDisabled)
}

func TestSubmitSteemDeposit_UnbondedValidatorRejected(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridge(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	v1 := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(v1.ValAddr, 100, false)

	msg := baseDepositMsg(v1, "7777000000000000000000000000000000000000", 0)
	_, err := ms.SubmitSteemDeposit(f.ctx, msg)
	require.ErrorIs(t, err, types.ErrNotBondedValidator)
}
