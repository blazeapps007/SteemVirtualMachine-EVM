package types

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// StakingKeeper is the subset of the staking keeper the parent oracle engine
// needs to translate a window verdict into a slash + jail. The signatures match
// the SDK staking keeper (v0.53) so the same keeper instance satisfies this.
type StakingKeeper interface {
	// GetValidator looks up a validator by operator address — used to resolve its
	// consensus address and current power for slashing.
	GetValidator(ctx context.Context, addr sdk.ValAddress) (stakingtypes.Validator, error)
	// Slash burns slashFactor of the validator's stake (snapshot at power).
	Slash(ctx context.Context, consAddr sdk.ConsAddress, infractionHeight, power int64, slashFactor math.LegacyDec) (math.Int, error)
	// Jail removes the validator from the active set until it unjails.
	Jail(ctx context.Context, consAddr sdk.ConsAddress) error
}
