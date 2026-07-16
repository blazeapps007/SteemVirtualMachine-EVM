package steembridge_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/address"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"

	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"

	precompile "steemvm/precompiles/steembridge"
	"steemvm/x/steembridge/keeper"
	module "steemvm/x/steembridge/module"
	"steemvm/x/steembridge/types"
)

// fakeBankKeeper is a minimal in-memory bank sufficient for the bridge-out
// burn path, mirroring x/steembridge/keeper/fakes_test.go (which lives in
// another test package and cannot be imported). It also satisfies the
// precompile common.BankKeeper interface trivially (only the module keeper's
// methods are exercised — Execute-level tests never invoke the balance
// handler).
type fakeBankKeeper struct {
	balances map[string]sdk.Coins
}

func newFakeBankKeeper() *fakeBankKeeper {
	return &fakeBankKeeper{balances: map[string]sdk.Coins{}}
}

func (f *fakeBankKeeper) SpendableCoins(_ context.Context, addr sdk.AccAddress) sdk.Coins {
	return f.balances[addr.String()]
}

func (f *fakeBankKeeper) MintCoins(_ context.Context, moduleName string, amt sdk.Coins) error {
	f.balances[moduleName] = f.balances[moduleName].Add(amt...)
	return nil
}

func (f *fakeBankKeeper) BurnCoins(_ context.Context, moduleName string, amt sdk.Coins) error {
	newBal, negative := f.balances[moduleName].SafeSub(amt...)
	if negative {
		return fmt.Errorf("insufficient module balance")
	}
	f.balances[moduleName] = newBal
	return nil
}

func (f *fakeBankKeeper) SendCoinsFromAccountToModule(_ context.Context, sender sdk.AccAddress, moduleName string, amt sdk.Coins) error {
	key := sender.String()
	newBal, negative := f.balances[key].SafeSub(amt...)
	if negative {
		return fmt.Errorf("insufficient account balance")
	}
	f.balances[key] = newBal
	f.balances[moduleName] = f.balances[moduleName].Add(amt...)
	return nil
}

func (f *fakeBankKeeper) SendCoinsFromModuleToAccount(_ context.Context, moduleName string, recipient sdk.AccAddress, amt sdk.Coins) error {
	newBal, negative := f.balances[moduleName].SafeSub(amt...)
	if negative {
		return fmt.Errorf("insufficient module balance")
	}
	f.balances[moduleName] = newBal
	key := recipient.String()
	f.balances[key] = f.balances[key].Add(amt...)
	return nil
}

// The following methods satisfy the precompile common.BankKeeper interface;
// they are never called by Execute-level tests (the balance handler only
// runs inside RunNativeAction, which is out of scope here).
func (f *fakeBankKeeper) IterateAccountBalances(context.Context, sdk.AccAddress, func(sdk.Coin) bool) {
}
func (f *fakeBankKeeper) IterateTotalSupply(context.Context, func(sdk.Coin) bool) {}
func (f *fakeBankKeeper) GetSupply(context.Context, string) sdk.Coin              { return sdk.Coin{} }
func (f *fakeBankKeeper) GetDenomMetaData(context.Context, string) (banktypes.Metadata, bool) {
	return banktypes.Metadata{}, false
}
func (f *fakeBankKeeper) SetDenomMetaData(context.Context, banktypes.Metadata) {}
func (f *fakeBankKeeper) GetBalance(context.Context, sdk.AccAddress, string) sdk.Coin {
	return sdk.Coin{}
}
func (f *fakeBankKeeper) SendCoins(context.Context, sdk.AccAddress, sdk.AccAddress, sdk.Coins) error {
	return nil
}
func (f *fakeBankKeeper) SpendableCoin(context.Context, sdk.AccAddress, string) sdk.Coin {
	return sdk.Coin{}
}
func (f *fakeBankKeeper) BlockedAddr(sdk.AccAddress) bool { return false }

// fakeStateDB records logs; embedding the interface satisfies vm.StateDB
// while leaving unimplemented methods nil (Execute only calls AddLog).
type fakeStateDB struct {
	vm.StateDB
	logs []*ethtypes.Log
}

func (f *fakeStateDB) AddLog(log *ethtypes.Log) { f.logs = append(f.logs, log) }

