package keeper

import (
	"context"

	"steemvm/x/oracle/types"
)

// InitGenesis initializes the module's state from a provided genesis state.
// Per-window tallies are transient and start empty after genesis/upgrade.
func (k Keeper) InitGenesis(ctx context.Context, genState types.GenesisState) error {
	return k.Params.Set(ctx, genState.Params)
}

// ExportGenesis returns the module's exported genesis.
func (k Keeper) ExportGenesis(ctx context.Context) (*types.GenesisState, error) {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}
	return &types.GenesisState{Params: params}, nil
}
