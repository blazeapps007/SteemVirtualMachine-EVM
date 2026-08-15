package types

import "cosmossdk.io/collections"

const (
	// ModuleName defines the parent oracle module name.
	ModuleName = "oracle"

	// StoreKey defines the primary module store key.
	StoreKey = ModuleName

	// GovModuleName duplicates the gov module's name to avoid a dependency with
	// x/gov. It should be synced with the gov module's name if it is ever changed.
	GovModuleName = "gov"
)

// ParamsKey is the prefix to retrieve the module Params.
var ParamsKey = collections.NewPrefix("p_oracle")

// TallyKey maps a validator operator address (bytes) -> its ValidatorTally for
// the current slash window.
var TallyKey = collections.NewPrefix("tally/")
