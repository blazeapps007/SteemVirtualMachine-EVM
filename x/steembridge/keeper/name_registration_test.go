package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"steemvm/x/steembridge/keeper"
	"steemvm/x/steembridge/types"
)

func enableNameService(t *testing.T, f *fixtureWithFakes) {
	t.Helper()
	params := types.DefaultParams()
	params.NameServiceEnabled = true
	params.GatewayAccount = "gateway-account"
	params.BridgeConfirmationThreshold = math.LegacyMustNewDecFromStr("0.666666666666666667")
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))
}

func baseNameRegistrationMsg(validator testValidator, txid string, opIndex uint32) *types.MsgSubmitNameRegistration {
	return &types.MsgSubmitNameRegistration{
		Validator:        validator.AccAddr,
		Txid:             txid,
		OpIndex:          opIndex,
		SteemBlock:       100,
		SteemTimestamp:   "2024-01-01T00:00:00",
		SteemAccount:     "alice",
		GatewayAccount:   "gateway-account",
		AmountMillisteem: 1, // 0.001 STEEM, the default minimum
		Memo:             sdk.AccAddress(make([]byte, 20)).String(),
	}
}

// registerToAwaiting drives a registration through attestation to
// AWAITING_CONFIRMATION using two validators that together clear the
// threshold, and returns the registration.
func registerToAwaiting(t *testing.T, f *fixtureWithFakes, ms types.MsgServer, txid, account, memo string) types.NameRegistration {
	t.Helper()

	v1 := newTestValidator(t, 101)
	v2 := newTestValidator(t, 102)
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)
	f.stakingKeeper.setValidator(v2.ValAddr, 150, true)

	msg1 := baseNameRegistrationMsg(v1, txid, 0)
	msg1.SteemAccount = account
	msg1.Memo = memo
	_, err := ms.SubmitNameRegistration(f.ctx, msg1)
	require.NoError(t, err)

	msg2 := baseNameRegistrationMsg(v2, txid, 0)
	msg2.SteemAccount = account
	msg2.Memo = memo
	_, err = ms.SubmitNameRegistration(f.ctx, msg2)
	require.NoError(t, err)

	id, err := f.keeper.NameRegistrationByTxid.Get(f.ctx, collections.Join(txid, uint32(0)))
	require.NoError(t, err)
	registration, err := f.keeper.NameRegistration.Get(f.ctx, id)
	require.NoError(t, err)
	require.Equal(t, types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_AWAITING_CONFIRMATION, registration.Status)
	return registration
}

func TestSubmitNameRegistration_ThresholdParksAwaitingAndCreditsFee(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableNameService(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	v1 := newTestValidator(t, 1)
	v2 := newTestValidator(t, 2)
	v3 := newTestValidator(t, 3)
	// total bonded = 300; v1 alone (100) is under 2/3, v1+v2 (250) is over.
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)
	f.stakingKeeper.setValidator(v2.ValAddr, 150, true)
	f.stakingKeeper.setValidator(v3.ValAddr, 50, true)

	dest := sdk.AccAddress([]byte("destination_20_bytes")[:20])
	txid := "aaaa000000000000000000000000000000000001"

	msg1 := baseNameRegistrationMsg(v1, txid, 0)
	msg1.Memo = dest.String()
	_, err := ms.SubmitNameRegistration(f.ctx, msg1)
	require.NoError(t, err)

	id, err := f.keeper.NameRegistrationByTxid.Get(f.ctx, collections.Join(txid, uint32(0)))
	require.NoError(t, err)
	registration, err := f.keeper.NameRegistration.Get(f.ctx, id)
	require.NoError(t, err)
	require.Equal(t, types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_PENDING, registration.Status, "below threshold must stay PENDING")

	msg2 := baseNameRegistrationMsg(v2, txid, 0)
	msg2.Memo = dest.String()
	_, err = ms.SubmitNameRegistration(f.ctx, msg2)
	require.NoError(t, err)

	registration, err = f.keeper.NameRegistration.Get(f.ctx, id)
	require.NoError(t, err)
	require.Equal(t, types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_AWAITING_CONFIRMATION, registration.Status)
	require.Equal(t, dest.String(), registration.DerivedDestination)
	require.Equal(t, types.DestinationType_DESTINATION_TYPE_COSMOS, registration.DestinationType)

	// Reaching the threshold parks the registration AND credits the
	// registration fee to the destination (backed by the real STEEM held at the
	// gateway), so the destination can pay gas for an EVM confirmName call. The
	// credit goes through the bridge's shared mint path, so the fee lands on the
	// destination and the module account nets to zero.
	wantCredit := types.MillisteemToAsteem(msg1.AmountMillisteem)
	require.Equal(t,
		sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, wantCredit)),
		f.bankKeeper.balances[dest.String()],
		"registration fee must be credited to the destination")
	require.True(t, f.bankKeeper.balances[types.ModuleName].IsZero(),
		"module account must net to zero after crediting the destination")

	has, err := f.keeper.NameRegistrationAwaitingByDest.Has(f.ctx, collections.Join([]byte(dest), id))
	require.NoError(t, err)
	require.True(t, has, "awaiting-by-destination index must be populated at threshold")
}

