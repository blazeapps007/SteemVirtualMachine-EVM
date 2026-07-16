package keeper_test

import (
	"context"
	"fmt"
	"testing"

	"cosmossdk.io/core/address"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"steemvm/x/steembridge/keeper"
	module "steemvm/x/steembridge/module"
	"steemvm/x/steembridge/types"
)

// fakeBankKeeper is a minimal, self-consistent in-memory implementation of
// types.BankKeeper sufficient to exercise the module's mint/burn logic in
// isolation, without depending on the real x/bank keeper.
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

// fakeStakingKeeper is a minimal, in-memory implementation of
// types.StakingKeeper letting tests directly control bonded status and
// voting power per validator, including changing them between confirmations
// to exercise the module's live-recomputed threshold math.
type fakeStakingKeeper struct {
	validators map[string]stakingtypes.Validator
}

func newFakeStakingKeeper() *fakeStakingKeeper {
	return &fakeStakingKeeper{validators: map[string]stakingtypes.Validator{}}
}

func (f *fakeStakingKeeper) setValidator(addr sdk.ValAddress, tokens int64, bonded bool) {
	status := stakingtypes.Unbonded
	if bonded {
		status = stakingtypes.Bonded
	}
	f.validators[addr.String()] = stakingtypes.Validator{
		OperatorAddress: addr.String(),
		Tokens:          math.NewInt(tokens),
		Status:          status,
	}
}

func (f *fakeStakingKeeper) setValidatorWithDetails(addr sdk.ValAddress, moniker, details string) {
	f.validators[addr.String()] = stakingtypes.Validator{
		OperatorAddress: addr.String(),
		Status:          stakingtypes.Bonded,
		Description:     stakingtypes.Description{Moniker: moniker, Details: details},
	}
}

func (f *fakeStakingKeeper) ConsensusAddressCodec() address.Codec {
	return addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32ConsensusAddrPrefix())
}

func (f *fakeStakingKeeper) ValidatorByConsAddr(context.Context, sdk.ConsAddress) (stakingtypes.ValidatorI, error) {
	return nil, fmt.Errorf("not implemented in fake")
}

func (f *fakeStakingKeeper) GetValidator(_ context.Context, addr sdk.ValAddress) (stakingtypes.Validator, error) {
	v, ok := f.validators[addr.String()]
	if !ok {
		return stakingtypes.Validator{}, fmt.Errorf("validator not found")
	}
	return v, nil
}

func (f *fakeStakingKeeper) TotalBondedTokens(context.Context) (math.Int, error) {
	total := math.ZeroInt()
	for _, v := range f.validators {
		if v.IsBonded() {
			total = total.Add(v.Tokens)
		}
	}
	return total, nil
}

// testValidator is a test-only bundle of a single validator's account bech32
// string (used as MsgSubmitSteemDeposit.Validator, the tx signer) and its
// corresponding operator address (same 20 bytes, different bech32 prefix).
type testValidator struct {
	AccAddr string
	ValAddr sdk.ValAddress
}

func newTestValidator(t *testing.T, seed byte) testValidator {
	t.Helper()
	raw := make([]byte, 20)
	raw[0] = seed
	return testValidator{
		AccAddr: sdk.AccAddress(raw).String(),
		ValAddr: sdk.ValAddress(raw),
	}
}

type fixtureWithFakes struct {
	ctx           context.Context
	keeper        keeper.Keeper
	addressCodec  address.Codec
	bankKeeper    *fakeBankKeeper
	stakingKeeper *fakeStakingKeeper
}

// sdkContextAt returns the fixture's context at a given block height.
func sdkContextAt(t *testing.T, f *fixtureWithFakes, height int64) sdk.Context {
	t.Helper()
	return sdk.UnwrapSDKContext(f.ctx).WithBlockHeight(height)
}

// initFixtureWithFakes builds a keeper wired to working fake bank/staking
// keepers, for tests that exercise mint/burn and validator-power logic.
// (The scaffolded initFixture wires nil bank/staking keepers and stays as-is
// for the tests that don't touch them.)
func initFixtureWithFakes(t *testing.T) *fixtureWithFakes {
	t.Helper()

	encCfg := moduletestutil.MakeTestEncodingConfig(module.AppModule{})
	addressCodec := addresscodec.NewBech32Codec(sdk.GetConfig().GetBech32AccountAddrPrefix())
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)

	storeService := runtime.NewKVStoreService(storeKey)
	ctx := testutil.DefaultContextWithDB(t, storeKey, storetypes.NewTransientStoreKey("transient_test")).Ctx

	authority := authtypes.NewModuleAddress(types.GovModuleName)

	bankKeeper := newFakeBankKeeper()
	stakingKeeper := newFakeStakingKeeper()

	k := keeper.NewKeeper(
		storeService,
		encCfg.Codec,
		addressCodec,
		authority,
		bankKeeper,
		stakingKeeper,
	)

	if err := k.Params.Set(ctx, types.DefaultParams()); err != nil {
		t.Fatalf("failed to set params: %v", err)
	}
	if err := k.Totals.Set(ctx, types.BridgeTotals{
		TotalMintedAsteem: math.ZeroInt(),
		TotalBurnedAsteem: math.ZeroInt(),
	}); err != nil {
		t.Fatalf("failed to set totals: %v", err)
	}

	return &fixtureWithFakes{
		ctx:           ctx,
		keeper:        k,
		addressCodec:  addressCodec,
		bankKeeper:    bankKeeper,
		stakingKeeper: stakingKeeper,
	}
}
