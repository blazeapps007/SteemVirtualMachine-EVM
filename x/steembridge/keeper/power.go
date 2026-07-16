package keeper

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"steemvm/x/steembridge/types"
)

// confirmedPowerRatio computes the fraction of current bonded voting power
// represented by the given validator confirmations. Voting power is
// recomputed live from current bonded stake on every call, never
// snapshotted, so thresholds are immune to validator-set drift (unbonding,
// slashing) during a confirmation window. Confirmers that have since
// unbonded are simply not counted, deterministically, by every validator
// re-evaluating this same read at this same height.
//
// Shared by the deposit and name-registration attestation flows so the two
// can never drift on how the threshold is measured.
func (k Keeper) confirmedPowerRatio(ctx context.Context, confirmations []*types.Confirmation) (math.LegacyDec, error) {
	totalBonded, err := k.stakingKeeper.TotalBondedTokens(ctx)
	if err != nil {
		return math.LegacyDec{}, err
	}

	confirmedPower := math.ZeroInt()
	for _, confirmation := range confirmations {
		confirmerAddr, err := sdk.ValAddressFromBech32(confirmation.ValidatorAddress)
		if err != nil {
			return math.LegacyDec{}, err
		}
		confirmer, err := k.stakingKeeper.GetValidator(ctx, confirmerAddr)
		if err != nil || !confirmer.IsBonded() {
			continue
		}
		confirmedPower = confirmedPower.Add(confirmer.GetTokens())
	}

	ratio := math.LegacyZeroDec()
	if totalBonded.IsPositive() {
		ratio = math.LegacyNewDecFromInt(confirmedPower).Quo(math.LegacyNewDecFromInt(totalBonded))
	}
	return ratio, nil
}
