package relayer

import "time"

// Config drives the relayer loop. It is populated by the standalone oracle
// binary from its environment (see cmd oracle/main.go). The relayer is a pure
// library — it no longer reads app.toml or the node's server context.
type Config struct {
	// SteemRPCURL is the Steem-blockchain RPC endpoint to scan (e.g.
	// https://api.steemit.com).
	SteemRPCURL string
	// SteemSymbol is the asset symbol that counts as bridgeable STEEM in
	// transfer amounts. Mainnet Steem uses "STEEM"; the Steem *testnet* denotes
	// the same asset as "TESTS" (and testnet SBD as "TBD", which the bridge
	// ignores). Amounts in any other symbol are skipped. The symbol is
	// relayer-side only — consensus sees just the integer millisteem — so every
	// oracle watching the same network must set the SAME symbol.
	SteemSymbol string
	// SbdSymbol is the asset symbol that counts as bridgeable SBD (mainnet
	// "SBD", defaulted in main.go). Empty disables SBD extraction.
	SbdSymbol string
	// KeyName is the in-memory keyring key that signs attestation transactions —
	// the validator's operator account, imported from the oracle env.
	KeyName string
	// PollInterval is how often the Steem last-irreversible block is checked.
	PollInterval time.Duration
	// MaxBlocksPerPoll caps how many Steem blocks are scanned per cycle when
	// catching up.
	MaxBlocksPerPoll uint64
	// StartBlock is an override for the first Steem block to scan when no
	// relayer state file exists yet. 0 defers to the chain's
	// relayer_start_block param (the shared anchor recorded in genesis),
	// falling back to the last irreversible block at first startup when that
	// param is also 0.
	StartBlock uint64
}

// DefaultConfig returns the relayer's default configuration (Steem mainnet
// symbol, 1-minute poll, 100 blocks/cycle). SteemRPCURL and KeyName must still
// be supplied by the caller.
func DefaultConfig() Config {
	return Config{
		SteemRPCURL:      "",
		SteemSymbol:      "STEEM",
		KeyName:          "",
		PollInterval:     time.Minute,
		MaxBlocksPerPoll: 100,
		StartBlock:       0,
	}
}

// Enabled reports whether the relayer has the minimum configuration to run.
func (c Config) Enabled() bool {
	return c.SteemRPCURL != "" && c.KeyName != ""
}
