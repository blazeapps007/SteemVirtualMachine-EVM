package app

import (
	"testing"

	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/stretchr/testify/require"

	bridgetypes "steemvm/x/oracle/bridge/types"
)

// Every module account must be blocked in x/bank. cosmos/evm mirrors a
// precompile's bank transfers into the EVM state except for blocked
// addresses, and refuses to write a module account's balance at commit — so
// bridgeOut through the 0x…0900 precompile (paying steemblackhole and
// bridge_reward) failed "is not allowed to receive funds" while those two
// accounts were missing from a hand-written override list.
func TestEveryModuleAccountIsBlocked(t *testing.T) {
	app := realTestApp()

	for _, name := range []string{
		bridgetypes.BlackHoleModuleName,
		bridgetypes.BridgeRewardModuleName,
		bridgetypes.ModuleName,
		govtypes.ModuleName,
		authtypes.FeeCollectorName,
	} {
		require.True(t, app.BankKeeper.BlockedAddr(authtypes.NewModuleAddress(name)), "module account %q must be blocked", name)
	}
	for name := range GetMaccPerms() {
		require.True(t, app.BankKeeper.BlockedAddr(authtypes.NewModuleAddress(name)), "module account %q must be blocked", name)
		require.True(t, BlockedAddresses()[authtypes.NewModuleAddress(name).String()], "BlockedAddresses() must list %q", name)
	}
}
