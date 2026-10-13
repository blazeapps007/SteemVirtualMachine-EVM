package keeper_test

import (
	"strings"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"steemvm/x/oracle/bridge/keeper"
	"steemvm/x/oracle/bridge/types"
)

// TestAttestWithdrawalPayout_MinorityCannotBlockRealPayout is the regression
// test for the per-candidate payout tally. A small validator attests a bogus
// payout txid first; the honest majority then attests the REAL payout. The
// bogus first attestation must NOT pin the facts: the majority candidate
// crosses the threshold and the withdrawal becomes PROCESSED, so the later
// auto-refund is a no-op and the user is not paid twice (on Steem and again
// on-chain).
func TestAttestWithdrawalPayout_MinorityCannotBlockRealPayout(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridgeOut(t, f)
	params, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	params.WithdrawalTimeoutBlocks = 10
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))
	ms := keeper.NewMsgServerImpl(f.keeper)

	attacker := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(attacker.ValAddr, 3, true)
	honestA := newTestValidator(t, 2)
	f.stakingKeeper.setValidator(honestA.ValAddr, 50, true)
	honestB := newTestValidator(t, 3)
	f.stakingKeeper.setValidator(honestB.ValAddr, 47, true)

	user := sdk.AccAddress(make([]byte, 20))
	fundSender(t, f, user, types.MillisteemToAsteem(1000))
	_, err = ms.BridgeOut(f.ctx, &types.MsgBridgeOut{
		Sender: user.String(), DestinationSteemAccount: "alice",
		AmountAsteem: types.MillisteemToAsteem(1000),
	})
	require.NoError(t, err)

	ctx := sdkContextAt(t, f, 2)
	bogus := strings.Repeat("a", 40)
	real := strings.Repeat("b", 40)

	// Attacker (3%) pins a bogus payout txid first.
	_, err = ms.AttestWithdrawalPayout(ctx, &types.MsgAttestWithdrawalPayout{
		Validator: attacker.AccAddr, WithdrawalId: 0, SteemTxid: bogus,
		AmountMillisteem: 1000, SteemBlock: 1, SteemTimestamp: "2026-01-01T00:00:00",
	})
	require.NoError(t, err)

	// The honest majority (97%) attests the REAL payout.
	_, err = ms.AttestWithdrawalPayout(ctx, &types.MsgAttestWithdrawalPayout{
		Validator: honestA.AccAddr, WithdrawalId: 0, SteemTxid: real,
		AmountMillisteem: 1000, SteemBlock: 2, SteemTimestamp: "2026-01-01T00:00:03",
	})
	require.NoError(t, err)
	_, err = ms.AttestWithdrawalPayout(ctx, &types.MsgAttestWithdrawalPayout{
		Validator: honestB.AccAddr, WithdrawalId: 0, SteemTxid: real,
		AmountMillisteem: 1000, SteemBlock: 2, SteemTimestamp: "2026-01-01T00:00:03",
	})
	require.NoError(t, err)

	w, err := f.keeper.Withdrawal.Get(ctx, 0)
	require.NoError(t, err)
	require.Equal(t, types.WithdrawalStatus_WITHDRAWAL_STATUS_PROCESSED, w.Status,
		"the real payout, attested by the majority, finalizes despite the bogus first attestation")
	require.Equal(t, real, w.SteemPayoutTxid, "the canonical payout is the majority candidate, not the bogus one")

	// The timeout refund must now be a no-op: the withdrawal is already PROCESSED.
	require.NoError(t, f.keeper.RefundExpiredWithdrawals(sdkContextAt(t, f, 20)))
	w, err = f.keeper.Withdrawal.Get(ctx, 0)
	require.NoError(t, err)
	require.Equal(t, types.WithdrawalStatus_WITHDRAWAL_STATUS_PROCESSED, w.Status, "no refund after a real payout")
	require.True(t, f.bankKeeper.balances[user.String()].AmountOf(sdk.DefaultBondDenom).IsZero(),
		"the user is NOT refunded on-chain after being paid on Steem")
}

// TestAttestWithdrawalPayout_OneVotePerValidator verifies a validator cannot
// vote for two payout candidates: its first attestation is its only vote. This
// is what prevents two candidates from each reaching the threshold.
func TestAttestWithdrawalPayout_OneVotePerValidator(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridgeOut(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	v := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(v.ValAddr, 100, true)
	other := newTestValidator(t, 2)
	f.stakingKeeper.setValidator(other.ValAddr, 100, true)

	user := sdk.AccAddress(make([]byte, 20))
	fundSender(t, f, user, types.MillisteemToAsteem(1000))
	_, err := ms.BridgeOut(f.ctx, &types.MsgBridgeOut{
		Sender: user.String(), DestinationSteemAccount: "alice",
		AmountAsteem: types.MillisteemToAsteem(1000),
	})
	require.NoError(t, err)

	ctx := sdkContextAt(t, f, 2)
	_, err = ms.AttestWithdrawalPayout(ctx, &types.MsgAttestWithdrawalPayout{
		Validator: v.AccAddr, WithdrawalId: 0, SteemTxid: strings.Repeat("a", 40),
		AmountMillisteem: 1000, SteemBlock: 1, SteemTimestamp: "2026-01-01T00:00:00",
	})
	require.NoError(t, err)
	// Same validator tries a second, different candidate: benign no-op, no second vote.
	_, err = ms.AttestWithdrawalPayout(ctx, &types.MsgAttestWithdrawalPayout{
		Validator: v.AccAddr, WithdrawalId: 0, SteemTxid: strings.Repeat("c", 40),
		AmountMillisteem: 1000, SteemBlock: 1, SteemTimestamp: "2026-01-01T00:00:00",
	})
	require.NoError(t, err)

	w, err := f.keeper.Withdrawal.Get(ctx, 0)
	require.NoError(t, err)
	require.Len(t, w.ValidatorConfirmations, 1, "a validator records at most one payout attestation per withdrawal")
	require.Equal(t, types.WithdrawalStatus_WITHDRAWAL_STATUS_REQUESTED, w.Status, "50% is under threshold")
}
