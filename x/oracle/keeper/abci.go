package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"steemvm/x/oracle/types"
)

// isNotFound reports whether err is a collections "not found".
func isNotFound(err error) bool { return errors.Is(err, collections.ErrNotFound) }

// EndBlock runs once per block. Between slash-window boundaries it does nothing
// (the duty modules keep feeding the tallies); at a boundary it evaluates every
// validator's combined participation and slashes + jails the failures, then
// resets the window.
func (k Keeper) EndBlock(ctx context.Context) error {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	height := sdkCtx.BlockHeight()
	if height <= 0 || params.SlashWindow == 0 || uint64(height)%params.SlashWindow != 0 {
		return nil
	}
	return k.evaluateWindow(ctx, sdkCtx, params, height)
}

// pendingSlash records a validator that failed its window, gathered during the
// tally walk so slashing happens after iteration (never mutating mid-walk).
type pendingSlash struct {
	valoper []byte
	result  types.WindowResult
}

func (k Keeper) evaluateWindow(ctx context.Context, sdkCtx sdk.Context, params types.Params, height int64) error {
	var toSlash []pendingSlash

	err := k.Tally.Walk(ctx, nil, func(valoper []byte, tally types.ValidatorTally) (bool, error) {
		result := types.EvaluateValidatorWindow(
			types.DutyTally{Opportunities: tally.PriceOpportunities, Hits: tally.PriceHits},
			types.DutyTally{Opportunities: tally.BridgeOpportunities, Hits: tally.BridgeHits},
			params.WPrice, params.WBridge, params.MinValidPerWindow, params.DutyFloor,
		)
		if result.Judged && result.ShouldSlash {
			// Copy the key: the walk reuses the backing slice between iterations.
			key := make([]byte, len(valoper))
			copy(key, valoper)
			toSlash = append(toSlash, pendingSlash{valoper: key, result: result})
		}
		return false, nil
	})
	if err != nil {
		return err
	}

	for _, ps := range toSlash {
		k.slashAndJail(sdkCtx, params, height, ps)
	}

	// Reset the window: a fresh tally accrues from the next block.
	return k.Tally.Clear(ctx, nil)
}

// slashAndJail applies the window verdict to one validator. Per-validator
// failures are logged and swallowed (never returned) so one problematic
// validator can't halt the chain from EndBlock; store-level failures in the
// caller still propagate.
func (k Keeper) slashAndJail(sdkCtx sdk.Context, params types.Params, height int64, ps pendingSlash) {
	valoperStr, _ := k.addressCodec.BytesToString(ps.valoper)

	validator, err := k.stakingKeeper.GetValidator(sdkCtx, sdk.ValAddress(ps.valoper))
	if err != nil {
		// Validator was removed between accrual and evaluation — nothing to slash.
		return
	}
	consAddr, err := validator.GetConsAddr()
	if err != nil {
		k.Logger(sdkCtx).Error("oracle slash: cannot resolve consensus address", "validator", valoperStr, "err", err)
		return
	}

	power := validator.GetConsensusPower(sdk.DefaultPowerReduction)
	if _, err := k.stakingKeeper.Slash(sdkCtx, sdk.ConsAddress(consAddr), height, power, params.SlashFraction); err != nil {
		k.Logger(sdkCtx).Error("oracle slash failed", "validator", valoperStr, "err", err)
		return
	}
	if err := k.stakingKeeper.Jail(sdkCtx, sdk.ConsAddress(consAddr)); err != nil {
		k.Logger(sdkCtx).Error("oracle jail failed", "validator", valoperStr, "err", err)
		// Slash already applied; fall through to emit the event.
	}

	k.Logger(sdkCtx).Info("oracle slashed validator",
		"validator", valoperStr, "reason", ps.result.Reason, "score", ps.result.Score.String())
	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeOracleSlash,
		sdk.NewAttribute(types.AttributeKeyValidator, valoperStr),
		sdk.NewAttribute(types.AttributeKeyReason, ps.result.Reason),
		sdk.NewAttribute(types.AttributeKeyScore, ps.result.Score.String()),
	))
}
