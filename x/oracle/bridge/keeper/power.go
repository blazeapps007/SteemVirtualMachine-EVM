package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"steemvm/x/oracle/bridge/types"
)

// payoutCandidateKey encodes a (txid, op-index) payout candidate into the
// string component of the WithdrawalPayoutVote key. op-index is fixed-width
// zero-padded so the encoding is unambiguous (txid is a fixed 40-hex string,
// so a '/' separator already suffices, but padding keeps keys sortable too).
func payoutCandidateKey(txid string, opIndex uint32) string {
	return fmt.Sprintf("%s/%010d", txid, opIndex)
}

// dedupKeyString encodes a (txid, op-index) pair into the first string
// component of a facts-vote key, matching payoutCandidateKey's scheme.
func dedupKeyString(txid string, opIndex uint32) string {
	return fmt.Sprintf("%s/%010d", txid, opIndex)
}

// factsCandidateRatio returns the fraction of current bonded voting power that
// attested a given fact-set (identified by factsHash) for a deposit or
// name-registration dedup key, recomputed live from the recorded votes in the
// supplied vote set. Shared by the deposit and name flows.
func (k Keeper) factsCandidateRatio(
	ctx context.Context,
	votes collections.KeySet[collections.Triple[string, string, []byte]],
	dedupKey, factsHash string,
) (math.LegacyDec, error) {
	totalBonded, err := k.stakingKeeper.TotalValidatorPower(ctx)
	if err != nil {
		return math.LegacyDec{}, err
	}

	confirmedPower := math.ZeroInt()
	rng := collections.NewSuperPrefixedTripleRange[string, string, []byte](dedupKey, factsHash)
	err = votes.Walk(ctx, rng, func(key collections.Triple[string, string, []byte]) (bool, error) {
		confirmer, gerr := k.stakingKeeper.GetValidator(ctx, sdk.ValAddress(key.K3()))
		if gerr != nil || !confirmer.IsBonded() {
			return false, nil
		}
		confirmedPower = confirmedPower.Add(confirmer.GetTokens())
		return false, nil
	})
	if err != nil {
		return math.LegacyDec{}, err
	}

	ratio := math.LegacyZeroDec()
	if totalBonded.IsPositive() {
		ratio = math.LegacyNewDecFromInt(confirmedPower).Quo(math.LegacyNewDecFromInt(totalBonded))
	}
	return ratio, nil
}

// clearFactsVotes removes every (dedupKey, *, *) entry from a facts-vote set.
func (k Keeper) clearFactsVotes(ctx context.Context, votes collections.KeySet[collections.Triple[string, string, []byte]], dedupKey string) error {
	var keys []collections.Triple[string, string, []byte]
	rng := collections.NewPrefixedTripleRange[string, string, []byte](dedupKey)
	if err := votes.Walk(ctx, rng, func(key collections.Triple[string, string, []byte]) (bool, error) {
		keys = append(keys, key)
		return false, nil
	}); err != nil {
		return err
	}
	for _, key := range keys {
		if err := votes.Remove(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

func (k Keeper) clearDepositFactsVotes(ctx context.Context, txid string, opIndex uint32) error {
	return k.clearFactsVotes(ctx, k.DepositFactsVote, dedupKeyString(txid, opIndex))
}

func (k Keeper) clearNameFactsVotes(ctx context.Context, txid string, opIndex uint32) error {
	return k.clearFactsVotes(ctx, k.NameRegistrationFactsVote, dedupKeyString(txid, opIndex))
}

// candidatePayoutRatio returns the fraction of current bonded voting power that
// attested the given payout candidate for a withdrawal, recomputed live from
// the recorded votes (validators that have since unbonded are not counted),
// exactly like confirmedPowerRatio does for a confirmation list.
func (k Keeper) candidatePayoutRatio(ctx context.Context, withdrawalID uint64, candidate string) (math.LegacyDec, error) {
	totalBonded, err := k.stakingKeeper.TotalValidatorPower(ctx)
	if err != nil {
		return math.LegacyDec{}, err
	}

	confirmedPower := math.ZeroInt()
	rng := collections.NewSuperPrefixedTripleRange[uint64, string, []byte](withdrawalID, candidate)
	err = k.WithdrawalPayoutVote.Walk(ctx, rng, func(key collections.Triple[uint64, string, []byte]) (bool, error) {
		confirmer, gerr := k.stakingKeeper.GetValidator(ctx, sdk.ValAddress(key.K3()))
		if gerr != nil || !confirmer.IsBonded() {
			return false, nil
		}
		confirmedPower = confirmedPower.Add(confirmer.GetTokens())
		return false, nil
	})
	if err != nil {
		return math.LegacyDec{}, err
	}

	ratio := math.LegacyZeroDec()
	if totalBonded.IsPositive() {
		ratio = math.LegacyNewDecFromInt(confirmedPower).Quo(math.LegacyNewDecFromInt(totalBonded))
	}
	return ratio, nil
}

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
	totalBonded, err := k.stakingKeeper.TotalValidatorPower(ctx)
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
