package app

import (
	"context"

	upgradetypes "cosmossdk.io/x/upgrade/types"

	"github.com/cosmos/cosmos-sdk/types/module"
)

// UpgradeName is the on-chain name of this software upgrade. It MUST match the
// plan name used in the governance software-upgrade proposal, and is the key
// x/upgrade uses to look up the handler below at the upgrade height.
const UpgradeName = "v0.0.2-Beta1"

// RegisterUpgradeHandlers wires the upgrade handler for UpgradeName. This
// release changes no module state and adds no new store keys (the erc20 IBC
// middleware is a pure app-wiring change), so the handler just runs the
// standard module migrations — a no-op version bump that is future-proof if a
// later module needs a migration. Called from New() before app.Load.
func (app *App) RegisterUpgradeHandlers() {
	app.UpgradeKeeper.SetUpgradeHandler(
		UpgradeName,
		func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
			return app.ModuleManager.RunMigrations(ctx, app.Configurator(), fromVM)
		},
	)
}
