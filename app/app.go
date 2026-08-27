package app

import (
	"fmt"

	clienthelpers "cosmossdk.io/client/v2/helpers"
	"cosmossdk.io/core/appmodule"
	"cosmossdk.io/depinject"
	"cosmossdk.io/log/v2"
	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"
	feegrantkeeper "github.com/cosmos/cosmos-sdk/x/feegrant/keeper"
	upgradekeeper "github.com/cosmos/cosmos-sdk/x/upgrade/keeper"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/baseapp/txnrunner"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/server"
	"github.com/cosmos/cosmos-sdk/server/api"
	"github.com/cosmos/cosmos-sdk/server/config"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkmempool "github.com/cosmos/cosmos-sdk/types/mempool"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/x/auth"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	authsims "github.com/cosmos/cosmos-sdk/x/auth/simulation"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	authzkeeper "github.com/cosmos/cosmos-sdk/x/authz/keeper"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	consensuskeeper "github.com/cosmos/cosmos-sdk/x/consensus/keeper"
	distrkeeper "github.com/cosmos/cosmos-sdk/x/distribution/keeper"
	"github.com/cosmos/cosmos-sdk/x/genutil"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"
	govkeeper "github.com/cosmos/cosmos-sdk/x/gov/keeper"
	mintkeeper "github.com/cosmos/cosmos-sdk/x/mint/keeper"
	paramskeeper "github.com/cosmos/cosmos-sdk/x/params/keeper"
	paramstypes "github.com/cosmos/cosmos-sdk/x/params/types"
	slashingkeeper "github.com/cosmos/cosmos-sdk/x/slashing/keeper"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	evmante "github.com/cosmos/evm/ante"
	evmsrvflags "github.com/cosmos/evm/server/flags"
	erc20keeper "github.com/cosmos/evm/x/erc20/keeper"
	feemarketkeeper "github.com/cosmos/evm/x/feemarket/keeper"
	"github.com/cosmos/evm/x/vm"
	evmkeeper "github.com/cosmos/evm/x/vm/keeper"
	vmrunner "github.com/cosmos/evm/x/vm/runner"
	icacontrollerkeeper "github.com/cosmos/ibc-go/v11/modules/apps/27-interchain-accounts/controller/keeper"
	icahostkeeper "github.com/cosmos/ibc-go/v11/modules/apps/27-interchain-accounts/host/keeper"
	ibctransferkeeper "github.com/cosmos/ibc-go/v11/modules/apps/transfer/keeper"
	ibckeeper "github.com/cosmos/ibc-go/v11/modules/core/keeper"
	_ "github.com/ethereum/go-ethereum/eth/tracers/js"
	_ "github.com/ethereum/go-ethereum/eth/tracers/native"
	"github.com/spf13/cast"

	"steemvm/docs"
	steembridgemodulekeeper "steemvm/x/oracle/bridge/keeper"
	oracledatakeeper "steemvm/x/oracle/data/keeper"
	oraclekeeper "steemvm/x/oracle/keeper"
	steemvmmodulekeeper "steemvm/x/steemvm/keeper"
)

const BaseDenomUnit int64 = 18

const (
	// Name is the name of the application.
	Name = "steemvm"
	// AccountAddressPrefix is the prefix for accounts addresses.
	AccountAddressPrefix = "steem"
	// ChainCoinType is the BIP44 coin type of the chain: 60 (Ethereum), so
	// the default HD path m/44'/60'/0'/0/0 matches MetaMask/Ledger-Ethereum —
	// a mnemonic recovered with `steemvmd keys add` (eth_secp256k1 default)
	// controls the same account in MetaMask. Changing this breaks that parity.
	ChainCoinType = 60
)

// DefaultNodeHome default home directories for the application daemon
var DefaultNodeHome string

var (
	_ runtime.AppI            = (*App)(nil)
	_ servertypes.Application = (*App)(nil)
)

