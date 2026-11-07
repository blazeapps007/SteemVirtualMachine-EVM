package app

import (
	"context"
	"encoding/base64"
	"errors"
	"math/big"
	"os"
	"sync"
	"testing"

	"cosmossdk.io/log/v2"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/client/flags"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	antetypes "github.com/cosmos/evm/ante/types"
	stakingprecompile "github.com/cosmos/evm/precompiles/staking"
	evmtypes "github.com/cosmos/evm/x/vm/types"
	gogoproto "github.com/cosmos/gogoproto/proto"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"steemvm/app/stakinggate"
)

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

// fakeStakingServer records which messages reached x/staking.
type fakeStakingServer struct {
	stakingtypes.MsgServer
	creates, edits int
}

func (s *fakeStakingServer) CreateValidator(context.Context, *stakingtypes.MsgCreateValidator) (*stakingtypes.MsgCreateValidatorResponse, error) {
	s.creates++
	return &stakingtypes.MsgCreateValidatorResponse{}, nil
}

func (s *fakeStakingServer) EditValidator(context.Context, *stakingtypes.MsgEditValidator) (*stakingtypes.MsgEditValidatorResponse, error) {
	s.edits++
	return &stakingtypes.MsgEditValidatorResponse{}, nil
}

func testOperator() sdk.ValAddress { return sdk.ValAddress(append(make([]byte, 19), 7)) }

func gatedServer(identity stakinggate.IdentityChecker) (stakingtypes.MsgServer, *fakeStakingServer) {
	inner := &fakeStakingServer{}
	gate := stakinggate.ProvideGate()
	if identity != nil {
		gate.Bind(identity)
	}
	return stakinggate.NewMsgServer(inner, gate), inner
}

func TestGateRefusesIneligibleValidatorsBeforeStaking(t *testing.T) {
	denied := errors.New("no active name-service registration")
	srv, inner := gatedServer(&fakeIdentity{createErr: denied, editErr: denied})

	_, err := srv.CreateValidator(context.Background(), &stakingtypes.MsgCreateValidator{ValidatorAddress: testOperator().String()})
	require.ErrorIs(t, err, sdkerrors.ErrUnauthorized)
	require.ErrorContains(t, err, denied.Error())

	_, err = srv.EditValidator(context.Background(), &stakingtypes.MsgEditValidator{ValidatorAddress: testOperator().String()})
	require.ErrorIs(t, err, sdkerrors.ErrUnauthorized)

	require.Zero(t, inner.creates+inner.edits, "x/staking must not be reached")
}

func TestGateChecksTheMessagesOwnIdentity(t *testing.T) {
	identity := &fakeIdentity{}
	srv, inner := gatedServer(identity)

	_, err := srv.CreateValidator(context.Background(), &stakingtypes.MsgCreateValidator{
		ValidatorAddress: testOperator().String(),
		Description:      stakingtypes.Description{Moniker: "blazed007", Details: "owner=STM1;active=STM2;posting=STM3"},
	})
	require.NoError(t, err)

	require.Equal(t, 1, identity.createCalls)
	require.Equal(t, testOperator().Bytes(), identity.valBytes)
	require.Equal(t, "blazed007", identity.moniker)
	require.Equal(t, "owner=STM1;active=STM2;posting=STM3", identity.details)
	require.Equal(t, 1, inner.creates)
}

func TestUnboundGateFailsClosed(t *testing.T) {
	srv, inner := gatedServer(nil)

	_, err := srv.CreateValidator(context.Background(), &stakingtypes.MsgCreateValidator{ValidatorAddress: testOperator().String()})
	require.Error(t, err)
	_, err = srv.EditValidator(context.Background(), &stakingtypes.MsgEditValidator{ValidatorAddress: testOperator().String()})
	require.Error(t, err)

	require.Zero(t, inner.creates+inner.edits)
}

var (
	testAppOnce sync.Once
	testAppVal  *App
)

// realTestApp builds the real app once per test process: cosmos/evm keeps its
// chain config in a global that can only be set once.
func realTestApp() *App {
	testAppOnce.Do(func() {
		home, err := os.MkdirTemp("", "steemvm-app-test")
		if err != nil {
			panic(err)
		}
		appOptions := make(simtestutil.AppOptionsMap, 0)
		appOptions[flags.FlagHome] = home
		testAppVal = New(log.NewNopLogger(), dbm.NewMemDB(), true, appOptions)
	})
	return testAppVal
}

// newGateTestApp returns the real app, so the wiring under test is the one a
// node runs, with its identity gate rebound to the given fake.
func newGateTestApp(t *testing.T, identity stakinggate.IdentityChecker) (*App, sdk.Context) {
	t.Helper()
	a := realTestApp()
	a.StakingIdentityGate.Bind(identity)
	t.Cleanup(func() { a.StakingIdentityGate.Bind(a.SteembridgeKeeper) })
	return a, a.NewContextLegacy(true, cmtproto.Header{Height: 1})
}

