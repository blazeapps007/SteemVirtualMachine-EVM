package app

import (
	"cosmossdk.io/core/appmodule"
	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/runtime"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	erc20 "github.com/cosmos/evm/x/erc20"
	erc20v2 "github.com/cosmos/evm/x/erc20/v2"
	icamodule "github.com/cosmos/ibc-go/v11/modules/apps/27-interchain-accounts"
	icacontroller "github.com/cosmos/ibc-go/v11/modules/apps/27-interchain-accounts/controller"
	icacontrollerkeeper "github.com/cosmos/ibc-go/v11/modules/apps/27-interchain-accounts/controller/keeper"
	icacontrollertypes "github.com/cosmos/ibc-go/v11/modules/apps/27-interchain-accounts/controller/types"
	icahost "github.com/cosmos/ibc-go/v11/modules/apps/27-interchain-accounts/host"
	icahostkeeper "github.com/cosmos/ibc-go/v11/modules/apps/27-interchain-accounts/host/keeper"
	icahosttypes "github.com/cosmos/ibc-go/v11/modules/apps/27-interchain-accounts/host/types"
	icatypes "github.com/cosmos/ibc-go/v11/modules/apps/27-interchain-accounts/types"
	ibctransfer "github.com/cosmos/ibc-go/v11/modules/apps/transfer"
	ibctransferkeeper "github.com/cosmos/ibc-go/v11/modules/apps/transfer/keeper"
	ibctransfertypes "github.com/cosmos/ibc-go/v11/modules/apps/transfer/types"
	ibctransferv2 "github.com/cosmos/ibc-go/v11/modules/apps/transfer/v2"
	ibc "github.com/cosmos/ibc-go/v11/modules/core"
	ibcclienttypes "github.com/cosmos/ibc-go/v11/modules/core/02-client/types"
	porttypes "github.com/cosmos/ibc-go/v11/modules/core/05-port/types"
	ibcapi "github.com/cosmos/ibc-go/v11/modules/core/api"
	ibcexported "github.com/cosmos/ibc-go/v11/modules/core/exported"
	ibckeeper "github.com/cosmos/ibc-go/v11/modules/core/keeper"
	solomachine "github.com/cosmos/ibc-go/v11/modules/light-clients/06-solomachine"
	ibctm "github.com/cosmos/ibc-go/v11/modules/light-clients/07-tendermint"
)