// App extends an ABCI application, but with most of its parameters exported.
// They are exported for convenience in creating helper functions, as object
// capabilities aren't needed for testing.
type App struct {
	*runtime.App
	legacyAmino       *codec.LegacyAmino
	appCodec          codec.Codec
	txConfig          client.TxConfig
	interfaceRegistry codectypes.InterfaceRegistry

	// keepers
	// only keepers required by the app are exposed
	// the list of all modules is available in the app_config
	AuthKeeper            authkeeper.AccountKeeper
	BankKeeper            bankkeeper.Keeper
	StakingKeeper         *stakingkeeper.Keeper
	SlashingKeeper        slashingkeeper.Keeper
	MintKeeper            mintkeeper.Keeper
	DistrKeeper           distrkeeper.Keeper
	GovKeeper             *govkeeper.Keeper
	UpgradeKeeper         *upgradekeeper.Keeper
	AuthzKeeper           authzkeeper.Keeper
	ConsensusParamsKeeper consensuskeeper.Keeper
	ParamsKeeper          paramskeeper.Keeper

	// ibc keepers
	IBCKeeper           *ibckeeper.Keeper
	ICAControllerKeeper *icacontrollerkeeper.Keeper
	ICAHostKeeper       *icahostkeeper.Keeper
	TransferKeeper      *ibctransferkeeper.Keeper

	// simulation manager
	sm                 *module.SimulationManager
	SteemvmKeeper      steemvmmodulekeeper.Keeper
	pendingTxListeners []evmante.PendingTxListener
	FeeGrantKeeper     feegrantkeeper.Keeper
	FeeMarketKeeper    feemarketkeeper.Keeper
	EVMKeeper          *evmkeeper.Keeper
	Erc20Keeper        erc20keeper.Keeper

	// AppConfig returns the default app config.
	// EVMMempool is widened to the ExtMempool interface (cosmos/evm v0.7.0):
	// the concrete v0.6 ExperimentalEVMMempool type is gone, replaced by the
	// Krakatoa app-side mempool (*evmmempool.Mempool, wired in
	// configureEVMMempool in evm.go) or any future custom subpool.
	EVMMempool sdkmempool.ExtMempool
	// vmModule is the EVM AppModule value bound once in registerEVMModules
	// and reused after Load() to call HydrateGlobals (see New()) — required
	// so evmCoinInfo is populated before any RPC handler runs on restart.
	vmModule          vm.AppModule
	SteembridgeKeeper steembridgemodulekeeper.Keeper
	OracleDataKeeper  oracledatakeeper.Keeper
	OracleKeeper      oraclekeeper.Keeper
}

func init() {
	var err error
	clienthelpers.EnvPrefix = Name
	DefaultNodeHome, err = clienthelpers.GetNodeHomeDirectory("." + Name)
	if err != nil {
		panic(err)
	}
}

func AppConfig() depinject.Config {
	return depinject.Configs(
		appConfig,
		depinject.Supply(
			// supply custom module basics
			map[string]module.AppModuleBasic{
				genutiltypes.ModuleName: genutil.NewAppModuleBasic(genutiltypes.DefaultMessageValidator),
			},
		),
	)
}

