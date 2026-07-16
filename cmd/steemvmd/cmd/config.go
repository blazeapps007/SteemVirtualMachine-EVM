package cmd

import (
	cmtcfg "github.com/cometbft/cometbft/config"
	serverconfig "github.com/cosmos/cosmos-sdk/server/config"
	cosmosevmserverconfig "github.com/cosmos/evm/server/config"

	"steemvm/app"
	"steemvm/relayer"
)

// initCometBFTConfig helps to override default CometBFT Config values.
// return cmtcfg.DefaultConfig if no custom configuration is required for the application.
func initCometBFTConfig() *cmtcfg.Config {
	cfg := cmtcfg.DefaultConfig()

	// these values put a higher strain on node memory
	// cfg.P2P.MaxNumInboundPeers = 100
	// cfg.P2P.MaxNumOutboundPeers = 40

	return cfg
}

// initAppConfig helps to override default appConfig template and configs.
// return "", nil if no custom configuration is required for the application.
//
// The generated app.toml carries three extra families of sections:
//   - cosmos/evm's [evm]/[json-rpc]/[tls] (the JSON-RPC server reads
//     evm-chain-id from here — it MUST default to app.EVMChainID or wallets
//     hit "incorrect chain-id; expected 262144"),
//   - the [steem-relayer] section for the in-binary validator relayer.
func initAppConfig() (string, interface{}) {
	type CustomAppConfig struct {
		serverconfig.Config `mapstructure:",squash"`

		EVM     cosmosevmserverconfig.EVMConfig     `mapstructure:"evm"`
		JSONRPC cosmosevmserverconfig.JSONRPCConfig `mapstructure:"json-rpc"`
		TLS     cosmosevmserverconfig.TLSConfig     `mapstructure:"tls"`
		Relayer relayer.Config                      `mapstructure:"steem-relayer"`
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

	evmCfg := cosmosevmserverconfig.DefaultEVMConfig()
	evmCfg.EVMChainID = app.EVMChainID

	customAppConfig := CustomAppConfig{
		Config:  *srvCfg,
		EVM:     *evmCfg,
		JSONRPC: *cosmosevmserverconfig.DefaultJSONRPCConfig(),
		TLS:     *cosmosevmserverconfig.DefaultTLSConfig(),
		Relayer: relayer.DefaultConfig(),
	}

	customAppTemplate := serverconfig.DefaultConfigTemplate +
		cosmosevmserverconfig.DefaultEVMConfigTemplate +
		relayer.DefaultConfigTemplate

	return customAppTemplate, customAppConfig
}
