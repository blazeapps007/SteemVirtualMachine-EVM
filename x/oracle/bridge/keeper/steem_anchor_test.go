package keeper_test

import (
	"fmt"
	"strings"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"steemvm/x/oracle/bridge/keeper"
	"steemvm/x/oracle/bridge/types"
)

const anchorBlock = 110435764

// setSteemAnchor records the Steem anchor the way genesis does: the block
// number plus its Steem block ID (which starts with the block number in hex).
func setSteemAnchor(t *testing.T, f *fixtureWithFakes, block uint64) {
	t.Helper()
	params, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	params.RelayerStartBlock = block
	params.RelayerStartBlockId = fmt.Sprintf("%08x", block) + strings.Repeat("ab", 16)
	require.NoError(t, params.Validate())
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))
}

// Steem history from before this chain's anchor (e.g. a previous chain's
// deposits to the same gateway) must never be attested, through the msg server
// or the ante fee-exemption pre-check.
func TestSteemAnchor_RejectsDepositsBeforeTheAnchor(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridge(t, f)
	setSteemAnchor(t, f, anchorBlock)
	ms := keeper.NewMsgServerImpl(f.keeper)
	v := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(v.ValAddr, 10, true)

	old := baseDepositMsg(v, strings.Repeat("1", 40), 0)
	old.SteemBlock = anchorBlock - 1
	_, err := ms.AttestDeposit(f.ctx, old)
	require.ErrorIs(t, err, types.ErrSteemBlockBeforeAnchor)
	require.ErrorIs(t, f.keeper.ValidateDepositAcceptance(f.ctx, old), types.ErrSteemBlockBeforeAnchor)
	_, err = f.keeper.DepositByTxid.Get(f.ctx, collections.Join(old.Txid, old.OpIndex))
	require.Error(t, err, "a rejected attestation creates no deposit record")

	atAnchor := baseDepositMsg(v, strings.Repeat("2", 40), 0)
	atAnchor.SteemBlock = anchorBlock
	require.NoError(t, f.keeper.ValidateDepositAcceptance(f.ctx, atAnchor))
	_, err = ms.AttestDeposit(f.ctx, atAnchor)
	require.NoError(t, err, "the anchor block itself is this chain's first Steem block")
}

func TestSteemAnchor_RejectsNameRegistrationsBeforeTheAnchor(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableNameService(t, f)
	setSteemAnchor(t, f, anchorBlock)
	ms := keeper.NewMsgServerImpl(f.keeper)
	v := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(v.ValAddr, 10, true)

	old := baseNameRegistrationMsg(v, strings.Repeat("3", 40), 0)
	old.SteemBlock = anchorBlock - 1
	_, err := ms.SubmitNameRegistration(f.ctx, old)
	require.ErrorIs(t, err, types.ErrSteemBlockBeforeAnchor)
	require.ErrorIs(t, f.keeper.ValidateNameRegistrationAcceptance(f.ctx, old), types.ErrSteemBlockBeforeAnchor)

	fresh := baseNameRegistrationMsg(v, strings.Repeat("4", 40), 0)
	fresh.SteemBlock = anchorBlock + 5
	_, err = ms.SubmitNameRegistration(f.ctx, fresh)
	require.NoError(t, err)
}

// An old Steem payout's "svm-withdrawal <id>" memo refers to a previous
// chain's withdrawal IDs, which restart at 0 here: it must never mark one of
// this chain's withdrawals PROCESSED.
func TestSteemAnchor_RejectsPayoutsBeforeTheAnchor(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridgeOut(t, f)
	setSteemAnchor(t, f, anchorBlock)
	ms := keeper.NewMsgServerImpl(f.keeper)
	v := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(v.ValAddr, 10, true)

	user := sdk.AccAddress(make([]byte, 20))
	fundSender(t, f, user, types.MillisteemToAsteem(1000))
	_, err := ms.BridgeOut(f.ctx, &types.MsgBridgeOut{
		Sender: user.String(), DestinationSteemAccount: "alice", AmountAsteem: types.MillisteemToAsteem(1000),
	})
	require.NoError(t, err)

	old := &types.MsgAttestWithdrawalPayout{
		Validator: v.AccAddr, WithdrawalId: 0, SteemTxid: strings.Repeat("5", 40),
		AmountMillisteem: 1000, SteemBlock: anchorBlock - 1, SteemTimestamp: "2026-10-01T00:00:00",
	}
	_, err = ms.AttestWithdrawalPayout(f.ctx, old)
	require.ErrorIs(t, err, types.ErrSteemBlockBeforeAnchor)
	require.ErrorIs(t, f.keeper.ValidateWithdrawalProcessedAcceptance(f.ctx, old), types.ErrSteemBlockBeforeAnchor)
	w, err := f.keeper.Withdrawal.Get(f.ctx, 0)
	require.NoError(t, err)
	require.Equal(t, types.WithdrawalStatus_WITHDRAWAL_STATUS_REQUESTED, w.Status)

	real := *old
	real.SteemBlock = anchorBlock + 100
	_, err = ms.AttestWithdrawalPayout(f.ctx, &real)
	require.NoError(t, err)
	w, err = f.keeper.Withdrawal.Get(f.ctx, 0)
	require.NoError(t, err)
	require.Equal(t, types.WithdrawalStatus_WITHDRAWAL_STATUS_PROCESSED, w.Status, "the sole validator's real payout finalizes")
}

// With no anchor (relayer_start_block 0) nothing is rejected: the behavior of
// a chain launched before the anchor existed.
func TestSteemAnchor_NoAnchorRejectsNothing(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridge(t, f)
	v := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(v.ValAddr, 10, true)
	msg := baseDepositMsg(v, strings.Repeat("6", 40), 0)
	msg.SteemBlock = 1
	_, err := keeper.NewMsgServerImpl(f.keeper).AttestDeposit(f.ctx, msg)
	require.NoError(t, err)
}
