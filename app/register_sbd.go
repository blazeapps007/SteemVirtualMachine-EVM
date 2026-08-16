package app

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	erc20types "github.com/cosmos/evm/x/erc20/types"
	"github.com/ethereum/go-ethereum/common"

	steembridgetypes "steemvm/x/oracle/bridge/types"
)

// SBDERC20Address is the fixed EVM address of the SBD dynamic ERC20 precompile.
// It must not collide with a static precompile (…0100/0400/0800-0806/0900/0902).
var SBDERC20Address = common.HexToAddress("0x0000000000000000000000000000000000000901")

// registerSBD registers the native asbd (SBD) coin: bank denom metadata plus a
// dynamic ERC20 precompile at SBDERC20Address, so SBD is usable in the EVM with
// balances mirroring the native bank balance. Driven by both the genesis path
// (InitChainer) and the v0.0.3 upgrade handler via this single helper so they
// can't drift. Idempotent: a no-op if the pair is already registered.
func (app *App) registerSBD(ctx sdk.Context) error {
	app.BankKeeper.SetDenomMetaData(ctx, banktypes.Metadata{
		Description: "Bridged SBD (Steem Backed Dollars) on SteemVM",
		DenomUnits: []*banktypes.DenomUnit{
			{Denom: steembridgetypes.DenomSBD, Exponent: 0, Aliases: []string{"attosbd"}},
			{Denom: "sbd", Exponent: 18},
		},
		Base:    steembridgetypes.DenomSBD,
		Display: "sbd",
		Name:    "SBD",
		Symbol:  "SBD",
	})

	// If already registered (e.g. re-run), do nothing.
	if app.Erc20Keeper.IsDenomRegistered(ctx, steembridgetypes.DenomSBD) {
		return nil
	}

	pair := erc20types.NewTokenPair(SBDERC20Address, steembridgetypes.DenomSBD, erc20types.OWNER_MODULE)
	if err := app.Erc20Keeper.SetToken(ctx, pair); err != nil {
		return err
	}
	return app.Erc20Keeper.EnableDynamicPrecompile(ctx, pair.GetERC20Contract())
}
