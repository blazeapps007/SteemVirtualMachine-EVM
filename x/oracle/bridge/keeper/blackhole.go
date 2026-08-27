package keeper

import (
	"context"

	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"steemvm/x/oracle/bridge/types"
)

// SweepBlackHole burns the entire balance of the STEEMBLACKHOLE module account.
// It is the app's SOLE burn point: bridge-out net amounts, the fee-split 25%
// burn (§4b), and any coins anyone sends to the black-hole address accumulate
// here and are destroyed every EndBlock. Deposits mint into and send out of the
// black hole atomically within their own tx, so this sweep only ever catches
// coins meant to die — never user funds. Called from the module's EndBlock.
func (k Keeper) SweepBlackHole(ctx context.Context) error {
	addr := authtypes.NewModuleAddress(types.BlackHoleModuleName)
	balance := k.bankKeeper.GetAllBalances(ctx, addr)
	if balance.IsZero() {
		return nil
	}
	return k.bankKeeper.BurnCoins(ctx, types.BlackHoleModuleName, balance)
}
