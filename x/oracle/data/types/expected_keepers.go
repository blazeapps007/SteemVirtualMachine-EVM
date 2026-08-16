package types

import (
	"context"

	"cosmossdk.io/core/address"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// StakingKeeper defines the expected interface for the Staking module. It
// mirrors the x/steembridge expected keeper so the same sdk staking keeper
// satisfies both. Validator bonded tokens (GetTokens) are used as the vote
// weight for the power-weighted median.
type StakingKeeper interface {
	ConsensusAddressCodec() address.Codec
	ValidatorByConsAddr(context.Context, sdk.ConsAddress) (stakingtypes.ValidatorI, error)
	// GetValidator looks up a validator by operator address; used to check
	// whether a vote/prevote signer is a currently bonded validator and to read
	// its bonded weight.
	GetValidator(ctx context.Context, addr sdk.ValAddress) (stakingtypes.Validator, error)
	// TotalBondedTokens returns the current total bonded stake, the denominator
	// of the vote-threshold ratio.
	TotalBondedTokens(ctx context.Context) (math.Int, error)
}

// OracleKeeper is the parent x/oracle engine's reporting entry point. The
// interface is declared here (not imported from x/oracle) so the price module
// never depends on the parent — the concrete oracle keeper satisfies it and is
// injected. A nil OracleKeeper leaves the module fully functional standalone
// (no participation is reported), so tests and a pre-wiring build still work.
type OracleKeeper interface {
	// RecordPriceParticipation reports this vote period's in-band voters (by
	// validator operator address bytes) so the parent engine can accrue the
	// price duty of its unified slashing counter.
	RecordPriceParticipation(ctx context.Context, voters [][]byte) error
}
