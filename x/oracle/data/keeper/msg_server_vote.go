package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"steemvm/x/oracle/data/types"
)

// AggregateExchangeRateVote reveals a bonded validator's exchange-rate vote.
// It verifies the reveal against the validator's committed prevote: the prevote
// must exist, must have been submitted in a previous vote period (commit-reveal
// separation), and its hash must equal the hash recomputed from the revealed
// salt, exchange_rates, and validator. On success the parsed vote is stored and
// the consumed prevote is removed.
func (k msgServer) AggregateExchangeRateVote(ctx context.Context, msg *types.MsgAggregateExchangeRateVote) (*types.MsgAggregateExchangeRateVoteResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	params, err := k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}

	validatorAddr, err := k.addressCodec.StringToBytes(msg.Validator)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid validator address")
	}

	validator, err := k.stakingKeeper.GetValidator(ctx, sdk.ValAddress(validatorAddr))
	if err != nil || !validator.IsBonded() {
		return nil, types.ErrNotBondedValidator
	}

	prevote, err := k.Prevote.Get(ctx, validatorAddr)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return nil, types.ErrNoAggregatePrevote
		}
		return nil, err
	}

	// The prevote must come from a strictly earlier vote period than the current
	// one, so the commit could not have been crafted with knowledge of this
	// period's revealed votes.
	height := uint64(sdkCtx.BlockHeight())
	currentPeriodStart := height
	if params.VotePeriod > 0 {
		currentPeriodStart = height - (height % params.VotePeriod)
	}
	if prevote.SubmitBlock >= currentPeriodStart {
		return nil, types.ErrPrevoteNotExpired
	}

	// Recompute and compare the commit hash (commit-reveal verification).
	expectedHash := types.GetAggregateVoteHash(msg.Salt, msg.ExchangeRates, msg.Validator)
	if expectedHash != prevote.Hash {
		return nil, errorsmod.Wrapf(types.ErrHashVerification, "expected %s, got %s", prevote.Hash, expectedHash)
	}

	tuples, err := types.ParseExchangeRateTuples(msg.ExchangeRates)
	if err != nil {
		return nil, err
	}

	vote := types.AggregateExchangeRateVote{
		ExchangeRates: tuples,
		Voter:         sdk.ValAddress(validatorAddr).String(),
	}
	if err := k.Vote.Set(ctx, validatorAddr, vote); err != nil {
		return nil, err
	}

	// The prevote has been consumed by this reveal.
	if err := k.Prevote.Remove(ctx, validatorAddr); err != nil {
		return nil, err
	}

	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeAggregateVote,
		sdk.NewAttribute(types.AttributeKeyValidator, vote.Voter),
		sdk.NewAttribute(types.AttributeKeyExchangeRate, msg.ExchangeRates),
	))

	return &types.MsgAggregateExchangeRateVoteResponse{}, nil
}
