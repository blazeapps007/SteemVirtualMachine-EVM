package app

import (
	"context"
	"errors"
	"testing"

	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/baseapp"
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
// ICA route: the host's router must apply the identity gate to create/edit.
// ---------------------------------------------------------------------------

type fakeIdentity struct {
	createErr, editErr     error
	createCalls, editCalls int
}

func (f *fakeIdentity) ValidateValidatorCreationEligibility(context.Context, []byte, string, string) error {
	f.createCalls++
	return f.createErr
}

func (f *fakeIdentity) ValidateValidatorEdit(context.Context, []byte, string, string) error {
	f.editCalls++
	return f.editErr
}

type fakeRouter struct {
	noHandler bool
	calls     int
}

func (r *fakeRouter) Handler(sdk.Msg) baseapp.MsgServiceHandler {
	if r.noHandler {
		return nil
	}
	return func(sdk.Context, sdk.Msg) (*sdk.Result, error) {
		r.calls++
		return &sdk.Result{}, nil
	}
}

func testValoper() string { return sdk.ValAddress(testAddr(7)).String() }

func TestICAIdentityRouterRejectsIneligibleValidatorMsgs(t *testing.T) {
	denied := errors.New("no active name-service registration")
	cases := map[string]struct {
		msg      sdk.Msg
		identity *fakeIdentity
	}{
		"create": {&stakingtypes.MsgCreateValidator{ValidatorAddress: testValoper()}, &fakeIdentity{createErr: denied}},
		"edit":   {&stakingtypes.MsgEditValidator{ValidatorAddress: testValoper()}, &fakeIdentity{editErr: denied}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			inner := &fakeRouter{}
			h := newICAIdentityRouter(inner, tc.identity).Handler(tc.msg)
			require.NotNil(t, h)
			_, err := h(sdk.Context{}, tc.msg)
			require.ErrorContains(t, err, denied.Error())
			require.Zero(t, inner.calls, "x/staking must not be reached")
		})
	}
}

func TestICAIdentityRouterDispatchesEligibleValidatorMsgs(t *testing.T) {
	identity := &fakeIdentity{}
	inner := &fakeRouter{}
	r := newICAIdentityRouter(inner, identity)

	create := &stakingtypes.MsgCreateValidator{ValidatorAddress: testValoper()}
	_, err := r.Handler(create)(sdk.Context{}, create)
	require.NoError(t, err)

	edit := &stakingtypes.MsgEditValidator{ValidatorAddress: testValoper()}
	_, err = r.Handler(edit)(sdk.Context{}, edit)
	require.NoError(t, err)

	require.Equal(t, 1, identity.createCalls)
	require.Equal(t, 1, identity.editCalls)
	require.Equal(t, 2, inner.calls)
}

func TestICAIdentityRouterPassesOtherMsgsThrough(t *testing.T) {
	identity := &fakeIdentity{createErr: errors.New("must not be consulted")}
	inner := &fakeRouter{}
	r := newICAIdentityRouter(inner, identity)

	send := &banktypes.MsgSend{}
	_, err := r.Handler(send)(sdk.Context{}, send)
	require.NoError(t, err)
	require.Equal(t, 1, inner.calls)
	require.Zero(t, identity.createCalls+identity.editCalls)

	// An unroutable message stays unroutable (the host reports it), rather
	// than being turned into a non-nil handler.
	require.Nil(t, newICAIdentityRouter(&fakeRouter{noHandler: true}, identity).
		Handler(&stakingtypes.MsgCreateValidator{}))
}

// ---------------------------------------------------------------------------
// v0.0.5 state policy: 1 gwei floor + staking precompile disabled.
// ---------------------------------------------------------------------------

const otherPrecompile = "0x0000000000000000000000000000000000000801"

func liveLikeParams() (feemarkettypes.Params, evmtypes.Params) {
	fm := feemarkettypes.DefaultParams()
	fm.MinGasPrice = math.LegacyZeroDec()
	fm.BaseFee = math.LegacyMustNewDecFromStr("0.000000000000000007") // live value
	evm := evmtypes.DefaultParams()
	evm.ActiveStaticPrecompiles = []string{StakingPrecompileAddress, otherPrecompile}
	return fm, evm
}

func TestApplyEVMPolicyRaisesFloorAndDisablesStaking(t *testing.T) {
	fmIn, evmIn := liveLikeParams()
	originalList := append([]string(nil), evmIn.ActiveStaticPrecompiles...)

	fm, fmChanged, evm, evmChanged := applyEVMPolicy(fmIn, evmIn)

	require.True(t, fmChanged)
	require.True(t, fm.MinGasPrice.Equal(EVMGasFloor))
	require.True(t, fm.BaseFee.Equal(EVMGasFloor))
	require.Equal(t, fmIn.ElasticityMultiplier, fm.ElasticityMultiplier, "unrelated fields untouched")

	require.True(t, evmChanged)
	require.Equal(t, []string{otherPrecompile}, evm.ActiveStaticPrecompiles)
	require.Equal(t, originalList, evmIn.ActiveStaticPrecompiles, "input slice must not be mutated")
}

func TestApplyEVMPolicyIsIdempotent(t *testing.T) {
	fm, _, evm, _ := applyEVMPolicy(liveLikeParams())
	_, fmChanged, _, evmChanged := applyEVMPolicy(fm, evm)
	require.False(t, fmChanged)
	require.False(t, evmChanged)
}

func TestApplyEVMPolicyNeverLowersAHigherGovernanceFloor(t *testing.T) {
	fmIn, evmIn := liveLikeParams()
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
