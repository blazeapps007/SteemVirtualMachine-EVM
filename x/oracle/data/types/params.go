package types

import (
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	errortypes "github.com/cosmos/cosmos-sdk/types/errors"
)

// DefaultVotePeriod is the default number of blocks per tally window (~1h at 6s).
var DefaultVotePeriod uint64 = 600

// DefaultVoteThreshold is the default minimum fraction of bonded power that must
// vote for a pair before its median is accepted.
var DefaultVoteThreshold math.LegacyDec = math.LegacyMustNewDecFromStr("0.50")

// DefaultRewardBand is the default ± band around the median considered accurate.
var DefaultRewardBand math.LegacyDec = math.LegacyMustNewDecFromStr("0.02")

// DefaultMissBand is the default ± band outside which a vote is a price-duty miss.
var DefaultMissBand math.LegacyDec = math.LegacyMustNewDecFromStr("0.03")

// DefaultWhitelist is the default set of voted pairs. STEEM/USD_External,
// STEEM/SBD_Internal, and SBD/USD_External are market pairs (external CMC
// price or Steem's own internal market, per the _External/_Internal suffix);
// Price_Feed is deliberately NOT pair-shaped — it's Steem's own witness-median
// feed price (via get_feed_history), a single blockchain-native value, not a
// tradeable market rate. See oracle/PROTOCOL.md §7 for the full mapping.
var DefaultWhitelist = []string{"STEEM/USD_External", "STEEM/SBD_Internal", "SBD/USD_External", "Price_Feed"}

// NewParams creates a new Params instance.
func NewParams(
	votePeriod uint64,
	voteThreshold math.LegacyDec,
	rewardBand math.LegacyDec,
	missBand math.LegacyDec,
	whitelist []string,
) Params {
	return Params{
		VotePeriod:    votePeriod,
		VoteThreshold: voteThreshold,
		RewardBand:    rewardBand,
		MissBand:      missBand,
		Whitelist:     whitelist,
	}
}

// DefaultParams returns a default set of parameters.
func DefaultParams() Params {
	return NewParams(
		DefaultVotePeriod,
		DefaultVoteThreshold,
		DefaultRewardBand,
		DefaultMissBand,
		DefaultWhitelist,
	)
}

// Validate validates the set of params.
func (p Params) Validate() error {
	if err := validateVotePeriod(p.VotePeriod); err != nil {
		return err
	}
	if err := validateFraction("vote_threshold", p.VoteThreshold, true); err != nil {
		return err
	}
	if err := validateFraction("reward_band", p.RewardBand, false); err != nil {
		return err
	}
	if err := validateFraction("miss_band", p.MissBand, false); err != nil {
		return err
	}
	if err := validateWhitelist(p.Whitelist); err != nil {
		return err
	}
	// A reward inside the miss band is nonsensical: the reward band must be no
	// wider than the (slashable) miss band.
	if !p.RewardBand.IsNil() && !p.MissBand.IsNil() && p.RewardBand.GT(p.MissBand) {
		return errorsmod.Wrap(errortypes.ErrInvalidRequest, "reward_band cannot exceed miss_band")
	}
	return nil
}

// validateVotePeriod validates the VotePeriod parameter.
func validateVotePeriod(v uint64) error {
	if v == 0 {
		return errorsmod.Wrap(errortypes.ErrInvalidRequest, "vote_period must be positive")
	}
	return nil
}

// validateFraction validates a LegacyDec that must lie in [0,1]. When
// mustBePositive is set, a zero value is rejected (e.g. vote_threshold).
func validateFraction(name string, v math.LegacyDec, mustBePositive bool) error {
	if v.IsNil() {
		return errorsmod.Wrapf(errortypes.ErrInvalidRequest, "%s cannot be nil", name)
	}
	if v.IsNegative() {
		return errorsmod.Wrapf(errortypes.ErrInvalidRequest, "%s cannot be negative", name)
	}
	if mustBePositive && v.IsZero() {
		return errorsmod.Wrapf(errortypes.ErrInvalidRequest, "%s must be positive", name)
	}
	if v.GT(math.LegacyOneDec()) {
		return errorsmod.Wrapf(errortypes.ErrInvalidRequest, "%s cannot exceed 1", name)
	}
	return nil
}

// validateWhitelist validates the Whitelist parameter.
func validateWhitelist(whitelist []string) error {
	if len(whitelist) == 0 {
		return errorsmod.Wrap(errortypes.ErrInvalidRequest, "whitelist cannot be empty")
	}
	seen := make(map[string]bool, len(whitelist))
	for _, pair := range whitelist {
		if pair == "" {
			return errorsmod.Wrap(errortypes.ErrInvalidRequest, "whitelist pair cannot be empty")
		}
		if seen[pair] {
			return errorsmod.Wrapf(errortypes.ErrInvalidRequest, "duplicate whitelist pair %q", pair)
		}
		seen[pair] = true
	}
	return nil
}
