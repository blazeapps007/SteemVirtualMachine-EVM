package keeper

import (
	"context"
	"sort"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"steemvm/x/oracle/data/types"
)

// EndBlocker runs the exchange-rate tally once per vote period. It is a thin
// gate around TallyExchangeRates; the parent x/oracle module may call
// TallyExchangeRates itself to read per-validator participation, but for the
// standalone data module the tally is driven from here.
func (k Keeper) EndBlocker(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	if params.VotePeriod == 0 {
		return nil
	}
	if uint64(sdkCtx.BlockHeight())%params.VotePeriod != 0 {
		return nil
	}

	inBandVoters, err := k.TallyExchangeRates(ctx)
	if err != nil {
		return err
	}
	// Feed the unified slashing counter: this period's in-band voters are the
	// price-duty hits; the parent engine supplies the bonded opportunity set.
	if k.oracleKeeper != nil {
		return k.oracleKeeper.RecordPriceParticipation(ctx, inBandVoters)
	}
	return nil
}

// voterBallot holds a single validator's revealed vote, resolved to its live
// bonded weight, ready for the per-pair tally.
type voterBallot struct {
	valStr  string
	valoper []byte
	power   math.Int
	rates   map[string]math.LegacyDec
}

// TallyExchangeRates computes, for every whitelisted pair, the power-weighted
// median of the current revealed votes, and — when the votes for that pair
// represent at least VoteThreshold of total bonded power — writes it to the
// ExchangeRate store with the current epoch and block time.
//
// It returns the in-band voters (validator operator address bytes): those that
// submitted a revealed vote this period whose rate stayed within RewardBand of
// the finalized median for every pair that finalized. This is the price-duty
// HIT set the parent x/oracle slashing engine reads; storing it is not this
// module's job, so the result is returned (and the ExchangeRates are set as a
// side effect). Revealed votes are cleared after the tally, and stale prevotes
// that were never revealed are expired.
//
// The weight of a vote is the validator's live bonded tokens (GetTokens),
// matching the x/steembridge attestation-power convention.
func (k Keeper) TallyExchangeRates(ctx context.Context) ([][]byte, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	params, err := k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}

	totalBonded, err := k.stakingKeeper.TotalBondedTokens(ctx)
	if err != nil {
		return nil, err
	}

	// Gather all revealed votes from currently bonded validators.
	var ballots []voterBallot
	err = k.Vote.Walk(ctx, nil, func(key []byte, vote types.AggregateExchangeRateVote) (bool, error) {
		valAddr := sdk.ValAddress(key)
		validator, gerr := k.stakingKeeper.GetValidator(ctx, valAddr)
		if gerr != nil || !validator.IsBonded() {
			// Votes from validators that have since unbonded are ignored, matching
			// the live-power convention: they carry no weight and are not scored.
			return false, nil
		}
		rates := make(map[string]math.LegacyDec, len(vote.ExchangeRates))
		for _, t := range vote.ExchangeRates {
			rates[t.Pair] = t.Rate
		}
		// Copy the key: the iterator reuses the buffer between iterations.
		valoper := make([]byte, len(key))
		copy(valoper, key)
		ballots = append(ballots, voterBallot{
			valStr:  valAddr.String(),
			valoper: valoper,
			power:   validator.GetTokens(),
			rates:   rates,
		})
		return false, nil
	})
	if err != nil {
		return nil, err
	}

	// Every validator that voted starts eligible for the in-band reward; a
	// missing or out-of-band rate on any finalized pair drops it.
	inBand := make(map[string]bool, len(ballots))
	for _, b := range ballots {
		inBand[b.valStr] = true
	}

	// Minimum voted power (as a Dec) required to finalize a pair.
	requiredPower := math.LegacyZeroDec()
	if totalBonded.IsPositive() {
		requiredPower = params.VoteThreshold.MulInt(totalBonded)
	}

	height := uint64(sdkCtx.BlockHeight())
	epoch := height
	if params.VotePeriod > 0 {
		epoch = height / params.VotePeriod
	}
	blockTime := sdkCtx.BlockTime().Unix()

	for _, pair := range params.Whitelist {
		var pairBallots []rateWeight
		votedPower := math.ZeroInt()
		for _, b := range ballots {
			rate, ok := b.rates[pair]
			if !ok || rate.IsNil() || !rate.IsPositive() {
				continue
			}
			pairBallots = append(pairBallots, rateWeight{rate: rate, power: b.power})
			votedPower = votedPower.Add(b.power)
		}

		// Not enough participation for this pair: leave the previous rate intact
		// and do not penalize non-voters on an unfinalized pair.
		if len(pairBallots) == 0 || math.LegacyNewDecFromInt(votedPower).LT(requiredPower) {
			continue
		}

		sort.Slice(pairBallots, func(i, j int) bool {
			return pairBallots[i].rate.LT(pairBallots[j].rate)
		})
		median := weightedMedian(pairBallots, votedPower)

		if err := k.ExchangeRate.Set(ctx, pair, types.ExchangeRate{
			Pair:        pair,
			Rate:        median,
			UpdateEpoch: epoch,
			UpdateTime:  blockTime,
		}); err != nil {
			return nil, err
		}

		// Reward band around the median: [median*(1-band), median*(1+band)].
		spread := median.Mul(params.RewardBand)
		lower := median.Sub(spread)
		upper := median.Add(spread)
		for _, b := range ballots {
			rate, ok := b.rates[pair]
			if !ok || rate.LT(lower) || rate.GT(upper) {
				inBand[b.valStr] = false
			}
		}

		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
			types.EventTypeExchangeRateUpdate,
			sdk.NewAttribute(types.AttributeKeyPair, pair),
			sdk.NewAttribute(types.AttributeKeyExchangeRate, median.String()),
		))
	}

	if err := k.clearVotes(ctx); err != nil {
		return nil, err
	}
	if err := k.expireStalePrevotes(ctx, height, params.VotePeriod); err != nil {
		return nil, err
	}

	// Collapse the in-band map into the operator-address hit set (in ballot
	// order, deterministic) for the parent engine.
	inBandVoters := make([][]byte, 0, len(ballots))
	for _, b := range ballots {
		if inBand[b.valStr] {
			inBandVoters = append(inBandVoters, b.valoper)
		}
	}
	return inBandVoters, nil
}

