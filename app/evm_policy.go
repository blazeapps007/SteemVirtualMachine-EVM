package app

import (
	"slices"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	feemarkettypes "github.com/cosmos/evm/x/feemarket/types"
	evmtypes "github.com/cosmos/evm/x/vm/types"
)

// EVMGasFloor is the chain's minimum EVM gas price: 1 gwei, in asteem per gas.
// It is both the feemarket's min_gas_price (the floor the EIP-1559 base fee can
// never decay below) and its starting base_fee.
//
// Without a non-zero floor the base fee decays to zero on a quiet chain: with
// unlimited block gas (max_gas -1) the EIP-1559 target is unreachable, so it
// drops 12.5% every block. EVM txs then pay no fees, and the fee-split burn
// burns nothing. The node's app.toml minimum-gas-prices does not prevent this,
// because cosmos/evm skips it for EVM txs once London is active.
//
// It deliberately EQUALS the minimum-gas-prices every node and all three
// oracle clients already pay (1000000000asteem). Raising it would reject the
// oracles' price-feed txs and get validators slashed for missed duty.
//
// Single source of truth for both the fresh-genesis default
// (cmd/steemvmd/cmd/init_genesis_defaults.go) and the live-chain enforcement
// in the v0.0.5 upgrade handler (enforceEVMPolicy).
var EVMGasFloor = math.LegacyNewDec(1_000_000_000)

// StakingPrecompileAddress is cosmos/evm's staking precompile. It is active on
// this chain ONLY in its identity-gated form (stakingIdentityPrecompile, wired
// in app/evm.go): upstream's createValidator/editValidator call x/staking's msg
// server directly and EVM txs skip the Cosmos ante chain, so unwrapped it let
// any EVM account register an anonymous validator or strip an identity. Before
// v0.0.5 the live chain therefore had it switched off (governance proposal 3);
// the v0.0.5 upgrade switches it back on together with the gate, in one binary.
const StakingPrecompileAddress = "0x0000000000000000000000000000000000000800"

// enforceEVMPolicy brings live chain state in line with EVMGasFloor and the
// (now identity-gated) staking precompile being active (see applyEVMPolicy),
// writing only what changed.
func (app *App) enforceEVMPolicy(ctx sdk.Context) error {
	fm, fmChanged, evm, evmChanged := applyEVMPolicy(
		app.FeeMarketKeeper.GetParams(ctx),
		app.EVMKeeper.GetParams(ctx),
	)
	if fmChanged {
		if err := app.FeeMarketKeeper.SetParams(ctx, fm); err != nil {
			return err
		}
	}
	if evmChanged {
		if err := app.EVMKeeper.SetParams(ctx, evm); err != nil {
			return err
		}
	}
	return nil
}

// applyEVMPolicy is the pure core of enforceEVMPolicy. Idempotent: it only
// raises a min_gas_price/base_fee that is below EVMGasFloor (never lowers one
// governance set higher), and only adds the staking precompile if it is not
// already active. Every other field passes through untouched.
//
// Re-activating the staking precompile is only safe because this same binary
// registers it wrapped in the identity gate — this must never run on a binary
// that registers the upstream precompile unwrapped.
func applyEVMPolicy(fm feemarkettypes.Params, evm evmtypes.Params) (feemarkettypes.Params, bool, evmtypes.Params, bool) {
	fmChanged := false
	if fm.MinGasPrice.IsNil() || fm.MinGasPrice.LT(EVMGasFloor) {
		fm.MinGasPrice = EVMGasFloor
		fmChanged = true
	}
	if fm.BaseFee.IsNil() || fm.BaseFee.LT(EVMGasFloor) {
		fm.BaseFee = EVMGasFloor
		fmChanged = true
	}

	evmChanged := false
	if !slices.Contains(evm.ActiveStaticPrecompiles, StakingPrecompileAddress) {
		// Keep ascending order (all entries are same-length lowercase hex, so a
		// string sort is address order) rather than relying on SetParams to sort.
		active := append(slices.Clone(evm.ActiveStaticPrecompiles), StakingPrecompileAddress)
		slices.Sort(active)
		evm.ActiveStaticPrecompiles = active
		evmChanged = true
	}
	return fm, fmChanged, evm, evmChanged
}
