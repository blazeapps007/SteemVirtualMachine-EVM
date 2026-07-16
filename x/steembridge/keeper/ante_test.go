package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"steemvm/x/steembridge/keeper"
	"steemvm/x/steembridge/types"
)

// These tests exercise the two building blocks the ante handler's fee
// exemption decorator (app/ante_steembridge.go) composes: whether a
// submission "would be accepted" (ValidateDepositAcceptance) and the
// per-validator per-block free-tx cap (ConsumeFreeDepositQuota). The ante
// decorator itself lives in the app package and wires in the real EVM/IBC
// ante chain, which is out of scope for this module's own test suite; these
// keeper-level tests give full coverage of the actual decision logic it
// delegates to.

func TestValidateDepositAcceptance_Boundaries(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridge(t, f)

	v1 := newTestValidator(t, 1)
	vOther := newTestValidator(t, 9) // never confirms; keeps v1 alone under threshold so the deposit stays pending
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)
	f.stakingKeeper.setValidator(vOther.ValAddr, 100, true)

	msg := baseDepositMsg(v1, "aaaa111111111111111111111111111111111111", 0)
	require.NoError(t, f.keeper.ValidateDepositAcceptance(f.ctx, msg), "a fresh, well-formed submission must be accepted")

	t.Run("bridge disabled", func(t *testing.T) {
		params, err := f.keeper.Params.Get(f.ctx)
		require.NoError(t, err)
		params.BridgeEnabled = false
		require.NoError(t, f.keeper.Params.Set(f.ctx, params))
		require.ErrorIs(t, f.keeper.ValidateDepositAcceptance(f.ctx, msg), types.ErrBridgeDisabled)
		params.BridgeEnabled = true
		require.NoError(t, f.keeper.Params.Set(f.ctx, params))
	})

	t.Run("gateway mismatch", func(t *testing.T) {
		bad := baseDepositMsg(v1, msg.Txid, msg.OpIndex)
		bad.GatewayAccount = "someone-else"
		require.ErrorIs(t, f.keeper.ValidateDepositAcceptance(f.ctx, bad), types.ErrInvalidGatewayAccount)
	})

	ms := keeper.NewMsgServerImpl(f.keeper)
	_, err := ms.SubmitSteemDeposit(f.ctx, msg)
	require.NoError(t, err)

	t.Run("duplicate confirmation from the same validator", func(t *testing.T) {
		require.ErrorIs(t, f.keeper.ValidateDepositAcceptance(f.ctx, msg), types.ErrDuplicateConfirmation)
	})

	t.Run("mismatched resubmission from a different validator", func(t *testing.T) {
		v2 := newTestValidator(t, 2)
		f.stakingKeeper.setValidator(v2.ValAddr, 100, true)
		mismatched := baseDepositMsg(v2, msg.Txid, msg.OpIndex)
		mismatched.AmountMillisteem = msg.AmountMillisteem + 1
		require.ErrorIs(t, f.keeper.ValidateDepositAcceptance(f.ctx, mismatched), types.ErrDepositMismatch)
	})

	t.Run("matching confirmation from a different validator is accepted", func(t *testing.T) {
		v3 := newTestValidator(t, 3)
		f.stakingKeeper.setValidator(v3.ValAddr, 100, true)
		matching := baseDepositMsg(v3, msg.Txid, msg.OpIndex)
		require.NoError(t, f.keeper.ValidateDepositAcceptance(f.ctx, matching))
	})
}

func TestValidateDepositAcceptance_AlreadyMinted(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridge(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	v1 := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(v1.ValAddr, 100, true)

	msg := baseDepositMsg(v1, "bbbb111111111111111111111111111111111111", 0)
	_, err := ms.SubmitSteemDeposit(f.ctx, msg)
	require.NoError(t, err) // sole bonded validator confirming alone reaches 100% >= 2/3, mints

	v2 := newTestValidator(t, 2)
	f.stakingKeeper.setValidator(v2.ValAddr, 100, true)
	resubmit := baseDepositMsg(v2, msg.Txid, msg.OpIndex)
	require.ErrorIs(t, f.keeper.ValidateDepositAcceptance(f.ctx, resubmit), types.ErrDepositAlreadyMinted)
}

func TestConsumeFreeDepositQuota(t *testing.T) {
	f := initFixtureWithFakes(t)
	valAddr := []byte{0x01, 0x02, 0x03}

	sdkCtx := sdkContextAt(t, f, 10)
	for i := 0; i < keeper.MaxFreeDepositsPerValidatorPerBlock; i++ {
		ok, err := f.keeper.ConsumeFreeDepositQuota(sdkCtx, valAddr)
		require.NoError(t, err)
		require.True(t, ok, "iteration %d should still be under the cap", i)
	}

	ok, err := f.keeper.ConsumeFreeDepositQuota(sdkCtx, valAddr)
	require.NoError(t, err)
	require.False(t, ok, "exceeding the cap within the same block must be rejected")

	// A new block resets the counter.
	sdkCtxNextBlock := sdkContextAt(t, f, 11)
	ok, err = f.keeper.ConsumeFreeDepositQuota(sdkCtxNextBlock, valAddr)
	require.NoError(t, err)
	require.True(t, ok, "the cap must reset on a new block")
}

func TestConsumeFreeDepositQuota_PerValidator(t *testing.T) {
	f := initFixtureWithFakes(t)
	sdkCtx := sdkContextAt(t, f, 1)

	v1Addr := []byte{0x01}
	v2Addr := []byte{0x02}

	for i := 0; i < keeper.MaxFreeDepositsPerValidatorPerBlock; i++ {
		ok, err := f.keeper.ConsumeFreeDepositQuota(sdkCtx, v1Addr)
		require.NoError(t, err)
		require.True(t, ok)
	}
	ok, err := f.keeper.ConsumeFreeDepositQuota(sdkCtx, v1Addr)
	require.NoError(t, err)
	require.False(t, ok, "v1 is now over its own cap")

	ok, err = f.keeper.ConsumeFreeDepositQuota(sdkCtx, v2Addr)
	require.NoError(t, err)
	require.True(t, ok, "v2's quota is independent of v1's")
}
