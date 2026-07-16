package relayer

import (
	"time"

	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	"github.com/spf13/cast"
)

// Config is the [steem-relayer] section of app.toml. The relayer is disabled
// unless both SteemRPCURL and KeyName are set.
type Config struct {
	// SteemRPCURL is the Steem-blockchain RPC endpoint to scan (e.g.
	// https://api.steemit.com). Empty disables the relayer.
	SteemRPCURL string `mapstructure:"steem-rpc-url"`
	// KeyName is the keyring key that signs attestation transactions — the
	// validator's operator account key. Empty disables the relayer.
	KeyName string `mapstructure:"key-name"`
	// PollInterval is how often the Steem last-irreversible block is checked.
	PollInterval time.Duration `mapstructure:"poll-interval"`
	// MaxBlocksPerPoll caps how many Steem blocks are scanned per cycle when
	// catching up.
	MaxBlocksPerPoll uint64 `mapstructure:"max-blocks-per-poll"`
	// StartBlock is a per-node override for the first Steem block to scan
	// when no relayer state file exists yet. 0 (the normal setting) defers
	// to the chain's relayer_start_block param — the shared anchor recorded
	// in genesis — falling back to the last irreversible block at first
	// startup when that param is also 0.
	StartBlock uint64 `mapstructure:"start-block"`
}

// DefaultConfig returns the relayer's default (disabled) configuration.
func DefaultConfig() Config {
	return Config{
		SteemRPCURL:      "",
		KeyName:          "",
		PollInterval:     3 * time.Second,
		MaxBlocksPerPoll: 100,
		StartBlock:       0,
	}
}

// Enabled reports whether the relayer has the minimum configuration to run.
func (c Config) Enabled() bool {
	return c.SteemRPCURL != "" && c.KeyName != ""
}

// ReadFromAppOpts extracts the relayer config from the server's app options
// (app.toml + flags), falling back to defaults for unset fields.
func ReadFromAppOpts(appOpts servertypes.AppOptions) Config {
	cfg := DefaultConfig()
	cfg.SteemRPCURL = cast.ToString(appOpts.Get("steem-relayer.steem-rpc-url"))
	cfg.KeyName = cast.ToString(appOpts.Get("steem-relayer.key-name"))
	if v := cast.ToDuration(appOpts.Get("steem-relayer.poll-interval")); v > 0 {
		cfg.PollInterval = v
	}
	if v := cast.ToUint64(appOpts.Get("steem-relayer.max-blocks-per-poll")); v > 0 {
		cfg.MaxBlocksPerPoll = v
	}
	cfg.StartBlock = cast.ToUint64(appOpts.Get("steem-relayer.start-block"))
	return cfg
}

// DefaultConfigTemplate is appended to the app.toml template.
const DefaultConfigTemplate = `

###############################################################################
###                           Steem Relayer                                 ###
###############################################################################

# The steem-relayer watches the Steem blockchain and, when this node's
# configured key belongs to a bonded validator, automatically attests bridge
# deposits (memo: "svm-deposit <address>" or a bare address) and name
# registrations (memo: "svm-register <address>") sent to the on-chain gateway
# account. Attestations are fee-exempt for bonded validators.

[steem-relayer]

# Steem RPC endpoint to scan, e.g. "https://api.steemit.com". Empty disables the relayer.
steem-rpc-url = "{{ .Relayer.SteemRPCURL }}"

# Keyring key name that signs attestations (the validator operator account).
key-name = "{{ .Relayer.KeyName }}"

# How often to poll the Steem last-irreversible block.
poll-interval = "{{ .Relayer.PollInterval }}"

# Maximum Steem blocks scanned per poll cycle while catching up.
max-blocks-per-poll = {{ .Relayer.MaxBlocksPerPoll }}

# Per-node override for the first Steem block to scan when no relayer state
# exists. Leave 0 to use the chain's relayer_start_block param (the shared
# anchor set in genesis), or Steem's current last-irreversible block if that
# param is also 0.
start-block = {{ .Relayer.StartBlock }}
`
