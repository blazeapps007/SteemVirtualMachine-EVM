package oracledata

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/core/vm"

	cmn "github.com/cosmos/evm/precompiles/common"

	"cosmossdk.io/collections"

	sdk "github.com/cosmos/cosmos-sdk/types"

	oracledatatypes "steemvm/x/oracle/data/types"
)

const (
	// GetPriceMethod defines the ABI method name for the getPrice query.
	GetPriceMethod = "getPrice"
	// GetPricesMethod defines the ABI method name for the getPrices query.
	GetPricesMethod = "getPrices"
)

// GetPrice returns the latest finalized rate for a currency pair. A pair with no
// finalized rate deliberately returns zeros instead of reverting (the resolver
// convention): no real rate is ever 0, so callers probe with `rate != 0` and no
// try/catch. The rate is packed as its 18-decimal fixed-point mantissa
// (LegacyDec.BigInt()), so a Solidity consumer recovers the value with `/ 1e18`.
func (p Precompile) GetPrice(
	ctx sdk.Context,
	method *abi.Method,
	_ *vm.Contract,
	args []interface{},
) ([]byte, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf(cmn.ErrInvalidNumberOfArgs, 1, len(args))
	}

	pair, ok := args[0].(string)
	if !ok {
		return nil, fmt.Errorf("invalid pair")
	}

	rate, err := p.keeper.ExchangeRate.Get(ctx, pair)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return method.Outputs.Pack(big.NewInt(0), big.NewInt(0), big.NewInt(0))
		}
		return nil, err
	}

	return method.Outputs.Pack(
		rate.Rate.BigInt(),
		new(big.Int).SetUint64(rate.UpdateEpoch),
		big.NewInt(rate.UpdateTime),
	)
}

// GetPrices returns every finalized rate as three parallel arrays
// (pairs[i]/rates[i]/updateTimes[i]). Rates are 18-decimal fixed-point mantissas.
func (p Precompile) GetPrices(
	ctx sdk.Context,
	method *abi.Method,
	_ *vm.Contract,
	args []interface{},
) ([]byte, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf(cmn.ErrInvalidNumberOfArgs, 0, len(args))
	}

	// Initialize non-nil so an empty result packs as empty arrays.
	pairs := []string{}
	rates := []*big.Int{}
	updateTimes := []*big.Int{}
	err := p.keeper.ExchangeRate.Walk(ctx, nil, func(pair string, rate oracledatatypes.ExchangeRate) (bool, error) {
		pairs = append(pairs, pair)
		rates = append(rates, rate.Rate.BigInt())
		updateTimes = append(updateTimes, big.NewInt(rate.UpdateTime))
		return false, nil
	})
	if err != nil {
		return nil, err
	}

	return method.Outputs.Pack(pairs, rates, updateTimes)
}
