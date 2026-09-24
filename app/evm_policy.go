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

// StakingPrecompileAddress is cosmos/evm's staking precompile, which this chain
// keeps permanently disabled. Its createValidator/editValidator call x/staking's
// msg server directly, and EVM txs never pass through the Cosmos ante chain, so
// it bypasses the Steem validator-identity gate entirely: any EVM account could
// register an anonymous validator or strip its identity. Staking stays available
// through Cosmos txs. Do NOT re-activate it without wrapping those two methods in
// the same identity check the ante gate and icaIdentityRouter apply.
const StakingPrecompileAddress = "0x0000000000000000000000000000000000000800"

// enforceEVMPolicy brings live chain state in line with EVMGasFloor and the
// staking-precompile ban (see applyEVMPolicy), writing only what changed.
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
// governance set higher), and only removes the staking precompile if it is
// still active. Every other field passes through untouched.
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
	if slices.Contains(evm.ActiveStaticPrecompiles, StakingPrecompileAddress) {
		evm.ActiveStaticPrecompiles = slices.DeleteFunc(
			slices.Clone(evm.ActiveStaticPrecompiles),
			func(addr string) bool { return addr == StakingPrecompileAddress },
		)
		evmChanged = true
	}
	return fm, fmChanged, evm, evmChanged
}