func TestSubmitNameRegistration_EVMMemoDerivesSameAccountBytes(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableNameService(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	registration := registerToAwaiting(t, f, ms,
		"aaaa000000000000000000000000000000000002", "alice",
		"0x1111111111111111111111111111111111111111")

	expected := sdk.AccAddress([]byte{
		0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11,
		0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11,
	})
	require.Equal(t, expected.String(), registration.DerivedDestination)
	require.Equal(t, types.DestinationType_DESTINATION_TYPE_EVM, registration.DestinationType)
}

func TestSubmitNameRegistration_UnparseableMemoUnclaimable(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableNameService(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	v1 := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)

	txid := "aaaa000000000000000000000000000000000003"
	msg := baseNameRegistrationMsg(v1, txid, 0)
	msg.Memo = "not an address at all"
	_, err := ms.SubmitNameRegistration(f.ctx, msg)
	require.NoError(t, err)

	id, err := f.keeper.NameRegistrationByTxid.Get(f.ctx, collections.Join(txid, uint32(0)))
	require.NoError(t, err)
	registration, err := f.keeper.NameRegistration.Get(f.ctx, id)
	require.NoError(t, err)
	require.Equal(t, types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_UNCLAIMABLE, registration.Status)
	require.Empty(t, registration.DerivedDestination)
}

func TestSubmitNameRegistration_Rejections(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableNameService(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	bonded := newTestValidator(t, 1)
	unbonded := newTestValidator(t, 2)
	unknown := newTestValidator(t, 3)
	f.stakingKeeper.setValidator(bonded.ValAddr, 100, true)
	f.stakingKeeper.setValidator(unbonded.ValAddr, 100, false)

	txid := "aaaa000000000000000000000000000000000004"

	_, err := ms.SubmitNameRegistration(f.ctx, baseNameRegistrationMsg(unbonded, txid, 0))
	require.ErrorIs(t, err, types.ErrNotBondedValidator)

	_, err = ms.SubmitNameRegistration(f.ctx, baseNameRegistrationMsg(unknown, txid, 0))
	require.ErrorIs(t, err, types.ErrNotBondedValidator)

	wrongGateway := baseNameRegistrationMsg(bonded, txid, 0)
	wrongGateway.GatewayAccount = "other-gateway"
	_, err = ms.SubmitNameRegistration(f.ctx, wrongGateway)
	require.ErrorIs(t, err, types.ErrInvalidGatewayAccount)

	params, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	params.NameRegistrationMinMillisteem = 10
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))
	belowMin := baseNameRegistrationMsg(bonded, txid, 0)
	belowMin.AmountMillisteem = 9
	_, err = ms.SubmitNameRegistration(f.ctx, belowMin)
	require.ErrorIs(t, err, types.ErrRegistrationBelowMinimum)
	params.NameRegistrationMinMillisteem = 1
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))

	// duplicate attestation by the same validator
	ok := baseNameRegistrationMsg(bonded, txid, 0)
	_, err = ms.SubmitNameRegistration(f.ctx, ok)
	require.NoError(t, err)
	_, err = ms.SubmitNameRegistration(f.ctx, ok)
	require.ErrorIs(t, err, types.ErrDuplicateConfirmation)

	// disabled name service
	params.NameServiceEnabled = false
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))
	_, err = ms.SubmitNameRegistration(f.ctx, baseNameRegistrationMsg(bonded, "aaaa000000000000000000000000000000000005", 0))
	require.ErrorIs(t, err, types.ErrNameServiceDisabled)
}