// New returns a reference to an initialized App.
func New(
	logger log.Logger,
	db dbm.DB,
	loadLatest bool,
	appOpts servertypes.AppOptions,
	baseAppOptions ...func(*baseapp.BaseApp),
) *App {
	var (
		app        = &App{}
		appBuilder *runtime.AppBuilder

		// merge the AppConfig and other configuration in one config
		appConfig = depinject.Configs(
			AppConfig(),
			depinject.Supply(
				appOpts, // supply app options
				logger,  // supply logger

				// Supply with IBC keeper getter for the IBC modules with App Wiring.
				// The IBC Keeper cannot be passed because it has not been initiated yet.
				// Passing the getter, the app IBC Keeper will always be accessible.
				// This needs to be removed after IBC supports App Wiring.
				app.GetIBCKeeper,

				// here alternative options can be supplied to the DI container.
				// those options can be used f.e to override the default behavior of some modules.
				// for instance supplying a custom address codec for not using bech32 addresses.
				// read the depinject documentation and depinject module wiring for more information
				// on available options and how to use them.
			), depinject.Provide(ProvideMsgEthereumTxCustomGetSigner),
		)
	)

	var appModules map[string]appmodule.AppModule
	if err := depinject.Inject(appConfig,
		&appBuilder,
		&appModules,
		&app.appCodec,
		&app.legacyAmino,
		&app.txConfig,
		&app.interfaceRegistry,
		&app.AuthKeeper,
		&app.BankKeeper,
		&app.StakingKeeper,
		&app.SlashingKeeper,
		&app.MintKeeper,
		&app.DistrKeeper,
		&app.GovKeeper,
		&app.UpgradeKeeper,
		&app.AuthzKeeper,
		&app.ConsensusParamsKeeper,
		&app.ParamsKeeper,
		&app.SteemvmKeeper, &app.FeeGrantKeeper,
		&app.SteembridgeKeeper,
		&app.OracleDataKeeper,
		&app.OracleKeeper,
	); err != nil {
		panic(err)
	}

	// add to default baseapp options
	// enable optimistic execution
	baseAppOptions = append(baseAppOptions, baseapp.SetOptimisticExecution())

	// build app
	app.App = appBuilder.Build(db, baseAppOptions...)

	// register legacy modules
	if err := app.registerIBCModules(appOpts); err != nil {
		panic(err)
	}

	/****  Module Options ****/

	// create the simulation manager and define the order of the modules for deterministic simulations
	overrideModules := map[string]module.AppModuleSimulation{
		authtypes.ModuleName: auth.NewAppModule(app.appCodec, app.AuthKeeper, authsims.RandomGenesisAccounts, nil),
	}
	if err := app.registerEVMModules(appOpts); err != nil {
		panic(err)
	}

	app.sm = module.NewSimulationManagerFromAppModules(app.ModuleManager.Modules, overrideModules)
	maxGasWanted := cast.ToUint64(appOpts.Get(evmsrvflags.EVMMaxTxGasWanted))
	// setAnteHandler MUST run before configureEVMMempool: the latter calls
	// app.GetAnteHandler() to seed the mempool's tx rechecker closures, and
	// that returns nil (leading to a first-RecheckTx panic) if the ante
	// handler hasn't been set yet.
	app.setAnteHandler(app.txConfig, maxGasWanted)

	if err := app.configureEVMMempool(appOpts, logger); err != nil {
		panic(fmt.Sprintf("failed to configure EVM mempool: %s", err.Error()))
	}

	app.sm.RegisterStoreDecoders()

	// A custom InitChainer sets if extra pre-init-genesis logic is required.
	// This is necessary for manually registered modules that do not support app wiring.
	// Manually set the module version map as shown below.
	// The upgrade module will automatically handle de-duplication of the module version map.
	app.SetInitChainer(func(ctx sdk.Context, req *abci.RequestInitChain) (*abci.ResponseInitChain, error) {
		if err := app.UpgradeKeeper.SetModuleVersionMap(ctx, app.ModuleManager.GetVersionMap()); err != nil {
			return nil, err
		}
		res, err := app.App.InitChainer(ctx, req)
		if err != nil {
			return nil, err
		}
		// Register the native SBD coin (bank metadata + dynamic ERC20 precompile)
		// on a fresh chain. The in-place upgrade path does the same via the
		// v0.0.3 handler, using the shared registerSBD helper so they can't drift.
		if err := app.registerSBD(ctx); err != nil {
			return nil, err
		}
		return res, nil
	})

	app.RegisterUpgradeHandlers()

	if err := app.Load(loadLatest); err != nil {
		panic(err)
	}

	// Hydrate EVM globals (evmCoinInfo etc.) from the KV store on restart
	// (cosmos/evm v0.7.0 #1126). Without this, an RPC call that arrives
	// before the first PreBlock panics on a nil evmCoinInfo. Only meaningful
	// once state has actually been loaded.
	if loadLatest {
		ctx := app.NewContextLegacy(true, cmtproto.Header{
			Height:  app.LastBlockHeight(),
			ChainID: app.ChainID(),
		})
		app.vmModule.HydrateGlobals(ctx)
	}

	// Wire the EVM tx runner (cosmos/evm v0.7.0 #1132): vmrunner.SetRunner
	// installs the baseapp tx runner wrapped with PatchTxResponses, which
	// fixes up log.Index/transactionIndex after execution. Deliberately
	// using the sequential txnrunner.NewDefaultRunner, NOT
	// txnrunner.NewSTMRunner (BlockSTM) — adopting parallel execution is a
	// separate, state-breaking decision this migration does not make.
	vmrunner.SetRunner(app.BaseApp, txnrunner.NewDefaultRunner(app.txConfig.TxDecoder()))

	return app
}

