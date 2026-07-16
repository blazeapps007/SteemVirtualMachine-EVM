package app

import (
	"fmt"
	//	"hash/fnv"
	"os"
	"path/filepath"

	"cosmossdk.io/core/appmodule"
	storetypes "cosmossdk.io/store/types"
	"cosmossdk.io/x/tx/signing"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkmempool "github.com/cosmos/cosmos-sdk/types/mempool"
	"github.com/cosmos/cosmos-sdk/types/module"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/spf13/cast"

	evmcryptocodec "github.com/cosmos/evm/crypto/codec"
	"github.com/cosmos/evm/ethereum/eip712"
	evmmempool "github.com/cosmos/evm/mempool"
	precompiletypes "github.com/cosmos/evm/precompiles/types"
	srvflags "github.com/cosmos/evm/server/flags"
	"github.com/cosmos/evm/utils"
	erc20 "github.com/cosmos/evm/x/erc20"
	erc20keeper "github.com/cosmos/evm/x/erc20/keeper"
	erc20types "github.com/cosmos/evm/x/erc20/types"
	"github.com/cosmos/evm/x/feemarket"
	feemarketkeeper "github.com/cosmos/evm/x/feemarket/keeper"
	feemarkettypes "github.com/cosmos/evm/x/feemarket/types"
	"github.com/cosmos/evm/x/vm"
	evmkeeper "github.com/cosmos/evm/x/vm/keeper"
	evmtypes "github.com/cosmos/evm/x/vm/types"
	"github.com/ethereum/go-ethereum/common"

	"github.com/cosmos/cosmos-sdk/codec/legacy"
	"github.com/cosmos/evm/crypto/ethsecp256k1"

	steembridgeprecompile "steemvm/precompiles/steembridge"
	steembridgekeeper "steemvm/x/steembridge/keeper"
)

func init() {
	// manually update the power reduction by replacing micro (u) -> atto (a) evmos
	sdk.DefaultPowerReduction = utils.AttoPowerReduction

	// set default EVM denom
	evmtypes.DefaultEVMDenom = sdk.DefaultBondDenom
	evmtypes.DefaultEVMDisplayDenom = sdk.DefaultBondDenom
	evmtypes.DefaultEVMExtendedDenom = sdk.DefaultBondDenom

	// Teach the SDK's GLOBAL legacy amino codec about eth_secp256k1 keys.
	// Required for tx simulation (`--gas auto`): x/auth's
	// ConsumeTxSizeGasDecorator marshals a mock legacytx.StdSignature carrying
	// the signer's pubkey using legacy.Cdc (x/auth/ante/basic.go). Without this,
	// that MustMarshal panics with "Cannot encode unregistered concrete type
	// ethsecp256k1.PubKey" and EVERY simulated Cosmos tx fails.
	//
	// Only the CONCRETE types are registered here. Do NOT call
	// evmcryptocodec.RegisterCrypto on legacy.Cdc / app.legacyAmino /
	// clientCtx.LegacyAmino: it chains into the SDK's cryptocodec.RegisterCrypto,
	// which re-registers the cryptotypes.PubKey/PrivKey *interfaces* those codecs
	// already have, and amino panics with "TypeInfo already exists for
	// types.PubKey". (evmd gets away with it only because it builds a fresh
	// amino codec.) This lives in init() so it runs exactly once per process —
	// amino panics on duplicate concrete registration too.
	legacy.Cdc.RegisterConcrete(&ethsecp256k1.PubKey{}, ethsecp256k1.PubKeyName, nil)
	legacy.Cdc.RegisterConcrete(&ethsecp256k1.PrivKey{}, ethsecp256k1.PrivKeyName, nil)
}