func TestSubmitNameRegistration_MismatchLeavesPendingUntouched(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableNameService(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	v1 := newTestValidator(t, 1)
	v2 := newTestValidator(t, 2)
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)
	f.stakingKeeper.setValidator(v2.ValAddr, 100, true)

	txid := "aaaa000000000000000000000000000000000006"
	_, err := ms.SubmitNameRegistration(f.ctx, baseNameRegistrationMsg(v1, txid, 0))
	require.NoError(t, err)

	mismatch := baseNameRegistrationMsg(v2, txid, 0)
	mismatch.Memo = "0x2222222222222222222222222222222222222222"
	resp, err := ms.SubmitNameRegistration(f.ctx, mismatch)
	require.NoError(t, err, "a mismatch is a benign no-op, not a tx error")
	require.NotNil(t, resp)

	id, err := f.keeper.NameRegistrationByTxid.Get(f.ctx, collections.Join(txid, uint32(0)))
	require.NoError(t, err)
	registration, err := f.keeper.NameRegistration.Get(f.ctx, id)
	require.NoError(t, err)
	require.Equal(t, types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_PENDING, registration.Status)
	require.Len(t, registration.ValidatorConfirmations, 1, "the mismatching validator's attestation must not be recorded")
}

func TestSubmitNameRegistration_UnbondedConfirmerNotCounted(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableNameService(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	v1 := newTestValidator(t, 1)
	v2 := newTestValidator(t, 2)
	v3 := newTestValidator(t, 3)
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)
	f.stakingKeeper.setValidator(v2.ValAddr, 100, true)
	f.stakingKeeper.setValidator(v3.ValAddr, 100, true)

	txid := "aaaa000000000000000000000000000000000007"
	_, err := ms.SubmitNameRegistration(f.ctx, baseNameRegistrationMsg(v1, txid, 0))
	require.NoError(t, err)

	// v1 unbonds after attesting; its power must not count toward threshold.
	f.stakingKeeper.setValidator(v1.ValAddr, 100, false)

	_, err = ms.SubmitNameRegistration(f.ctx, baseNameRegistrationMsg(v2, txid, 0))
	require.NoError(t, err)

	id, err := f.keeper.NameRegistrationByTxid.Get(f.ctx, collections.Join(txid, uint32(0)))
	require.NoError(t, err)
	registration, err := f.keeper.NameRegistration.Get(f.ctx, id)
	require.NoError(t, err)
	// bonded total = 200 (v2+v3); only v2's 100 counts => 0.5 < 2/3.
	require.Equal(t, types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_PENDING, registration.Status)
}