// GetSubspace returns a param subspace for a given module name.
func (app *App) GetSubspace(moduleName string) paramstypes.Subspace {
	subspace, _ := app.ParamsKeeper.GetSubspace(moduleName)
	return subspace
}

// LegacyAmino returns App's amino codec.
func (app *App) LegacyAmino() *codec.LegacyAmino {
	return app.legacyAmino
}

// AppCodec returns App's app codec.
func (app *App) AppCodec() codec.Codec {
	return app.appCodec
}

// InterfaceRegistry returns App's InterfaceRegistry.
func (app *App) InterfaceRegistry() codectypes.InterfaceRegistry {
	return app.interfaceRegistry
}

// TxConfig returns App's TxConfig
func (app *App) TxConfig() client.TxConfig {
	return app.txConfig
}

// GetKey returns the KVStoreKey for the provided store key.
func (app *App) GetKey(storeKey string) *storetypes.KVStoreKey {
	kvStoreKey, ok := app.UnsafeFindStoreKey(storeKey).(*storetypes.KVStoreKey)
	if !ok {
		return nil
	}
	return kvStoreKey
}

// SimulationManager implements the SimulationApp interface
func (app *App) SimulationManager() *module.SimulationManager {
	return app.sm
}

// RegisterAPIRoutes registers all application module routes with the provided
// API server.
func (app *App) RegisterAPIRoutes(apiSvr *api.Server, apiConfig config.APIConfig) {
	app.App.RegisterAPIRoutes(apiSvr, apiConfig)
	// register swagger API in app.go so that other applications can override easily
	if err := server.RegisterSwaggerAPI(apiSvr.ClientCtx, apiSvr.Router, apiConfig.Swagger); err != nil {
		panic(err)
	}

	// register app's OpenAPI routes.
	docs.RegisterOpenAPIService(Name, apiSvr.Router)
}

// GetMaccPerms returns a copy of the module account permissions
//
// NOTE: This is solely to be used for testing purposes.
func GetMaccPerms() map[string][]string {
	dup := make(map[string][]string)
	for _, perms := range moduleAccPerms {
		dup[perms.GetAccount()] = perms.GetPermissions()
	}

	return dup
}

// BlockedAddresses returns all the app's blocked account addresses.
func BlockedAddresses() map[string]bool {
	result := make(map[string]bool)

	if len(blockAccAddrs) > 0 {
		for _, addr := range blockAccAddrs {
			result[addr] = true
		}
	} else {
		for addr := range GetMaccPerms() {
			result[addr] = true
		}
	}

	return result
}

// GetStoreKeysMap returns the []storetypes.StoreKey slice the EVM keeper's
// constructor expects for cross-module store access (cosmos/evm v0.7.0
// evmkeeper.NewKeeper's 4th arg), filtered to only *storetypes.KVStoreKey and
// *storetypes.ObjectStoreKey entries. Filtering matters: cosmos/evm's
// x/vm/store/snapshotmulti.NewStore partitions every key it's handed into
// "is it a *storetypes.KVStoreKey?" vs. "must be Object-store-shaped" — it
// never checks for *storetypes.ObjectStoreKey specifically, so any other
// legacy key type (e.g. x/params' still-unmigrated *storetypes.TransientStoreKey,
// picked up by the unfiltered app.GetStoreKeys()) falls into the wrong branch
// and panics with "store with key ... is not ObjKVStore" on the first
// stateful precompile call of any tx. Nothing in this app (or x/erc20, which
// doesn't use a legacy params Subspace) needs x/params' transient store
// reachable through this EVM cross-store mechanism, so excluding it has no
// functional downside.
func (app *App) GetStoreKeysMap() []storetypes.StoreKey {
	all := app.GetStoreKeys()
	filtered := make([]storetypes.StoreKey, 0, len(all))
	for _, k := range all {
		switch k.(type) {
		case *storetypes.KVStoreKey, *storetypes.ObjectStoreKey:
			filtered = append(filtered, k)
		}
	}
	return filtered
}
