package app

import (
	"fmt"
	//	"hash/fnv"
	"os"
	"path/filepath"

	"cosmossdk.io/core/appmodule"
	"cosmossdk.io/log/v2"
	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"
	"github.com/cosmos/cosmos-sdk/x/tx/signing"
	"github.com/cosmos/cosmos-sdk/baseapp"
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
	evmserver "github.com/cosmos/evm/server"
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

	oracledataprecompile "steemvm/precompiles/oracledata"
	steembridgeprecompile "steemvm/precompiles/steembridge"
	steembridgekeeper "steemvm/x/oracle/bridge/keeper"
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

	// set up non depinject support modules store keys. The transient stores
	// evmtypes.TransientKey/feemarkettypes.TransientKey are gone as of
	// cosmos/evm v0.7.0 (cosmos-sdk v0.54 store/v2); the EVM keeper's per-tx
	// scratch store (tx bloom, gas accounting) is now an object store
	// (evmtypes.ObjectKey) instead. RegisterStores forwards to baseapp's
	// generic MountStores, which type-switches on *storetypes.ObjectStoreKey,
	// so the object key can be registered in the same call as the KV keys.
	evmObjectKey := storetypes.NewObjectStoreKey(evmtypes.ObjectKey)
	if err := app.RegisterStores(
		storetypes.NewKVStoreKey(evmtypes.StoreKey),
		storetypes.NewKVStoreKey(feemarkettypes.StoreKey),
		storetypes.NewKVStoreKey(erc20types.StoreKey),
		evmObjectKey,
	); err != nil {
		return err
	}

	// set up EVM keeper
	tracer := cast.ToString(appOpts.Get(srvflags.EVMTracer))

	app.FeeMarketKeeper = feemarketkeeper.NewKeeper(
		app.appCodec,
		authtypes.NewModuleAddress(govtypes.ModuleName),
		app.GetKey(feemarkettypes.StoreKey),
	)

	app.EVMKeeper = evmkeeper.NewKeeper(
		app.appCodec,
		app.GetKey(evmtypes.StoreKey),
		evmObjectKey,
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
			app.TransferKeeper,
			app.IBCKeeper.ChannelKeeper,
			app.IBCKeeper.ClientKeeper,
			*app.GovKeeper,
			app.SlashingKeeper,
			app.appCodec,
		),
	)

	// NOTE: virtual fee collection (EnableVirtualFeeCollection) is deliberately
	// NOT wired here. It is part of the BlockSTM parallel-execution bundle
	// (cosmos/evm v0.7.0 migration guide, Step 5b) and this migration
	// intentionally keeps sequential execution (see setEVMTxRunner in this
	// file) — do not enable it without also adopting BlockSTM as a coordinated,
	// separate decision.

	app.Erc20Keeper = erc20keeper.NewKeeper(
		app.GetKey(erc20types.StoreKey),
		app.appCodec,
		authtypes.NewModuleAddress(govtypes.ModuleName),
		app.AuthKeeper,
		app.BankKeeper,
		app.EVMKeeper,
		app.StakingKeeper,
		app.TransferKeeper,
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

	// Register the read-only oracle price-feed precompile (0x...0902). Like the
	// steembridge one, its address must ALSO be listed in the evm module's
	// active_static_precompiles param to be callable. OracleDataKeeper is already
	// populated: depinject runs before registerEVMModules.
	oracledataPrecompile := oracledataprecompile.NewPrecompile(app.OracleDataKeeper)
	app.EVMKeeper.RegisterStaticPrecompile(oracledataPrecompile.Address(), oracledataPrecompile)

	// register evm modules. vmModule is bound to a field (not passed inline)
	// so New() in app.go can call HydrateGlobals on it after Load() — see the
	// field doc comment on App.vmModule.
	app.vmModule = vm.NewAppModule(app.EVMKeeper, app.AuthKeeper, app.BankKeeper, app.AuthKeeper.AddressCodec())
	if err := app.RegisterModules(
		app.vmModule,
		feemarket.NewAppModule(app.FeeMarketKeeper),
		erc20.NewAppModule(app.Erc20Keeper, app.AuthKeeper),
	); err != nil {
		return err
	}

	return nil
}

// configureEVMMempool sets up the EVM application-layer mempool ("Krakatoa").
// cosmos/evm v0.7.0 removed the v0.6 ExperimentalEVMMempool type entirely —
// Krakatoa is its mandatory replacement for any fork (like this one) that
// already wired an app-side EVM mempool; forks that ran on CometBFT's stock
// mempool could skip this, but that's not our starting point. Mirrors
// cosmos/evm's reference evmd/mempool.go configureEVMMempool almost exactly.
//
// IMPORTANT: app.setAnteHandler(...) MUST run before this is called —
// server.ResolveMempoolConfig reads app.GetAnteHandler() and stashes it for
// the rechecker closures; if the ante handler isn't set yet this panics on
// the first RecheckTx with no compile-time signal. app.go's New() already
// calls setAnteHandler before setEVMMempool, preserve that order.
func (app *App) configureEVMMempool(appOpts servertypes.AppOptions, logger log.Logger) error {
	if evmtypes.GetChainConfig() == nil {
		logger.Debug("evm chain config is not set, skipping mempool configuration")
		return nil
	}

	var (
		mpConfig = evmserver.ResolveMempoolConfig(app.AnteHandler(), appOpts, logger)

		txEncoder       = evmmempool.NewTxEncoder(app.txConfig)
		evmRechecker    = evmmempool.NewTxRechecker(mpConfig.AnteHandler, txEncoder)
		cosmosRechecker = evmmempool.NewTxRechecker(mpConfig.AnteHandler, txEncoder)
		cosmosPoolMaxTx = evmserver.GetCosmosPoolMaxTx(appOpts, logger)
		checkTxTimeout  = evmserver.GetMempoolCheckTxTimeout(appOpts, logger)
	)

	if cosmosPoolMaxTx < 0 {
		logger.Debug("evm mempool is disabled, skipping configuration")
		return nil
	}

	if err := evmserver.ValidateReapBounds(appOpts, mpConfig.BlockGasLimit); err != nil {
		return err
	}

	mempool := evmmempool.NewMempool(
		app.CreateQueryContext,
		logger,
		app.EVMKeeper,
		app.FeeMarketKeeper,
		app.txConfig,
		evmRechecker,
		cosmosRechecker,
		mpConfig,
		cosmosPoolMaxTx,
	)

	app.EVMMempool = mempool

	prepareProposalHandler := baseapp.
		NewDefaultProposalHandler(mempool, NewNoCheckProposalTxVerifier(app.BaseApp)).
		PrepareProposalHandler()

	insertTxHandler := mempool.NewInsertTxHandler(app.TxDecode)
	reapTxsHandler := mempool.NewReapTxsHandler()
	checkTxHandler := mempool.NewCheckTxHandler(app.TxDecode, checkTxTimeout)

	app.SetPrepareProposal(prepareProposalHandler)
	app.SetInsertTxHandler(insertTxHandler)
	app.SetReapTxsHandler(reapTxsHandler)
	app.SetCheckTxHandler(checkTxHandler)

	app.SetMempool(mempool)

	app.SetPrepareCheckStater(func(_ sdk.Context) {
		if !mempool.HasEventBus() {
			mempool.NotifyNewBlock()
		}
	})

	return nil
}

// RegisterPendingTxListener a function that registers a listener for pending transactions.
func (app *App) RegisterPendingTxListener(listener func(common.Hash)) {
	app.pendingTxListeners = append(app.pendingTxListeners, listener)
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