func TestConfirmName_ActivatesLink(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableNameService(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	dest := sdk.AccAddress([]byte("confirm_dest_20bytes")[:20])
	registration := registerToAwaiting(t, f, ms,
		"bbbb000000000000000000000000000000000001", "alice", dest.String())

	_, err := ms.ConfirmName(f.ctx, &types.MsgConfirmName{
		Confirmer:      dest.String(),
		RegistrationId: registration.Id,
	})
	require.NoError(t, err)

	record, err := f.keeper.ActiveName.Get(f.ctx, "alice")
	require.NoError(t, err)
	require.Equal(t, dest.String(), record.Address)
	require.Equal(t, registration.Id, record.RegistrationId)

	updated, err := f.keeper.NameRegistration.Get(f.ctx, registration.Id)
	require.NoError(t, err)
	require.Equal(t, types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_ACTIVE, updated.Status)
	require.NotEmpty(t, updated.ConfirmTxHash)

	has, err := f.keeper.NameRegistrationAwaitingByDest.Has(f.ctx, collections.Join([]byte(dest), registration.Id))
	require.NoError(t, err)
	require.False(t, has, "awaiting-by-destination entry must be cleared on confirmation")

	hasReverse, err := f.keeper.ActiveNameByAddress.Has(f.ctx, collections.Join([]byte(dest), "alice"))
	require.NoError(t, err)
	require.True(t, hasReverse)
}

func TestConfirmName_Rejections(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableNameService(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	dest := sdk.AccAddress([]byte("confirm_dest_20bytes")[:20])
	stranger := sdk.AccAddress([]byte("stranger_20_bytes___")[:20])
	registration := registerToAwaiting(t, f, ms,
		"bbbb000000000000000000000000000000000002", "alice", dest.String())

	// wrong signer
	_, err := ms.ConfirmName(f.ctx, &types.MsgConfirmName{Confirmer: stranger.String(), RegistrationId: registration.Id})
	require.ErrorIs(t, err, types.ErrConfirmerNotDestination)

	// unknown id
	_, err = ms.ConfirmName(f.ctx, &types.MsgConfirmName{Confirmer: dest.String(), RegistrationId: 999})
	require.ErrorIs(t, err, types.ErrRegistrationNotFound)

	// still PENDING (single low-power attestation)
	weak := newTestValidator(t, 50)
	f.stakingKeeper.setValidator(weak.ValAddr, 1, true)
	pendingTxid := "bbbb000000000000000000000000000000000003"
	pendingMsg := baseNameRegistrationMsg(weak, pendingTxid, 0)
	pendingMsg.SteemAccount = "bob"
	_, err = ms.SubmitNameRegistration(f.ctx, pendingMsg)
	require.NoError(t, err)
	pendingID, err := f.keeper.NameRegistrationByTxid.Get(f.ctx, collections.Join(pendingTxid, uint32(0)))
	require.NoError(t, err)
	_, err = ms.ConfirmName(f.ctx, &types.MsgConfirmName{Confirmer: dest.String(), RegistrationId: pendingID})
	require.ErrorIs(t, err, types.ErrRegistrationNotAwaiting)

	// already confirmed
	_, err = ms.ConfirmName(f.ctx, &types.MsgConfirmName{Confirmer: dest.String(), RegistrationId: registration.Id})
	require.NoError(t, err)
	_, err = ms.ConfirmName(f.ctx, &types.MsgConfirmName{Confirmer: dest.String(), RegistrationId: registration.Id})
	require.ErrorIs(t, err, types.ErrRegistrationNotAwaiting)

	// name service disabled
	params, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	params.NameServiceEnabled = false
	require.NoError(t, f.keeper.Params.Set(f.ctx, params))
	_, err = ms.ConfirmName(f.ctx, &types.MsgConfirmName{Confirmer: dest.String(), RegistrationId: registration.Id})
	require.ErrorIs(t, err, types.ErrNameServiceDisabled)
}

func TestConfirmName_RelinkSupersedesAcrossAddresses(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableNameService(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	addr1 := sdk.AccAddress([]byte("first_address_20byte")[:20])
	addr2 := sdk.AccAddress([]byte("second_address_20byt")[:20])

	regA := registerToAwaiting(t, f, ms,
		"cccc000000000000000000000000000000000001", "alice", addr1.String())
	_, err := ms.ConfirmName(f.ctx, &types.MsgConfirmName{Confirmer: addr1.String(), RegistrationId: regA.Id})
	require.NoError(t, err)

	regB := registerToAwaiting(t, f, ms,
		"cccc000000000000000000000000000000000002", "alice", addr2.String())
	_, err = ms.ConfirmName(f.ctx, &types.MsgConfirmName{Confirmer: addr2.String(), RegistrationId: regB.Id})
	require.NoError(t, err)

	record, err := f.keeper.ActiveName.Get(f.ctx, "alice")
	require.NoError(t, err)
	require.Equal(t, addr2.String(), record.Address, "newest confirmed link must win")
	require.Equal(t, regB.Id, record.RegistrationId)

	oldReg, err := f.keeper.NameRegistration.Get(f.ctx, regA.Id)
	require.NoError(t, err)
	require.Equal(t, types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_SUPERSEDED, oldReg.Status)

	hasOld, err := f.keeper.ActiveNameByAddress.Has(f.ctx, collections.Join([]byte(addr1), "alice"))
	require.NoError(t, err)
	require.False(t, hasOld, "old reverse-index entry must be removed")
	hasNew, err := f.keeper.ActiveNameByAddress.Has(f.ctx, collections.Join([]byte(addr2), "alice"))
	require.NoError(t, err)
	require.True(t, hasNew)
}

func TestConfirmName_SameAddressHoldsMultipleNames(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableNameService(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	dest := sdk.AccAddress([]byte("shared_dest_20_bytes")[:20])

	regA := registerToAwaiting(t, f, ms,
		"dddd000000000000000000000000000000000001", "alice", dest.String())
	regB := registerToAwaiting(t, f, ms,
		"dddd000000000000000000000000000000000002", "bob", dest.String())

	_, err := ms.ConfirmName(f.ctx, &types.MsgConfirmName{Confirmer: dest.String(), RegistrationId: regA.Id})
	require.NoError(t, err)
	_, err = ms.ConfirmName(f.ctx, &types.MsgConfirmName{Confirmer: dest.String(), RegistrationId: regB.Id})
	require.NoError(t, err)

	for _, name := range []string{"alice", "bob"} {
		record, err := f.keeper.ActiveName.Get(f.ctx, name)
		require.NoError(t, err)
		require.Equal(t, dest.String(), record.Address)
	}
}

func TestExpireNameRegistrations_PendingExpiresAndKeyIsReusable(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableNameService(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	v1 := newTestValidator(t, 1)
	v2 := newTestValidator(t, 2)
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)
	f.stakingKeeper.setValidator(v2.ValAddr, 300, true)

	txid := "eeee000000000000000000000000000000000001"
	_, err := ms.SubmitNameRegistration(f.ctx, baseNameRegistrationMsg(v1, txid, 0))
	require.NoError(t, err)

	params, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)

	// One block before the deadline: nothing expires.
	almostCtx := sdkContextAt(t, f, int64(params.NamePendingTimeoutBlocks))
	require.NoError(t, f.keeper.ExpireNameRegistrations(almostCtx))
	id, err := f.keeper.NameRegistrationByTxid.Get(f.ctx, collections.Join(txid, uint32(0)))
	require.NoError(t, err)

	// Past the deadline: the registration expires and releases its key.
	expiredCtx := sdkContextAt(t, f, int64(params.NamePendingTimeoutBlocks)+1)
	require.NoError(t, f.keeper.ExpireNameRegistrations(expiredCtx))

	registration, err := f.keeper.NameRegistration.Get(f.ctx, id)
	require.NoError(t, err)
	require.Equal(t, types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_EXPIRED, registration.Status, "record must be kept for audit")

	_, err = f.keeper.NameRegistrationByTxid.Get(f.ctx, collections.Join(txid, uint32(0)))
	require.Error(t, err, "dedup entry must be released")

	// The same validator can attest the same key fresh.
	_, err = ms.SubmitNameRegistration(expiredCtx, baseNameRegistrationMsg(v1, txid, 0))
	require.NoError(t, err)
	newID, err := f.keeper.NameRegistrationByTxid.Get(f.ctx, collections.Join(txid, uint32(0)))
	require.NoError(t, err)
	require.NotEqual(t, id, newID, "resubmission must create a fresh registration")
}

func TestExpireNameRegistrations_AwaitingWindowRestartsAtThreshold(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableNameService(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	params, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	timeout := int64(params.NamePendingTimeoutBlocks)

	v1 := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)

	dest := sdk.AccAddress([]byte("late_confirm_20bytes")[:20])

	// Reach threshold late in the pending window (single validator = 100%).
	txid := "eeee000000000000000000000000000000000002"
	lateCtx := sdkContextAt(t, f, timeout-1)
	msg := baseNameRegistrationMsg(v1, txid, 0)
	msg.Memo = dest.String()
	_, err = ms.SubmitNameRegistration(lateCtx, msg)
	require.NoError(t, err)

	id, err := f.keeper.NameRegistrationByTxid.Get(f.ctx, collections.Join(txid, uint32(0)))
	require.NoError(t, err)
	registration, err := f.keeper.NameRegistration.Get(f.ctx, id)
	require.NoError(t, err)
	require.Equal(t, types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_AWAITING_CONFIRMATION, registration.Status)
	require.Equal(t, uint64(timeout-1), registration.AwaitingSince)

	// Well past the ORIGINAL pending deadline, but within the restarted
	// confirmation window: must NOT expire.
	midCtx := sdkContextAt(t, f, timeout+timeout/2)
	require.NoError(t, f.keeper.ExpireNameRegistrations(midCtx))
	registration, err = f.keeper.NameRegistration.Get(f.ctx, id)
	require.NoError(t, err)
	require.Equal(t, types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_AWAITING_CONFIRMATION, registration.Status,
		"confirmation window must be measured from awaiting_since, not created_at")

	// Past awaiting_since + timeout: expires, and the awaiting index is cleared.
	pastCtx := sdkContextAt(t, f, (timeout-1)+timeout+1)
	require.NoError(t, f.keeper.ExpireNameRegistrations(pastCtx))
	registration, err = f.keeper.NameRegistration.Get(f.ctx, id)
	require.NoError(t, err)
	require.Equal(t, types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_EXPIRED, registration.Status)

	has, err := f.keeper.NameRegistrationAwaitingByDest.Has(f.ctx, collections.Join([]byte(dest), id))
	require.NoError(t, err)
	require.False(t, has)

	// An expired registration can no longer be confirmed.
	_, err = ms.ConfirmName(f.ctx, &types.MsgConfirmName{Confirmer: dest.String(), RegistrationId: id})
	require.ErrorIs(t, err, types.ErrRegistrationNotAwaiting)
}

func TestConsumeFreeNameConfirmQuota_CapAndLazyReset(t *testing.T) {
	f := initFixtureWithFakes(t)
	addr := sdk.AccAddress([]byte("quota_address_20byte")[:20])

	ctx := sdkContextAt(t, f, 1)
	for i := 0; i < keeper.MaxFreeNameConfirmationsPerAddressPerBlock; i++ {
		ok, err := f.keeper.ConsumeFreeNameConfirmQuota(ctx, addr)
		require.NoError(t, err)
		require.True(t, ok, "confirmation %d must be under the cap", i)
	}
	ok, err := f.keeper.ConsumeFreeNameConfirmQuota(ctx, addr)
	require.NoError(t, err)
	require.False(t, ok, "cap must be enforced")

	// New block: the counter lazily resets.
	nextCtx := sdkContextAt(t, f, 2)
	ok, err = f.keeper.ConsumeFreeNameConfirmQuota(nextCtx, addr)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestNameGenesis_RoundTripAndExpiredDoesNotResurrectDedup(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableNameService(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	// Build state covering PENDING, AWAITING, ACTIVE and EXPIRED. The EXPIRED
	// record is produced first (via a sweep at a far-future height), because
	// the sweep would otherwise also expire the PENDING/AWAITING records this
	// test needs to survive.
	dest := sdk.AccAddress([]byte("genesis_dest_20bytes")[:20])

	weak := newTestValidator(t, 60)
	f.stakingKeeper.setValidator(weak.ValAddr, 1, true)

	expiredMsg := baseNameRegistrationMsg(weak, "ffff000000000000000000000000000000000004", 1)
	expiredMsg.SteemAccount = "dave"
	_, err := ms.SubmitNameRegistration(f.ctx, expiredMsg)
	require.NoError(t, err)
	params, err := f.keeper.Params.Get(f.ctx)
	require.NoError(t, err)
	require.NoError(t, f.keeper.ExpireNameRegistrations(sdkContextAt(t, f, int64(params.NamePendingTimeoutBlocks)+1)))

	active := registerToAwaiting(t, f, ms,
		"ffff000000000000000000000000000000000001", "alice", dest.String())
	_, err = ms.ConfirmName(f.ctx, &types.MsgConfirmName{Confirmer: dest.String(), RegistrationId: active.Id})
	require.NoError(t, err)

	awaiting := registerToAwaiting(t, f, ms,
		"ffff000000000000000000000000000000000002", "bob", dest.String())

	pendingMsg := baseNameRegistrationMsg(weak, "ffff000000000000000000000000000000000003", 0)
	pendingMsg.SteemAccount = "carol"
	_, err = ms.SubmitNameRegistration(f.ctx, pendingMsg)
	require.NoError(t, err)

	exported, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	require.NoError(t, exported.Validate())
	require.Len(t, exported.ActiveNameList, 1)
	require.Len(t, exported.NameRegistrationList, 4)

	// Import into a fresh keeper.
	f2 := initFixtureWithFakes(t)
	require.NoError(t, f2.keeper.InitGenesis(f2.ctx, *exported))

	reExported, err := f2.keeper.ExportGenesis(f2.ctx)
	require.NoError(t, err)
	require.Equal(t, exported.NameRegistrationList, reExported.NameRegistrationList)
	require.Equal(t, exported.ActiveNameList, reExported.ActiveNameList)
	require.Equal(t, exported.NameRegistrationCount, reExported.NameRegistrationCount)

	// Active/awaiting keys are still occupied after import...
	_, err = f2.keeper.NameRegistrationByTxid.Get(f2.ctx, collections.Join(active.Txid, active.OpIndex))
	require.NoError(t, err)
	_, err = f2.keeper.NameRegistrationByTxid.Get(f2.ctx, collections.Join(awaiting.Txid, awaiting.OpIndex))
	require.NoError(t, err)
	// ...but the EXPIRED registration's dedup key must stay released.
	_, err = f2.keeper.NameRegistrationByTxid.Get(f2.ctx, collections.Join(expiredMsg.Txid, uint32(1)))
	require.Error(t, err, "expired registration must not resurrect its dedup entry on import")

	// The awaiting-by-destination index must be rebuilt for AWAITING entries.
	has, err := f2.keeper.NameRegistrationAwaitingByDest.Has(f2.ctx, collections.Join([]byte(dest), awaiting.Id))
	require.NoError(t, err)
	require.True(t, has)
}
