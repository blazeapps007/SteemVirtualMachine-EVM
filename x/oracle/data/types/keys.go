package types

import "cosmossdk.io/collections"

const (
	// ModuleName defines the module name.
	ModuleName = "oracledata"

	// StoreKey defines the primary module store key.
	StoreKey = ModuleName

	// GovModuleName duplicates the gov module's name to avoid a dependency with
	// x/gov. It should be synced with the gov module's name if it is ever changed.
	GovModuleName = "gov"
)

// ParamsKey is the prefix to retrieve the module Params.
var ParamsKey = collections.NewPrefix("p_oracledata")

var (
	// ExchangeRateKey maps a pair -> its finalized ExchangeRate.
	ExchangeRateKey = collections.NewPrefix("exchange_rate/")
	// PrevoteKey maps a validator operator address -> its AggregateExchangeRatePrevote.
	PrevoteKey = collections.NewPrefix("prevote/")
	// VoteKey maps a validator operator address -> its AggregateExchangeRateVote.
	VoteKey = collections.NewPrefix("vote/")
)
