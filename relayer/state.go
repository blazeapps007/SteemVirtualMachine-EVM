package relayer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// State is the relayer's persisted scan cursor. It lives outside consensus
// state (a plain file under the node home's data directory): each validator
// tracks its own scanning progress independently.
type State struct {
	// LastScannedBlock is the last Steem block fully processed and broadcast.
	LastScannedBlock uint64 `json:"last_scanned_block"`
}

// StateFileName is the relayer state file, relative to <home>/data.
const StateFileName = "steem_relayer_state.json"

// LoadState reads the relayer state from dir/StateFileName. A missing file
// returns a zero State and no error (first run).
func LoadState(dir string) (State, error) {
	var s State
	bz, err := os.ReadFile(filepath.Join(dir, StateFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return State{}, err
	}
	if err := json.Unmarshal(bz, &s); err != nil {
		return State{}, fmt.Errorf("corrupt relayer state file: %w", err)
	}
	return s, nil
}

// SaveState atomically persists the state (write temp file, rename over) so
// a crash mid-write can never leave a truncated cursor behind.
func SaveState(dir string, s State) error {
	bz, err := json.Marshal(s)
	if err != nil {
		return err
	}
	target := filepath.Join(dir, StateFileName)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, bz, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}
