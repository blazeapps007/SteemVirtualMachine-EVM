package app

import (
	"context"
	"slices"

	"cosmossdk.io/math"
	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"
	upgradetypes "github.com/cosmos/cosmos-sdk/x/upgrade/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"

	oracledataprecompile "steemvm/precompiles/oracledata"
	oracledatatypes "steemvm/x/oracle/data/types"
	oracletypes "steemvm/x/oracle/types"
)

// UpgradeName is the on-chain name of this software upgrade. It MUST match the
// plan name used in a future governance software-upgrade proposal, and is the
// key x/upgrade uses to look up the handler at the upgrade height. On a chain
// launched fresh at this version the handler never fires (InitChainer + genesis
// do the setup); it is retained so the same migration is available if an older
// chain ever upgrades in-place.
const UpgradeName = "v0.0.3"

// UpgradeNameV004 is the coordinated security-patch upgrade: bumps
// cosmos/evm to v0.7.2 (SubBalance underflow fix — see
// precompiles/steembridge/steembridge.go's doc comment) and cosmos-sdk to
// v0.54.4, plus on-chain enforcement that MsgAttestWithdrawalPayout's
// observed amount/asset actually match the withdrawal record (see
// AttestWithdrawalPayout in x/oracle/bridge/keeper). MUST exactly match both
// the Makefile's VERSION (cosmovisor stages the built binary under
// cosmovisor/upgrades/v$(steemvmd version)/bin/) and the Plan.Name used in
// the governance MsgSoftwareUpgrade proposal that schedules it.
const UpgradeNameV004 = "v0.0.4"

// UpgradeNameV005 closes the remaining routes around the validator identity
// gate and makes the EVM gas floor part of the chain:
//   - authz: MsgCreateValidator/MsgEditValidator can no longer be wrapped in
//     MsgExec or authorized via MsgGrant (ante_steembridge.go's limiter);
//   - ICA host: interchain-account validator create/edit now runs the same
//     identity check (ica_identity_router.go);
//   - EVM: the staking precompile is registered wrapped in the same identity
//     check (staking_identity_precompile.go), so it can be active again;
//   - state: the handler enforces the 1 gwei EVM gas floor and switches the
//     (now gated) staking precompile back on (enforceEVMPolicy). Governance
//     proposal 3 had switched it off on the live chain until this binary.
//
// Same naming contract as UpgradeNameV004: MUST match the Makefile's VERSION
// and the Plan.Name of the governance MsgSoftwareUpgrade.
const UpgradeNameV005 = "v0.0.5"

// RegisterUpgradeHandlers wires the v0.0.3 upgrade handler and store loader. On
// the IN-PLACE upgrade path this: runs module migrations, registers the native
// SBD coin (bank metadata + ERC20 precompile, via the same registerSBD helper the
// genesis/InitChainer path uses), sets distribution community_tax to 0 (the
// 50/25/25 fee split is handled by the steembridge fee-split BeginBlocker), and
// adds the new x/oracle/data module store. On the fresh-genesis path the handler
// is never invoked — InitChainer does the SBD registration and genesis carries
// community_tax=0. Called from New() before app.Load.
func (app *App) RegisterUpgradeHandlers() {
	app.UpgradeKeeper.SetUpgradeHandler(
		UpgradeName,
		func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
			vm, err := app.ModuleManager.RunMigrations(ctx, app.Configurator(), fromVM)
			if err != nil {
				return vm, err
			}

			sdkCtx := sdk.UnwrapSDKContext(ctx)
			if err := app.registerSBD(sdkCtx); err != nil {
				return vm, err
			}

			distrParams, err := app.DistrKeeper.Params.Get(ctx)
			if err != nil {
				return vm, err
			}
			distrParams.CommunityTax = math.LegacyZeroDec()
			if err := app.DistrKeeper.Params.Set(ctx, distrParams); err != nil {
				return vm, err
			}

			// Activate the oracle price-feed precompile (0x...0902) so EVM callers
			// can reach it. SetParams re-sorts the list, so order/idempotency are
			// handled; guard the append so a re-run doesn't duplicate the entry.
			evmParams := app.EVMKeeper.GetParams(sdkCtx)
			if !slices.Contains(evmParams.ActiveStaticPrecompiles, oracledataprecompile.PrecompileAddress) {
				evmParams.ActiveStaticPrecompiles = append(evmParams.ActiveStaticPrecompiles, oracledataprecompile.PrecompileAddress)
				if err := app.EVMKeeper.SetParams(sdkCtx, evmParams); err != nil {
					return vm, err
				}
			}

			return vm, nil
		},
	)

	// v0.0.4: cosmos/evm v0.7.2 + cosmos-sdk v0.54.4 security patches, plus
	// withdrawal-payout asset/amount enforcement. Logic-only — neither diff
	// adds a new store key, so no StoreUpgrades/SetStoreLoader block is
	// needed for this name (unlike v0.0.3 above).
	app.UpgradeKeeper.SetUpgradeHandler(
		UpgradeNameV004,
		func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
			return app.ModuleManager.RunMigrations(ctx, app.Configurator(), fromVM)
		},
	)

	// v0.0.5: validator-identity hardening + EVM gas floor. No new store keys.
	app.UpgradeKeeper.SetUpgradeHandler(
		UpgradeNameV005,
		func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
			vm, err := app.ModuleManager.RunMigrations(ctx, app.Configurator(), fromVM)
			if err != nil {
				return vm, err
			}
			return vm, app.enforceEVMPolicy(sdk.UnwrapSDKContext(ctx))
		},
	)

	// Add the new x/oracle/data store key at the upgrade height (in-place path).
	upgradeInfo, err := app.UpgradeKeeper.ReadUpgradeInfoFromDisk()
	if err != nil {
		panic(err)
	}
	if upgradeInfo.Name == UpgradeName && !app.UpgradeKeeper.IsSkipHeight(upgradeInfo.Height) {
		storeUpgrades := storetypes.StoreUpgrades{
			Added: []string{oracledatatypes.StoreKey, oracletypes.StoreKey},
		}
		app.SetStoreLoader(upgradetypes.UpgradeStoreLoader(upgradeInfo.Height, &storeUpgrades))
	}
}