// registerEVMModules register EVM keepers and non dependency inject modules.
func (app *App) registerEVMModules(appOpts servertypes.AppOptions) error {
	// Register the Ethermint key/extension types (ethsecp256k1 PubKey/PrivKey,
	// EIP-712 Web3 extension) on the app's interface registry. evmd does this
	// in its encoding config; the depinject scaffold has no equivalent hook,
	// and without it Cosmos txs signed by eth_secp256k1 accounts cannot even
	// be decoded.
	// (The legacy-amino half of this registration is done in init() above, on
	// the global legacy.Cdc — see the note there for why it can't happen here.)
	evmcryptocodec.RegisterInterfaces(app.interfaceRegistry)
	eip712.RegisterInterfaces(app.interfaceRegistry)

	// chain config
	chainID := GetEVMChainID(appOpts)

	// set up non depinject support modules store keys
	if err := app.RegisterStores(
		storetypes.NewKVStoreKey(evmtypes.StoreKey),
		storetypes.NewKVStoreKey(feemarkettypes.StoreKey),
		storetypes.NewKVStoreKey(erc20types.StoreKey),
		storetypes.NewTransientStoreKey(evmtypes.TransientKey),
		storetypes.NewTransientStoreKey(feemarkettypes.TransientKey),
	); err != nil {
		return err
	}

	// set up EVM keeper
	tracer := cast.ToString(appOpts.Get(srvflags.EVMTracer))

	app.FeeMarketKeeper = feemarketkeeper.NewKeeper(
		app.appCodec,
		authtypes.NewModuleAddress(govtypes.ModuleName),
		app.GetKey(feemarkettypes.StoreKey),
		app.UnsafeFindStoreKey(feemarkettypes.TransientKey),
	)

	app.EVMKeeper = evmkeeper.NewKeeper(
		app.appCodec,
		app.GetKey(evmtypes.StoreKey),
		app.UnsafeFindStoreKey(evmtypes.TransientKey),
		app.GetStoreKeysMap(),
		authtypes.NewModuleAddress(govtypes.ModuleName),
		app.AuthKeeper,
		app.BankKeeper,
		app.StakingKeeper,
		app.FeeMarketKeeper,
		&app.ConsensusParamsKeeper,
		&app.Erc20Keeper,
		chainID,
		tracer,
	).WithStaticPrecompiles(
		precompiletypes.DefaultStaticPrecompiles(
			*app.StakingKeeper,
			app.DistrKeeper,
			app.BankKeeper,
			&app.Erc20Keeper,
			&app.TransferKeeper,
			app.IBCKeeper.ChannelKeeper,
			*app.GovKeeper,
			app.SlashingKeeper,
			app.appCodec,
		),
	).WithDefaultEvmCoinInfo(evmtypes.EvmCoinInfo{
		Denom:         sdk.DefaultBondDenom,
		ExtendedDenom: sdk.DefaultBondDenom,
		DisplayDenom:  sdk.DefaultBondDenom,
		Decimals:      evmtypes.EighteenDecimals.Uint32(),
	})

	app.Erc20Keeper = erc20keeper.NewKeeper(
		app.GetKey(erc20types.StoreKey),
		app.appCodec,
		authtypes.NewModuleAddress(govtypes.ModuleName),
		app.AuthKeeper,
		app.BankKeeper,
		app.EVMKeeper,
		app.StakingKeeper,
		&app.TransferKeeper,
	)

	// Register the custom steembridge precompile (0x...0900) on top of the
	// defaults. RegisterStaticPrecompile (not a second WithStaticPrecompiles,
	// which panics if called twice) extends the keeper's precompile map; the
	// address must ALSO be listed in the evm module's
	// active_static_precompiles param to be callable. SteembridgeKeeper is
	// already populated: depinject runs before registerEVMModules.
	steembridgePrecompile := steembridgeprecompile.NewPrecompile(
		app.SteembridgeKeeper,
		steembridgekeeper.NewMsgServerImpl(app.SteembridgeKeeper),
		app.BankKeeper,
		app.AuthKeeper.AddressCodec(),
	)
	app.EVMKeeper.RegisterStaticPrecompile(steembridgePrecompile.Address(), steembridgePrecompile)

	// register evm modules
	if err := app.RegisterModules(
		vm.NewAppModule(app.EVMKeeper, app.AuthKeeper, app.BankKeeper, app.AuthKeeper.AddressCodec()),
		feemarket.NewAppModule(app.FeeMarketKeeper),
		erc20.NewAppModule(app.Erc20Keeper, app.AuthKeeper),
	); err != nil {
		return err
	}

	return nil
}

// setEVMMempool sets the EVM priority nonce mempool
// it is required for the ethereum json rpc server to work
func (app *App) setEVMMempool() {
	if evmtypes.GetChainConfig() != nil {
		mempoolConfig := &evmmempool.EVMMempoolConfig{
			AnteHandler:   app.BaseApp.AnteHandler(),
			BlockGasLimit: 100_000_000,
		}

		evmMempool := evmmempool.NewExperimentalEVMMempool(app.CreateQueryContext, app.Logger(), app.EVMKeeper, app.FeeMarketKeeper, app.txConfig, app.clientCtx, mempoolConfig, 4096)
		app.EVMMempool = evmMempool

		app.SetMempool(evmMempool)
		checkTxHandler := evmmempool.NewCheckTxHandler(evmMempool)
		app.SetCheckTxHandler(checkTxHandler)

		abciProposalHandler := baseapp.NewDefaultProposalHandler(evmMempool, app)
		abciProposalHandler.SetSignerExtractionAdapter(evmmempool.NewEthSignerExtractionAdapter(sdkmempool.NewDefaultSignerExtractionAdapter()))
		app.SetPrepareProposal(abciProposalHandler.PrepareProposalHandler())
	}
}

