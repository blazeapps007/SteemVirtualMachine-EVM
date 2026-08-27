package cmd

import (
	cmtcfg "github.com/cometbft/cometbft/config"
	serverconfig "github.com/cosmos/cosmos-sdk/server/config"
	cosmosevmserverconfig "github.com/cosmos/evm/server/config"

	"steemvm/app"
)

// initCometBFTConfig helps to override default CometBFT Config values.
// return cmtcfg.DefaultConfig if no custom configuration is required for the application.
func initCometBFTConfig() *cmtcfg.Config {
	cfg := cmtcfg.DefaultConfig()

	// these values put a higher strain on node memory
	// cfg.P2P.MaxNumInboundPeers = 100
	// cfg.P2P.MaxNumOutboundPeers = 40

	// cosmos/evm's EVM mempool takes over tx handling from CometBFT's default
	// flood-gossip mempool; boot refuses to start ("EVM mempool enabled, but
	// comet-bft has invalid config.toml:mempool.type") unless this is "app".
	cfg.Mempool.Type = cmtcfg.MempoolTypeApp

	return cfg
}

// initAppConfig helps to override default appConfig template and configs.
// return "", nil if no custom configuration is required for the application.
//
// The generated app.toml carries cosmos/evm's [evm]/[json-rpc]/[tls] sections
// (the JSON-RPC server reads evm-chain-id from here — it MUST default to
// app.EVMChainID or wallets hit "incorrect chain-id; expected 262144"). The
// Steem relayer no longer lives in this binary; it is a standalone oracle (see
// the oracle/ package) configured entirely from its own environment.
func initAppConfig() (string, interface{}) {
	type CustomAppConfig struct {
		serverconfig.Config `mapstructure:",squash"`

		EVM     cosmosevmserverconfig.EVMConfig     `mapstructure:"evm"`
		JSONRPC cosmosevmserverconfig.JSONRPCConfig `mapstructure:"json-rpc"`
		TLS     cosmosevmserverconfig.TLSConfig     `mapstructure:"tls"`
	}

	// Optionally allow the chain developer to overwrite the SDK's default
	// server config.
	srvCfg := serverconfig.DefaultConfig()
	// The SDK's default minimum gas price is set to "" (empty value) inside
	// app.toml. If left empty by validators, the node will halt on startup.
	// However, the chain developer can set a default app.toml value for their
	// validators here.
	//
	// In summary:
	// - if you leave srvCfg.MinGasPrices = "", all validators MUST tweak their
	//   own app.toml config,
	// - if you set srvCfg.MinGasPrices non-empty, validators CAN tweak their
	//   own app.toml to override, or use this default value.
	//
	// In tests, we set the min gas prices to 0.
	// srvCfg.MinGasPrices = "0stake"
	//
	// 1e9 asteem/gas matches both the EVM feemarket's starting base_fee (see
	// app_state.feemarket.params.base_fee in genesis) and the --gas-prices
	// value already documented for non-fee-exempt oracledata txs
	// (Instructions/ORACLE_COMMANDS.md) -- so this floor never binds tighter
	// than what those txs already pay, it just stops `steemvmd start` from
	// refusing to boot when a validator's app.toml leaves MinGasPrices unset.
	srvCfg.MinGasPrices = "1000000000asteem"

	evmCfg := cosmosevmserverconfig.DefaultEVMConfig()
	evmCfg.EVMChainID = app.EVMChainID

	customAppConfig := CustomAppConfig{
		Config:  *srvCfg,
		EVM:     *evmCfg,
		JSONRPC: *cosmosevmserverconfig.DefaultJSONRPCConfig(),
		TLS:     *cosmosevmserverconfig.DefaultTLSConfig(),
	}

	customAppTemplate := serverconfig.DefaultConfigTemplate +
		cosmosevmserverconfig.DefaultEVMConfigTemplate

	return customAppTemplate, customAppConfig
}
