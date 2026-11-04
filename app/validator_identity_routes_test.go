package app

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	cosmosante "github.com/cosmos/evm/ante/cosmos"
	feemarkettypes "github.com/cosmos/evm/x/feemarket/types"
	evmtypes "github.com/cosmos/evm/x/vm/types"
	"github.com/stretchr/testify/require"
	protov2 "google.golang.org/protobuf/proto"
)

// ---------------------------------------------------------------------------
// authz route: validator create/edit must never be wrappable in authz.
// ---------------------------------------------------------------------------

type msgsTx struct{ msgs []sdk.Msg }

func (t msgsTx) GetMsgs() []sdk.Msg                    { return t.msgs }
func (t msgsTx) GetMsgsV2() ([]protov2.Message, error) { return nil, nil }

func runAuthzLimiter(t *testing.T, msgs ...sdk.Msg) (nextCalled bool, err error) {
	t.Helper()
	dec := cosmosante.NewAuthzLimiterDecorator(authzDisabledMsgTypes()...)
	_, err = dec.AnteHandle(sdk.Context{}, msgsTx{msgs: msgs}, false,
		func(ctx sdk.Context, _ sdk.Tx, _ bool) (sdk.Context, error) {
			nextCalled = true
			return ctx, nil
		})
	return nextCalled, err
}

func testAddr(b byte) sdk.AccAddress {
	return sdk.AccAddress(append(make([]byte, 19), b))
}

func execOf(msgs ...sdk.Msg) *authz.MsgExec {
	exec := authz.NewMsgExec(testAddr(2), msgs)
	return &exec
}

func TestAuthzLimiterBlocksWrappedValidatorCreateAndEdit(t *testing.T) {
	for name, inner := range map[string]sdk.Msg{
		"create": &stakingtypes.MsgCreateValidator{},
		"edit":   &stakingtypes.MsgEditValidator{},
	} {
		t.Run(name+" in MsgExec", func(t *testing.T) {
			next, err := runAuthzLimiter(t, execOf(inner))
			require.Error(t, err)
			require.False(t, next)
		})
		t.Run(name+" in nested MsgExec", func(t *testing.T) {
			next, err := runAuthzLimiter(t, execOf(execOf(inner)))
			require.Error(t, err)
			require.False(t, next)
		})
		t.Run(name+" granted via MsgGrant", func(t *testing.T) {
			grant, err := authz.NewMsgGrant(testAddr(1), testAddr(2),
				authz.NewGenericAuthorization(sdk.MsgTypeURL(inner)), nil)
			require.NoError(t, err)
			next, err := runAuthzLimiter(t, grant)
			require.Error(t, err)
			require.False(t, next)
		})
	}
}

func TestAuthzLimiterStillAllowsLegitimateTraffic(t *testing.T) {
	// A top-level MsgCreateValidator passes the limiter — it is the identity
	// gate further down the ante chain that checks it, not the limiter.
	next, err := runAuthzLimiter(t, &stakingtypes.MsgCreateValidator{})
	require.NoError(t, err)
	require.True(t, next)

	// Ordinary authz use is untouched.
	next, err = runAuthzLimiter(t, execOf(&banktypes.MsgSend{}))
	require.NoError(t, err)
	require.True(t, next)
}

// ---------------------------------------------------------------------------
// v0.0.5 state policy: 1 gwei floor + identity-gated staking precompile on.
// ---------------------------------------------------------------------------

const (
	p256Precompile  = "0x0000000000000000000000000000000000000100"
	distrPrecompile = "0x0000000000000000000000000000000000000801"
	bridgePrecomp   = "0x0000000000000000000000000000000000000900"
)

// liveParams is the live chain after governance proposal 3: 1 gwei floor
// already set, staking precompile switched off.
func liveParams() (feemarkettypes.Params, evmtypes.Params) {
	fm := feemarkettypes.DefaultParams()
	fm.MinGasPrice, fm.BaseFee = EVMGasFloor, EVMGasFloor
	evm := evmtypes.DefaultParams()
	evm.ActiveStaticPrecompiles = []string{p256Precompile, distrPrecompile, bridgePrecomp}
	return fm, evm
}

// preProposalParams is the chain had proposal 3 never passed: fee floor
// decayed to ~0, staking precompile still on.
func preProposalParams() (feemarkettypes.Params, evmtypes.Params) {
	fm := feemarkettypes.DefaultParams()
	fm.MinGasPrice = math.LegacyZeroDec()
	fm.BaseFee = math.LegacyMustNewDecFromStr("0.000000000000000007")
	evm := evmtypes.DefaultParams()
	evm.ActiveStaticPrecompiles = []string{p256Precompile, StakingPrecompileAddress, distrPrecompile}
	return fm, evm
}

func TestApplyEVMPolicyReEnablesStakingInSortedPosition(t *testing.T) {
	fmIn, evmIn := liveParams()
	originalList := append([]string(nil), evmIn.ActiveStaticPrecompiles...)

	_, fmChanged, evm, evmChanged := applyEVMPolicy(fmIn, evmIn)

	require.False(t, fmChanged, "floor already at 1 gwei")
	require.True(t, evmChanged)
	require.Equal(t, []string{p256Precompile, StakingPrecompileAddress, distrPrecompile, bridgePrecomp},
		evm.ActiveStaticPrecompiles, "inserted in address order, nothing else touched")
	require.Equal(t, originalList, evmIn.ActiveStaticPrecompiles, "input slice must not be mutated")
}

func TestApplyEVMPolicyRaisesFloorWhenProposalNeverPassed(t *testing.T) {
	fmIn, evmIn := preProposalParams()

	fm, fmChanged, _, evmChanged := applyEVMPolicy(fmIn, evmIn)

	require.True(t, fmChanged)
	require.True(t, fm.MinGasPrice.Equal(EVMGasFloor))
	require.True(t, fm.BaseFee.Equal(EVMGasFloor))
	require.Equal(t, fmIn.ElasticityMultiplier, fm.ElasticityMultiplier, "unrelated fields untouched")
	require.False(t, evmChanged, "staking precompile already active")
}

func TestApplyEVMPolicyIsIdempotent(t *testing.T) {
	for _, start := range []func() (feemarkettypes.Params, evmtypes.Params){liveParams, preProposalParams} {
		fm, _, evm, _ := applyEVMPolicy(start())
		_, fmChanged, _, evmChanged := applyEVMPolicy(fm, evm)
		require.False(t, fmChanged)
		require.False(t, evmChanged)
	}
}

func TestApplyEVMPolicyNeverLowersAHigherGovernanceFloor(t *testing.T) {
	fmIn, evmIn := preProposalParams()
	higher := EVMGasFloor.MulInt64(2)
	fmIn.MinGasPrice, fmIn.BaseFee = higher, higher

	fm, fmChanged, _, _ := applyEVMPolicy(fmIn, evmIn)
	require.False(t, fmChanged)
	require.True(t, fm.MinGasPrice.Equal(higher))
}

func TestEVMGasFloorIsOneGwei(t *testing.T) {
	// Must equal the 1000000000asteem minimum-gas-prices the nodes and all
	// three oracle clients pay; anything higher breaks the price feed.
	require.Equal(t, "1000000000.000000000000000000", EVMGasFloor.String())
}