type fixture struct {
	ctx          sdk.Context
	keeper       keeper.Keeper
	precompile   *precompile.Precompile
	bankKeeper   *fakeBankKeeper
	addressCodec address.Codec
	stateDB      *fakeStateDB
}

func initFixture(t *testing.T) *fixture {
	t.Helper()

	encCfg := moduletestutil.MakeTestEncodingConfig(module.AppModule{})
	addressCodec := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)

	storeService := runtime.NewKVStoreService(storeKey)
	ctx := testutil.DefaultContextWithDB(t, storeKey, storetypes.NewTransientStoreKey("transient_test")).Ctx

	authority := authtypes.NewModuleAddress(types.GovModuleName)
	bankKeeper := newFakeBankKeeper()

	k := keeper.NewKeeper(storeService, encCfg.Codec, addressCodec, authority, bankKeeper, nil)

	params := types.DefaultParams()
	params.NameServiceEnabled = true
	params.BridgeOutEnabled = true
	params.GatewayAccount = "gateway-account"
	require.NoError(t, k.Params.Set(ctx, params))
	require.NoError(t, k.Totals.Set(ctx, types.BridgeTotals{
		TotalMintedAsteem: math.ZeroInt(),
		TotalBurnedAsteem: math.ZeroInt(),
	}))

	p := precompile.NewPrecompile(k, keeper.NewMsgServerImpl(k), bankKeeper, addressCodec)

	return &fixture{
		ctx:          ctx,
		keeper:       k,
		precompile:   p,
		bankKeeper:   bankKeeper,
		addressCodec: addressCodec,
		stateDB:      &fakeStateDB{},
	}
}

// packCall ABI-encodes a call to the given method.
func packCall(t *testing.T, method string, args ...interface{}) []byte {
	t.Helper()
	input, err := precompile.ABI.Pack(method, args...)
	require.NoError(t, err)
	return input
}

// newContract builds a call frame against the precompile with the given
// caller and calldata.
func newContract(caller common.Address, input []byte) *vm.Contract {
	c := vm.NewContract(caller, common.HexToAddress(precompile.PrecompileAddress), uint256.NewInt(0), 10_000_000, nil)
	c.Input = input
	return c
}

// seedAwaitingRegistration writes an AWAITING_CONFIRMATION registration with
// all the indexes the msg server would have created, destined to dest.
func seedAwaitingRegistration(t *testing.T, f *fixture, id uint64, steemAccount string, dest common.Address) {
	t.Helper()

	destBech, err := f.addressCodec.BytesToString(dest.Bytes())
	require.NoError(t, err)

	registration := types.NameRegistration{
		Id:                 id,
		Txid:               fmt.Sprintf("%040x", id),
		OpIndex:            0,
		SteemBlock:         100,
		SteemTimestamp:     "2024-01-01T00:00:00",
		SteemAccount:       steemAccount,
		GatewayAccount:     "gateway-account",
		AmountMillisteem:   1,
		Memo:               destBech,
		DerivedDestination: destBech,
		DestinationType:    types.DestinationType_DESTINATION_TYPE_COSMOS,
		Status:             types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_AWAITING_CONFIRMATION,
		CreatedAt:          1,
		AwaitingSince:      1,
	}
	require.NoError(t, f.keeper.NameRegistration.Set(f.ctx, id, registration))
	require.NoError(t, f.keeper.NameRegistrationByTxid.Set(f.ctx, collections.Join(registration.Txid, registration.OpIndex), id))
	require.NoError(t, f.keeper.NameRegistrationByStatus.Set(f.ctx, collections.Join(int32(registration.Status), id)))
	require.NoError(t, f.keeper.NameRegistrationByAccount.Set(f.ctx, collections.Join(steemAccount, id)))
	require.NoError(t, f.keeper.NameRegistrationAwaitingByDest.Set(f.ctx, collections.Join(dest.Bytes(), id)))
	seq, err := f.keeper.NameRegistrationSeq.Peek(f.ctx)
	require.NoError(t, err)
	if id >= seq {
		require.NoError(t, f.keeper.NameRegistrationSeq.Set(f.ctx, id+1))
	}
}
