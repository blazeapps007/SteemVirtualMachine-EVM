package app

import (
	"context"
	"encoding/base64"
	"errors"
	"math/big"
	"testing"

	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/baseapp"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	cosmosante "github.com/cosmos/evm/ante/cosmos"
	evmaddress "github.com/cosmos/evm/encoding/address"
	stakingprecompile "github.com/cosmos/evm/precompiles/staking"
	feemarkettypes "github.com/cosmos/evm/x/feemarket/types"
	evmtypes "github.com/cosmos/evm/x/vm/types"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
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
	// what the last check was asked about
	valBytes         []byte
	moniker, details string
}

func (f *fakeIdentity) ValidateValidatorCreationEligibility(_ context.Context, v []byte, moniker, details string) error {
	f.createCalls++
	f.valBytes, f.moniker, f.details = v, moniker, details
	return f.createErr
}

func (f *fakeIdentity) ValidateValidatorEdit(_ context.Context, v []byte, moniker, details string) error {
	f.editCalls++
	f.valBytes, f.moniker, f.details = v, moniker, details
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

// ---------------------------------------------------------------------------
// EVM route: the staking precompile runs the same identity check.
// ---------------------------------------------------------------------------

var testValidatorHex = common.HexToAddress("0x0000000000000000000000000000000000000007")

func gatedStaking(identity validatorIdentityChecker) stakingIdentityPrecompile {
	return newStakingIdentityPrecompile(nil, identity,
		func(context.Context) (string, error) { return "asteem", nil },
		evmaddress.NewEvmCodec("steem"))
}

// decodeCall ABI-encodes a real call to the staking precompile with cosmos/evm's
// own ABI, then decodes it back the way cmn.SetupABI does — so the wrapper sees
// exactly the argument values and types a real EVM tx would produce.
func decodeCall(t *testing.T, name string, args ...interface{}) (*abi.Method, []interface{}) {
	t.Helper()
	input, err := stakingprecompile.ABI.Pack(name, args...)
	require.NoError(t, err)
	method, err := stakingprecompile.ABI.MethodById(input[:4])
	require.NoError(t, err)
	decoded, err := method.Inputs.Unpack(input[4:])
	require.NoError(t, err)
	return method, decoded
}

func createValidatorCall(t *testing.T, moniker, details string) (*abi.Method, []interface{}) {
	return decodeCall(t, stakingprecompile.CreateValidatorMethod,
		stakingprecompile.Description{Moniker: moniker, Details: details},
		stakingprecompile.Commission{Rate: big.NewInt(1e17), MaxRate: big.NewInt(2e17), MaxChangeRate: big.NewInt(1e16)},
		big.NewInt(1),
		testValidatorHex,
		base64.StdEncoding.EncodeToString(make([]byte, 32)),
		big.NewInt(1e18),
	)
}

const testSteemKeys = "owner=STM1;active=STM2;posting=STM3"

func TestStakingPrecompileRejectsAnonymousCreateValidator(t *testing.T) {
	identity := &fakeIdentity{createErr: errors.New("no active name-service registration")}
	method, args := createValidatorCall(t, "anon", "")

	err := gatedStaking(identity).checkValidatorIdentity(sdk.Context{}, method, args)

	require.ErrorIs(t, err, sdkerrors.ErrUnauthorized)
	require.Equal(t, 1, identity.createCalls)
}

func TestStakingPrecompileChecksTheCallersOwnIdentity(t *testing.T) {
	identity := &fakeIdentity{}
	method, args := createValidatorCall(t, "blazed007", testSteemKeys)

	require.NoError(t, gatedStaking(identity).checkValidatorIdentity(sdk.Context{}, method, args))

	// The check is asked about exactly what x/staking will receive: this
	// validator's own address, moniker and Steem keys.
	require.Equal(t, 1, identity.createCalls)
	require.Equal(t, testValidatorHex.Bytes(), identity.valBytes)
	require.Equal(t, "blazed007", identity.moniker)
	require.Equal(t, testSteemKeys, identity.details)
}

func TestStakingPrecompileGatesEditValidator(t *testing.T) {
	identity := &fakeIdentity{editErr: errors.New("registered to a different account")}
	method, args := decodeCall(t, stakingprecompile.EditValidatorMethod,
		stakingprecompile.Description{Moniker: "someone-else", Details: testSteemKeys},
		testValidatorHex,
		big.NewInt(-1), // commission rate: leave unchanged
		big.NewInt(-1), // min self delegation: leave unchanged
	)

	err := gatedStaking(identity).checkValidatorIdentity(sdk.Context{}, method, args)

	require.ErrorIs(t, err, sdkerrors.ErrUnauthorized)
	require.Equal(t, 1, identity.editCalls)
	require.Equal(t, "someone-else", identity.moniker)
}

func TestStakingPrecompileLeavesOrdinaryStakingAlone(t *testing.T) {
	// delegate/undelegate/redelegate/queries never consult the identity check —
	// a check that would fail proves it isn't even called.
	identity := &fakeIdentity{createErr: errors.New("must not be consulted"), editErr: errors.New("must not be consulted")}
	for _, name := range []string{"delegate", "undelegate", "redelegate", "cancelUnbondingDelegation", "validator", "delegation"} {
		method, ok := stakingprecompile.ABI.Methods[name]
		require.True(t, ok, "cosmos/evm staking ABI has no %q", name)
		require.NoError(t, gatedStaking(identity).checkValidatorIdentity(sdk.Context{}, &method, nil), name)
	}
	require.Zero(t, identity.createCalls+identity.editCalls)
}
