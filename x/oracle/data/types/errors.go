package types

// DONTCOVER

import (
	"cosmossdk.io/errors"
)

// x/oracle/data module sentinel errors
var (
	ErrInvalidSigner      = errors.Register(ModuleName, 1100, "expected gov account as only signer for proposal message")
	ErrNotBondedValidator = errors.Register(ModuleName, 1101, "signer is not a currently bonded validator")
	ErrNoAggregatePrevote = errors.Register(ModuleName, 1102, "no aggregate prevote found for this validator")
	ErrNoAggregateVote    = errors.Register(ModuleName, 1103, "no aggregate vote found for this validator")
	ErrPrevoteNotExpired  = errors.Register(ModuleName, 1104, "prevote was not submitted in a previous vote period")
	ErrHashVerification   = errors.Register(ModuleName, 1105, "revealed vote does not match the committed prevote hash")
	ErrInvalidExchangeRate = errors.Register(ModuleName, 1106, "invalid exchange_rates string")
	ErrNoExchangeRate     = errors.Register(ModuleName, 1107, "no exchange rate for this pair")
	ErrInvalidHash        = errors.Register(ModuleName, 1108, "invalid prevote hash")
)