// The router is what direct txs, authz, ICA and governance all dispatch through.
func TestAppRouterEnforcesIdentity(t *testing.T) {
	denied := errors.New("no active name-service registration")
	identity := &fakeIdentity{createErr: denied, editErr: denied}
	a, ctx := newGateTestApp(t, identity)

	create := &stakingtypes.MsgCreateValidator{ValidatorAddress: testOperator().String()}
	h := a.MsgServiceRouter().Handler(create)
	require.NotNil(t, h)
	_, err := h(ctx, create)
	require.ErrorContains(t, err, denied.Error())
	require.Equal(t, 1, identity.createCalls)

	edit := &stakingtypes.MsgEditValidator{ValidatorAddress: testOperator().String()}
	h = a.MsgServiceRouter().Handler(edit)
	require.NotNil(t, h)
	_, err = h(ctx, edit)
	require.ErrorContains(t, err, denied.Error())
	require.Equal(t, 1, identity.editCalls)
}

// The gated module must still register everything else x/staking registers.
func TestAppRouterKeepsOtherStakingMessages(t *testing.T) {
	identity := &fakeIdentity{createErr: errors.New("must not be consulted")}
	a, _ := newGateTestApp(t, identity)

	for _, msg := range []sdk.Msg{
		&stakingtypes.MsgDelegate{},
		&stakingtypes.MsgUndelegate{},
		&stakingtypes.MsgBeginRedelegate{},
		&stakingtypes.MsgCancelUnbondingDelegation{},
		&stakingtypes.MsgUpdateParams{},
	} {
		require.NotNil(t, a.MsgServiceRouter().Handler(msg), "%T", msg)
	}
	require.Zero(t, identity.createCalls+identity.editCalls)
}

// noCodeStateDB makes the caller an ordinary account, which is all the precompile
// asks the state before it reaches its MsgServer.
type noCodeStateDB struct{ vm.StateDB }

func (noCodeStateDB) GetCode(common.Address) []byte { return nil }

func TestStakingPrecompileEnforcesIdentity(t *testing.T) {
	denied := errors.New("no active name-service registration")
	identity := &fakeIdentity{createErr: denied}
	a, ctx := newGateTestApp(t, identity)

	addr := common.HexToAddress(evmtypes.StakingPrecompileAddress)
	precompile, found, err := a.EVMKeeper.GetStaticPrecompileInstance(&evmtypes.Params{ActiveStaticPrecompiles: []string{addr.Hex()}}, addr)
	require.NoError(t, err)
	require.True(t, found)
	staking, ok := precompile.(*stakingprecompile.Precompile)
	require.True(t, ok, "the precompile a node runs is %T", precompile)

	// Encode and decode the call with cosmos/evm's own ABI, so the arguments have
	// exactly the types a real EVM tx produces.
	operator := common.BytesToAddress(testOperator())
	input, err := stakingprecompile.ABI.Pack(stakingprecompile.CreateValidatorMethod,
		stakingprecompile.Description{Moniker: "anon"},
		stakingprecompile.Commission{Rate: big.NewInt(1e17), MaxRate: big.NewInt(2e17), MaxChangeRate: big.NewInt(1e16)},
		big.NewInt(1),
		operator,
		base64.StdEncoding.EncodeToString(make([]byte, 32)),
		big.NewInt(1e18),
	)
	require.NoError(t, err)
	method, err := stakingprecompile.ABI.MethodById(input[:4])
	require.NoError(t, err)
	args, err := method.Inputs.Unpack(input[4:])
	require.NoError(t, err)

	contract := vm.NewContract(operator, addr, uint256.NewInt(0), 10_000_000, nil)
	_, err = staking.CreateValidator(ctx, contract, noCodeStateDB{}, method, args)
	require.ErrorContains(t, err, denied.Error())
	require.Equal(t, 1, identity.createCalls)
}

// ---- ante routing ----

type extensionTx struct {
	msgsTx
	opts []*codectypes.Any
}

func (t extensionTx) GetExtensionOptions() []*codectypes.Any { return t.opts }

func (t extensionTx) GetNonCriticalExtensionOptions() []*codectypes.Any { return nil }

func anyOf(t *testing.T, msg gogoproto.Message) *codectypes.Any {
	t.Helper()
	a, err := codectypes.NewAnyWithValue(msg)
	require.NoError(t, err)
	return a
}

// Upstream's handler has no authz limit on validator messages, so only a real
// Ethereum tx may be routed to it.
func TestOnlyEthereumTxsReachUpstreamAnteHandler(t *testing.T) {
	create := &stakingtypes.MsgCreateValidator{ValidatorAddress: testOperator().String()}
	tests := map[string]struct {
		opts []*codectypes.Any
		want bool
	}{
		"no extension":          {nil, false},
		"dynamic fee":           {[]*codectypes.Any{anyOf(t, &antetypes.ExtensionOptionDynamicFeeTx{})}, false},
		"ethereum":              {[]*codectypes.Any{anyOf(t, &evmtypes.ExtensionOptionsEthereumTx{})}, true},
		"dynamic fee, then eth": {[]*codectypes.Any{anyOf(t, &antetypes.ExtensionOptionDynamicFeeTx{}), anyOf(t, &evmtypes.ExtensionOptionsEthereumTx{})}, false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, isEthereumTx(extensionTx{msgsTx{[]sdk.Msg{create}}, tc.opts}))
		})
	}
}
