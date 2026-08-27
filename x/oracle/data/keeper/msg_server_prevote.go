package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"steemvm/x/oracle/data/types"
)

// AggregateExchangeRatePrevote records the commit (hash) of a bonded validator's
// upcoming exchange-rate vote, keyed by the validator's operator address, with
// the current block height as the submit block. The hash is revealed later via
// AggregateExchangeRateVote.
func (k msgServer) AggregateExchangeRatePrevote(ctx context.Context, msg *types.MsgAggregateExchangeRatePrevote) (*types.MsgAggregateExchangeRatePrevoteResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if msg.Hash == "" {
		return nil, types.ErrInvalidHash
	}

	validatorAddr, err := k.addressCodec.StringToBytes(msg.Validator)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid validator address")
	}

	validator, err := k.stakingKeeper.GetValidator(ctx, sdk.ValAddress(validatorAddr))
	if err != nil || !validator.IsBonded() {
		return nil, types.ErrNotBondedValidator
	}

	prevote := types.AggregateExchangeRatePrevote{
		Hash:        msg.Hash,
		Voter:       sdk.ValAddress(validatorAddr).String(),
		SubmitBlock: uint64(sdkCtx.BlockHeight()),
	}
	if err := k.Prevote.Set(ctx, validatorAddr, prevote); err != nil {
		return nil, err
	}

	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeAggregatePrevote,
		sdk.NewAttribute(types.AttributeKeyValidator, prevote.Voter),
		sdk.NewAttribute(types.AttributeKeyHash, msg.Hash),
	))

	return &types.MsgAggregateExchangeRatePrevoteResponse{}, nil
}
