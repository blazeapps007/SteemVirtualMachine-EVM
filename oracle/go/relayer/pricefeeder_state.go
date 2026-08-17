package relayer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// PriceFeederStateFileName is the price feeder's persisted commit state,
// relative to <home>/data. Kept in a file separate from StateFileName (the
// Steem scan cursor) so the two duties' failure/persistence domains stay
// independent — a corrupt or missing price-feeder state file never affects
// bridge scanning, and vice versa.
const PriceFeederStateFileName = "price_feeder_state.json"

// LoadFeederState reads the persisted FeederState from dir/PriceFeederStateFileName.
// A missing file returns a zero FeederState and no error (first run, or a
// feeder that has never committed a prevote).
func LoadFeederState(dir string) (FeederState, error) {
	var s FeederState
	bz, err := os.ReadFile(filepath.Join(dir, PriceFeederStateFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return FeederState{}, nil
		}
		return FeederState{}, err
	}
	if err := json.Unmarshal(bz, &s); err != nil {
		return FeederState{}, fmt.Errorf("corrupt price feeder state file: %w", err)
	}
	return s, nil
}

// SaveFeederState atomically persists the feeder state (write temp file,
// rename over) so a crash mid-write can never leave a truncated commit
// behind — losing a pending commit only costs one missed reveal, but a
// truncated/corrupt file would need manual recovery.
func SaveFeederState(dir string, s FeederState) error {
	bz, err := json.Marshal(s)
	if err != nil {
		return err
	}
	target := filepath.Join(dir, PriceFeederStateFileName)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, bz, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}
