package types

import (
	"fmt"
)

// DefaultGenesis returns the default genesis state.
func DefaultGenesis() *GenesisState {
	return &GenesisState{
		Params:        DefaultParams(),
		ExchangeRates: []ExchangeRate{},
	}
}

// Validate performs basic genesis state validation returning an error upon any
// failure.
func (gs GenesisState) Validate() error {
	seen := make(map[string]bool, len(gs.ExchangeRates))
	for _, elem := range gs.ExchangeRates {
		if elem.Pair == "" {
			return fmt.Errorf("exchange rate has an empty pair")
		}
		if seen[elem.Pair] {
			return fmt.Errorf("duplicated pair in exchange rates: %s", elem.Pair)
		}
		seen[elem.Pair] = true

		if elem.Rate.IsNil() || elem.Rate.IsNegative() {
			return fmt.Errorf("exchange rate for %s must be a non-negative amount", elem.Pair)
		}
	}

	return gs.Params.Validate()
}