// RegisterPendingTxListener a function that registers a listener for pending transactions.
func (app *App) RegisterPendingTxListener(listener func(common.Hash)) {
	app.pendingTxListeners = append(app.pendingTxListeners, listener)
}

// SetClientCtx a function that sets the client context on the app, required by EVM module implementation.
func (app *App) SetClientCtx(ctx client.Context) {
	app.clientCtx = ctx
}

// GetMempool returns the mempool of the app.
// It is required by the EVM application interface.
func (app *App) GetMempool() sdkmempool.ExtMempool {
	return app.EVMMempool
}

// GetEVMChainID returns the EVM chain ID from the app options.
func GetEVMChainID(appOpts servertypes.AppOptions) uint64 {
	chainID := cast.ToString(appOpts.Get(flags.FlagChainID))
	if chainID == "" {
		// fallback to genesis chain-id
		genesisPathCfg, _ := appOpts.Get("genesis_file").(string)
		if genesisPathCfg == "" {
			genesisPathCfg = filepath.Join("config", "genesis.json")
		}

		reader, err := os.Open(filepath.Join(DefaultNodeHome, genesisPathCfg))
		if err != nil {
			return cosmosChainIDToEVMChainID("ignite")
		}
		defer reader.Close()

		chainID, err = genutiltypes.ParseChainIDFromGenesis(reader)
		if err != nil {
			panic(fmt.Errorf("failed to parse chain-id from genesis file: %w", err))
		}
	}

	return cosmosChainIDToEVMChainID(chainID)
}

// EVMChainID is this chain's fixed EIP-155 chain ID. It is used both by the
// consensus-side chain config (cosmosChainIDToEVMChainID) and as the default
// for app.toml's [evm] evm-chain-id (cmd/steemvmd/cmd/config.go) — the
// JSON-RPC server reads the latter, so the two MUST agree or wallets get
// "incorrect chain-id" errors.
const EVMChainID uint64 = 8163

// cosmosChainIDToEVMChainID converts a Cosmos chain ID to an EVM chain ID.
// This is an opinionated function to simplify chain id management.
// In theory, cosmos chain id and evm chain id are independent and can be managed separately.
//Modify  EVM CHAIN ID //

//	func cosmosChainIDToEVMChainID(chainID string) uint64 {
//		hasher := fnv.New32a()
//		hasher.Write([]byte(chainID))
//		return uint64(hasher.Sum32())
//	}
func cosmosChainIDToEVMChainID(chainID string) uint64 {
	return EVMChainID
}

// RegisterEVM Since the EVM modules don't support dependency injection,
// we need to manually register the modules on the client side.
// This needs to be removed after EVM supports App Wiring.
func RegisterEVM(cdc codec.Codec, interfaceRegistry codectypes.InterfaceRegistry) map[string]appmodule.AppModule {
	// Client-side counterpart of the registration in registerEVMModules: the
	// CLI must be able to (un)marshal eth_secp256k1 keys (keyring records,
	// tx signing) and the EIP-712 extension option. The legacy-amino half is
	// handled once in init() on the global legacy.Cdc — see the note there.
	evmcryptocodec.RegisterInterfaces(interfaceRegistry)
	eip712.RegisterInterfaces(interfaceRegistry)

	modules := map[string]appmodule.AppModule{
		evmtypes.ModuleName:       vm.NewAppModule(nil, authkeeper.AccountKeeper{}, nil, interfaceRegistry.SigningContext().AddressCodec()),
		erc20types.ModuleName:     erc20.NewAppModule(erc20keeper.Keeper{}, authkeeper.AccountKeeper{}),
		feemarkettypes.ModuleName: feemarket.NewAppModule(feemarketkeeper.Keeper{}),
	}

	for _, m := range modules {
		if mr, ok := m.(module.AppModuleBasic); ok {
			mr.RegisterInterfaces(cdc.InterfaceRegistry())
		}
	}

	return modules
}

// ProvideMsgEthereumTxCustomGetSigner provides a custom signer for the MsgEthereumTx message.
func ProvideMsgEthereumTxCustomGetSigner() signing.CustomGetSigner {
	return evmtypes.MsgEthereumTxCustomGetSigner
}