// rateWeight is a single (rate, bonded power) ballot for one pair.
type rateWeight struct {
	rate  math.LegacyDec
	power math.Int
}

// weightedMedian returns the rate at which the cumulative bonded power of the
// ballots — which MUST be sorted ascending by rate — first reaches half of
// totalPower (2*cumulative >= totalPower). ballots is guaranteed non-empty by
// the caller (a finalized pair always has at least one ballot).
func weightedMedian(ballots []rateWeight, totalPower math.Int) math.LegacyDec {
	median := ballots[len(ballots)-1].rate
	cumulative := math.ZeroInt()
	for _, b := range ballots {
		cumulative = cumulative.Add(b.power)
		if cumulative.MulRaw(2).GTE(totalPower) {
			median = b.rate
			break
		}
	}
	return median
}

// clearVotes removes every revealed vote after a tally.
func (k Keeper) clearVotes(ctx context.Context) error {
	var keys [][]byte
	if err := k.Vote.Walk(ctx, nil, func(key []byte, _ types.AggregateExchangeRateVote) (bool, error) {
		// Copy: the iterator reuses the key buffer.
		k2 := make([]byte, len(key))
		copy(k2, key)
		keys = append(keys, k2)
		return false, nil
	}); err != nil {
		return err
	}
	for _, key := range keys {
		if err := k.Vote.Remove(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

// expireStalePrevotes removes prevotes older than one full vote period that were
// never revealed, so abandoned commits do not accumulate.
func (k Keeper) expireStalePrevotes(ctx context.Context, height, votePeriod uint64) error {
	if votePeriod == 0 || height < votePeriod {
		return nil
	}
	cutoff := height - votePeriod
	var keys [][]byte
	if err := k.Prevote.Walk(ctx, nil, func(key []byte, prevote types.AggregateExchangeRatePrevote) (bool, error) {
		if prevote.SubmitBlock < cutoff {
			k2 := make([]byte, len(key))
			copy(k2, key)
			keys = append(keys, k2)
		}
		return false, nil
	}); err != nil {
		return err
	}
	for _, key := range keys {
		if err := k.Prevote.Remove(ctx, key); err != nil {
			return err
		}
	}
	return nil
}