// registerIBCModules register IBC keepers and non dependency inject modules.
func (app *App) registerIBCModules(appOpts servertypes.AppOptions) error {
	// set up non depinject support modules store keys
	if err := app.RegisterStores(
		storetypes.NewKVStoreKey(ibcexported.StoreKey),
		storetypes.NewKVStoreKey(ibctransfertypes.StoreKey),
		storetypes.NewKVStoreKey(icahosttypes.StoreKey),
		storetypes.NewKVStoreKey(icacontrollertypes.StoreKey),
	); err != nil {
		return err
	}

	// NOTE: ibc-go v11 removed legacy x/params subspace support entirely —
	// ibcclienttypes.ParamKeyTable, ibctransfertypes.ParamKeyTable,
	// icacontrollertypes.ParamKeyTable, and icahosttypes.ParamKeyTable no
	// longer exist, and none of the keeper constructors below take a
	// legacy subspace argument any more. The v0.6-era key-table
	// registration block and every app.GetSubspace(...) call that fed these
	// constructors have been removed accordingly (this is a fresh-chain
	// launch, so there is no legacy param state to migrate).
	govModuleAddr, _ := app.AuthKeeper.AddressCodec().BytesToString(authtypes.NewModuleAddress(govtypes.ModuleName))

	// Create IBC keeper. ibc-go v11 dropped the capability-keeper argument
	// (already nil pre-migration, nothing to migrate) from the signature.
	app.IBCKeeper = ibckeeper.NewKeeper(
		app.appCodec,
		runtime.NewKVStoreService(app.GetKey(ibcexported.StoreKey)),
		app.UpgradeKeeper,
		govModuleAddr,
	)

	// Create IBC transfer keeper. ibc-go v11 returns *transferkeeper.Keeper
	// (App.TransferKeeper is now a pointer field — see app.go), takes the
	// address codec inline (no more SetAddressCodec), and drops the
	// duplicate ICS4Wrapper/ChannelKeeper param (the single channelKeeper
	// param below doubles as the default ICS4Wrapper).
	app.TransferKeeper = ibctransferkeeper.NewKeeper(
		app.appCodec,
		app.AuthKeeper.AddressCodec(),
		runtime.NewKVStoreService(app.GetKey(ibctransfertypes.StoreKey)),
		app.IBCKeeper.ChannelKeeper,
		app.MsgServiceRouter(),
		app.AuthKeeper,
		app.BankKeeper,
		govModuleAddr,
	)

	// Create interchain account keepers. Both now return pointers and drop
	// the legacy subspace + duplicate ICS4Wrapper/channelKeeper params, same
	// shape as the transfer keeper above.
	app.ICAHostKeeper = icahostkeeper.NewKeeper(
		app.appCodec,
		runtime.NewKVStoreService(app.GetKey(icahosttypes.StoreKey)),
		app.IBCKeeper.ChannelKeeper,
		app.AuthKeeper,
		app.MsgServiceRouter(),
		app.GRPCQueryRouter(),
		govModuleAddr,
	)

	app.ICAControllerKeeper = icacontrollerkeeper.NewKeeper(
		app.appCodec,
		runtime.NewKVStoreService(app.GetKey(icacontrollertypes.StoreKey)),
		app.IBCKeeper.ChannelKeeper,
		app.MsgServiceRouter(),
		govModuleAddr,
	)

	// create IBC module from bottom to top of stack.
	//
	// The transfer stacks are wrapped by the erc20 middleware so that incoming
	// IBC vouchers are auto-registered as ERC20 (dynamic precompile) and
	// converted on receipt. &app.Erc20Keeper is deliberate: this runs before
	// registerEVMModules creates the erc20 keeper, and the pointer-to-field
	// sees that later assignment; the middleware is only invoked at packet
	// time, long after both are wired. (v2's constructor takes args reversed.)
	// erc20.NewIBCMiddleware / erc20v2.NewIBCMiddleware remain single-step
	// constructors in cosmos/evm v0.7.0 — no SetICS4Wrapper/
	// SetUnderlyingApplication split, unlike the (unused here) ibc-go
	// callbacks middleware. app.ICAControllerKeeper/app.ICAHostKeeper are
	// already pointer-typed fields now, so no leading & is needed.
	var (
		transferStack      porttypes.IBCModule = erc20.NewIBCMiddleware(&app.Erc20Keeper, ibctransfer.NewIBCModule(app.TransferKeeper))
		transferStackV2    ibcapi.IBCModule    = erc20v2.NewIBCMiddleware(ibctransferv2.NewIBCModule(app.TransferKeeper), &app.Erc20Keeper)
		icaControllerStack porttypes.IBCModule = icacontroller.NewIBCMiddleware(app.ICAControllerKeeper)
		icaHostStack       porttypes.IBCModule = icahost.NewIBCModule(app.ICAHostKeeper)
	)

	// create IBC v1 router, add transfer route, then set it on the keeper
	ibcRouter := porttypes.NewRouter().
		AddRoute(ibctransfertypes.ModuleName, transferStack).
		AddRoute(icacontrollertypes.SubModuleName, icaControllerStack).
		AddRoute(icahosttypes.SubModuleName, icaHostStack)

	// create IBC v2 router, add transfer route, then set it on the keeper
	ibcv2Router := ibcapi.NewRouter().
		AddRoute(ibctransfertypes.PortID, transferStackV2)

	app.IBCKeeper.SetRouter(ibcRouter)
	app.IBCKeeper.SetRouterV2(ibcv2Router)

	clientKeeper := app.IBCKeeper.ClientKeeper
	storeProvider := clientKeeper.GetStoreProvider()

	tmLightClientModule := ibctm.NewLightClientModule(app.appCodec, storeProvider)
	clientKeeper.AddRoute(ibctm.ModuleName, &tmLightClientModule)

	soloLightClientModule := solomachine.NewLightClientModule(app.appCodec, storeProvider)
	clientKeeper.AddRoute(solomachine.ModuleName, &soloLightClientModule)

	// register IBC modules
	if err := app.RegisterModules(
		ibc.NewAppModule(app.IBCKeeper),
		ibctransfer.NewAppModule(app.TransferKeeper),
		icamodule.NewAppModule(app.ICAControllerKeeper, app.ICAHostKeeper),
		ibctm.NewAppModule(tmLightClientModule),
		solomachine.NewAppModule(soloLightClientModule),
	); err != nil {
		return err
	}

	return nil
}

// RegisterIBC Since the IBC modules don't support dependency injection,
// we need to manually register the modules on the client side.
// This needs to be removed after IBC supports App Wiring.
func RegisterIBC(cdc codec.Codec) map[string]appmodule.AppModule {
	modules := map[string]appmodule.AppModule{
		ibcexported.ModuleName:      ibc.NewAppModule(&ibckeeper.Keeper{}),
		ibctransfertypes.ModuleName: ibctransfer.NewAppModule(&ibctransferkeeper.Keeper{}),
		icatypes.ModuleName:         icamodule.NewAppModule(&icacontrollerkeeper.Keeper{}, &icahostkeeper.Keeper{}),
		ibctm.ModuleName:            ibctm.NewAppModule(ibctm.NewLightClientModule(cdc, ibcclienttypes.StoreProvider{})),
		solomachine.ModuleName:      solomachine.NewAppModule(solomachine.NewLightClientModule(cdc, ibcclienttypes.StoreProvider{})),
	}

	for _, m := range modules {
		if mr, ok := m.(module.AppModuleBasic); ok {
			mr.RegisterInterfaces(cdc.InterfaceRegistry())
		}
	}

	return modules
}

// GetIBCKeeper returns the IBC keeper.
// Used for supply with IBC keeper getter for the IBC modules with App Wiring.
func (app *App) GetIBCKeeper() *ibckeeper.Keeper {
	return app.IBCKeeper
}
