package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"steemvm/x/oracle/bridge/types"
)

// SplitFees runs in BeginBlock, BEFORE distribution's AllocateTokens, and
// implements the 50/25/25 tx-fee split plus the bridge-reward payout:
//   - 25% of the fee_collector balance -> community pool
//   - 25% -> STEEMBLACKHOLE (burned by the EndBlock sweep)
//   - the remaining ~50% stays in fee_collector for distribution to stakers
//
// It then moves the accrued bridge fee (bridge_reward) into fee_collector so
// distribution pays it out 100% to stakers at each validator's commission
// (§4b) — kept out of the 50/25/25 split above. Requires distribution's
// community_tax = 0 so the 50% left is not further taxed.
func (k Keeper) SplitFees(ctx context.Context) error {
	feeCollector := authtypes.NewModuleAddress(authtypes.FeeCollectorName)
	balance := k.bankKeeper.GetAllBalances(ctx, feeCollector)

	for _, coin := range balance {
		quarter := coin.Amount.QuoRaw(4)
		if quarter.IsZero() {
			continue
		}
		quarterCoins := sdk.NewCoins(sdk.NewCoin(coin.Denom, quarter))
		// 25% -> community pool
		if err := k.distrKeeper.FundCommunityPool(ctx, quarterCoins, feeCollector); err != nil {
			return err
		}
		// 25% -> STEEMBLACKHOLE (burned by the EndBlock sweep)
		if err := k.bankKeeper.SendCoinsFromModuleToModule(ctx, authtypes.FeeCollectorName, types.BlackHoleModuleName, quarterCoins); err != nil {
			return err
		}
		// remaining ~50% (incl. rounding remainder) stays for distribution -> stakers.
	}

	// Route accrued bridge fees to stakers via fee_collector (community_tax=0 => 100%).
	bridgeReward := k.bankKeeper.GetAllBalances(ctx, authtypes.NewModuleAddress(types.BridgeRewardModuleName))
	if !bridgeReward.IsZero() {
		if err := k.bankKeeper.SendCoinsFromModuleToModule(ctx, types.BridgeRewardModuleName, authtypes.FeeCollectorName, bridgeReward); err != nil {
			return err
		}
	}
	return nil
}
