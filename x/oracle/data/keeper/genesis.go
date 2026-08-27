package keeper

import (
	"context"

	"steemvm/x/oracle/data/types"
)

// InitGenesis initializes the module's state from a provided genesis state.
func (k Keeper) InitGenesis(ctx context.Context, genState types.GenesisState) error {
	for _, elem := range genState.ExchangeRates {
		if err := k.ExchangeRate.Set(ctx, elem.Pair, elem); err != nil {
			return err
		}
	}

	return k.Params.Set(ctx, genState.Params)
}

// ExportGenesis returns the module's exported genesis.
func (k Keeper) ExportGenesis(ctx context.Context) (*types.GenesisState, error) {
	var err error

	genesis := types.DefaultGenesis()
	genesis.Params, err = k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}

	err = k.ExchangeRate.Walk(ctx, nil, func(_ string, elem types.ExchangeRate) (bool, error) {
		genesis.ExchangeRates = append(genesis.ExchangeRates, elem)
		return false, nil
	})
	if err != nil {
		return nil, err
	}

	return genesis, nil
}
