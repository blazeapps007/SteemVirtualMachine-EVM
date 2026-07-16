package types

import (
	"context"

	"cosmossdk.io/core/address"
	"cosmossdk.io/math"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// StakingKeeper defines the expected interface for the Staking module.
type StakingKeeper interface {
	ConsensusAddressCodec() address.Codec
	ValidatorByConsAddr(context.Context, sdk.ConsAddress) (stakingtypes.ValidatorI, error)
	// GetValidator looks up a validator by operator address; used to check
	// whether a MsgSubmitSteemDeposit signer is a currently bonded validator.
	GetValidator(ctx context.Context, addr sdk.ValAddress) (stakingtypes.Validator, error)
	// TotalBondedTokens returns the current total bonded stake, used as the
	// denominator of the confirmation voting-power ratio.
	TotalBondedTokens(ctx context.Context) (math.Int, error)
}

// AuthKeeper defines the expected interface for the Auth module.
type AuthKeeper interface {
	AddressCodec() address.Codec
	GetAccount(context.Context, sdk.AccAddress) sdk.AccountI // only used for simulation
	// Methods imported from account should be defined here
}

// BankKeeper defines the expected interface for the Bank module.
type BankKeeper interface {
	SpendableCoins(context.Context, sdk.AccAddress) sdk.Coins
	// MintCoins mints deposit amounts into the module account.
	MintCoins(ctx context.Context, moduleName string, amt sdk.Coins) error
	// BurnCoins burns bridge-out amounts from the module account.
	BurnCoins(ctx context.Context, moduleName string, amt sdk.Coins) error
	// SendCoinsFromAccountToModule collects bridge-out burns from the sender.
	SendCoinsFromAccountToModule(ctx context.Context, senderAddr sdk.AccAddress, recipientModule string, amt sdk.Coins) error
	// SendCoinsFromModuleToAccount pays out minted deposits to the derived destination.
	SendCoinsFromModuleToAccount(ctx context.Context, senderModule string, recipientAddr sdk.AccAddress, amt sdk.Coins) error
}

// ParamSubspace defines the expected Subspace interface for parameters.
type ParamSubspace interface {
	Get(context.Context, []byte, interface{})
	Set(context.Context, []byte, interface{})
}
