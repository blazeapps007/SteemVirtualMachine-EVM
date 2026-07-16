package steembridge_test

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	precompile "steemvm/precompiles/steembridge"
	"steemvm/x/steembridge/types"
)

var (
	callerAddr   = common.HexToAddress("0x1111111111111111111111111111111111111111")
	strangerAddr = common.HexToAddress("0x2222222222222222222222222222222222222222")
)

func TestABIAndDispatch(t *testing.T) {
	f := initFixture(t)

	methods := map[string]bool{ // name -> IsTransaction
		"confirmName":             true,
		"bridgeOut":               true,
		"resolveName":             false,
		"namesOf":                 false,
		"awaitingRegistrationIds": false,
	}
	for name, isTx := range methods {
		method, ok := precompile.ABI.Methods[name]
		require.True(t, ok, "method %s must exist in the ABI", name)
		resolved, err := precompile.ABI.MethodById(method.ID)
		require.NoError(t, err)
		require.Equal(t, name, resolved.Name)
		require.Equal(t, isTx, f.precompile.IsTransaction(&method), "IsTransaction(%s)", name)
	}

	// RequiredGas: tx methods cost more than views for same-size calldata,
	// and garbage input costs nothing.
	txGas := f.precompile.RequiredGas(packCall(t, "confirmName", uint64(1)))
	queryGas := f.precompile.RequiredGas(packCall(t, "awaitingRegistrationIds", callerAddr))
	require.Greater(t, txGas, queryGas)
	require.Zero(t, f.precompile.RequiredGas([]byte{0x01}))

	// Unknown method ID errors at dispatch.
	_, err := f.precompile.Execute(f.ctx, f.stateDB, newContract(callerAddr, []byte{0xde, 0xad, 0xbe, 0xef}), false)
	require.Error(t, err)
}

func TestConfirmName_HappyPath(t *testing.T) {
	f := initFixture(t)
	seedAwaitingRegistration(t, f, 0, "alice", callerAddr)

	out, err := f.precompile.Execute(f.ctx, f.stateDB, newContract(callerAddr, packCall(t, "confirmName", uint64(0))), false)
	require.NoError(t, err)

	method := precompile.ABI.Methods["confirmName"]
	unpacked, err := method.Outputs.Unpack(out)
	require.NoError(t, err)
	require.Equal(t, true, unpacked[0])

	// Link is active and owned by the caller.
	record, err := f.keeper.ActiveName.Get(f.ctx, "alice")
	require.NoError(t, err)
	callerBech, err := f.addressCodec.BytesToString(callerAddr.Bytes())
	require.NoError(t, err)
	require.Equal(t, callerBech, record.Address)

	// One EVM log at the precompile address: NameConfirmed(caller indexed).
	require.Len(t, f.stateDB.logs, 1)
	log := f.stateDB.logs[0]
	require.Equal(t, common.HexToAddress(precompile.PrecompileAddress), log.Address)
	require.Equal(t, precompile.ABI.Events["NameConfirmed"].ID, log.Topics[0])
	require.Equal(t, common.BytesToHash(callerAddr.Bytes()), log.Topics[1])

	data, err := precompile.ABI.Events["NameConfirmed"].Inputs.NonIndexed().Unpack(log.Data)
	require.NoError(t, err)
	require.Equal(t, uint64(0), data[0])
	require.Equal(t, "alice", data[1])
}

func TestConfirmName_WrongCallerReverts(t *testing.T) {
	f := initFixture(t)
	seedAwaitingRegistration(t, f, 0, "alice", callerAddr)

	_, err := f.precompile.Execute(f.ctx, f.stateDB, newContract(strangerAddr, packCall(t, "confirmName", uint64(0))), false)
	require.ErrorIs(t, err, types.ErrConfirmerNotDestination)

	// No state change, no log.
	_, err = f.keeper.ActiveName.Get(f.ctx, "alice")
	require.Error(t, err)
	require.Empty(t, f.stateDB.logs)
}

func TestConfirmName_UnknownRegistration(t *testing.T) {
	f := initFixture(t)
	_, err := f.precompile.Execute(f.ctx, f.stateDB, newContract(callerAddr, packCall(t, "confirmName", uint64(42))), false)
	require.ErrorIs(t, err, types.ErrRegistrationNotFound)
}

func TestBridgeOut_HappyPath(t *testing.T) {
	f := initFixture(t)

	// Fund the caller with 5 STEEM worth of asteem.
	callerAcc := sdk.AccAddress(callerAddr.Bytes())
	funded := types.MillisteemToAsteem(5000)
	require.NoError(t, f.bankKeeper.MintCoins(f.ctx, "faucet", sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, funded))))
	require.NoError(t, f.bankKeeper.SendCoinsFromModuleToAccount(f.ctx, "faucet", callerAcc, sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, funded))))

	amount := types.MillisteemToAsteem(1000) // 1 STEEM, a whole millisteem multiple
	out, err := f.precompile.Execute(f.ctx, f.stateDB,
		newContract(callerAddr, packCall(t, "bridgeOut", "bob", amount.BigInt(), "thanks")), false)
	require.NoError(t, err)

	method := precompile.ABI.Methods["bridgeOut"]
	unpacked, err := method.Outputs.Unpack(out)
	require.NoError(t, err)
	require.Equal(t, true, unpacked[0])

	// Caller debited; module holds nothing (burned).
	remaining := f.bankKeeper.SpendableCoins(f.ctx, callerAcc).AmountOf(sdk.DefaultBondDenom)
	require.Equal(t, funded.Sub(amount), remaining)

	// Withdrawal 0 recorded PENDING.
	withdrawal, err := f.keeper.Withdrawal.Get(f.ctx, 0)
	require.NoError(t, err)
	require.Equal(t, types.WithdrawalStatus_WITHDRAWAL_STATUS_PENDING, withdrawal.Status)
	require.Equal(t, "bob", withdrawal.DestinationSteemAccount)
	require.Equal(t, uint64(1000), withdrawal.AmountMillisteem)

	// Log decodes with the peeked withdrawal id.
	require.Len(t, f.stateDB.logs, 1)
	log := f.stateDB.logs[0]
	require.Equal(t, precompile.ABI.Events["BridgeOutRequested"].ID, log.Topics[0])
	require.Equal(t, common.BytesToHash(callerAddr.Bytes()), log.Topics[1])
	data, err := precompile.ABI.Events["BridgeOutRequested"].Inputs.NonIndexed().Unpack(log.Data)
	require.NoError(t, err)
	require.Equal(t, "bob", data[0])
	require.Zero(t, amount.BigInt().Cmp(data[1].(*big.Int)))
	require.Equal(t, "thanks", data[2])
	require.Equal(t, uint64(0), data[3])
}

func TestBridgeOut_Rejections(t *testing.T) {
	f := initFixture(t)

	t.Run("zero amount", func(t *testing.T) {
		_, err := f.precompile.Execute(f.ctx, f.stateDB,
			newContract(callerAddr, packCall(t, "bridgeOut", "bob", big.NewInt(0), "")), false)
		require.ErrorContains(t, err, "amount must be positive")
	})

	t.Run("not a millisteem multiple", func(t *testing.T) {
		_, err := f.precompile.Execute(f.ctx, f.stateDB,
			newContract(callerAddr, packCall(t, "bridgeOut", "bob", big.NewInt(123), "")), false)
		require.ErrorIs(t, err, types.ErrInvalidAmount)
	})

	t.Run("bridge out disabled", func(t *testing.T) {
		params, err := f.keeper.Params.Get(f.ctx)
		require.NoError(t, err)
		params.BridgeOutEnabled = false
		require.NoError(t, f.keeper.Params.Set(f.ctx, params))
		defer func() {
			params.BridgeOutEnabled = true
			require.NoError(t, f.keeper.Params.Set(f.ctx, params))
		}()

		amount := types.MillisteemToAsteem(1)
		_, err = f.precompile.Execute(f.ctx, f.stateDB,
			newContract(callerAddr, packCall(t, "bridgeOut", "bob", amount.BigInt(), "")), false)
		require.ErrorIs(t, err, types.ErrBridgeOutDisabled)
	})
}

func TestResolveName(t *testing.T) {
	f := initFixture(t)
	method := precompile.ABI.Methods["resolveName"]

	t.Run("not found returns zero values without error", func(t *testing.T) {
		out, err := f.precompile.Execute(f.ctx, f.stateDB,
			newContract(callerAddr, packCall(t, "resolveName", "nobody")), true)
		require.NoError(t, err)
		unpacked, err := method.Outputs.Unpack(out)
		require.NoError(t, err)
		require.Equal(t, common.Address{}, unpacked[0])
		require.Equal(t, uint64(0), unpacked[1])
		require.Equal(t, uint64(0), unpacked[2])
	})

	t.Run("found", func(t *testing.T) {
		seedAwaitingRegistration(t, f, 0, "alice", callerAddr)
		_, err := f.precompile.Execute(f.ctx, f.stateDB, newContract(callerAddr, packCall(t, "confirmName", uint64(0))), false)
		require.NoError(t, err)

		out, err := f.precompile.Execute(f.ctx, f.stateDB,
			newContract(strangerAddr, packCall(t, "resolveName", "alice")), true)
		require.NoError(t, err)
		unpacked, err := method.Outputs.Unpack(out)
		require.NoError(t, err)
		require.Equal(t, callerAddr, unpacked[0])
		require.Equal(t, uint64(0), unpacked[1])
	})
}

func TestNamesOfAndAwaitingIds(t *testing.T) {
	f := initFixture(t)

	namesOf := precompile.ABI.Methods["namesOf"]
	awaiting := precompile.ABI.Methods["awaitingRegistrationIds"]

	// Empty results are empty arrays, not errors.
	out, err := f.precompile.Execute(f.ctx, f.stateDB, newContract(callerAddr, packCall(t, "namesOf", callerAddr)), true)
	require.NoError(t, err)
	unpacked, err := namesOf.Outputs.Unpack(out)
	require.NoError(t, err)
	require.Empty(t, unpacked[0].([]string))

	// Two awaiting registrations for caller, one for a stranger.
	seedAwaitingRegistration(t, f, 0, "alice", callerAddr)
	seedAwaitingRegistration(t, f, 1, "bob", callerAddr)
	seedAwaitingRegistration(t, f, 2, "carol", strangerAddr)

	out, err = f.precompile.Execute(f.ctx, f.stateDB, newContract(callerAddr, packCall(t, "awaitingRegistrationIds", callerAddr)), true)
	require.NoError(t, err)
	unpacked, err = awaiting.Outputs.Unpack(out)
	require.NoError(t, err)
	require.ElementsMatch(t, []uint64{0, 1}, unpacked[0].([]uint64))

	// Confirm both; namesOf now returns both names, stranger's excluded.
	for id := uint64(0); id < 2; id++ {
		_, err = f.precompile.Execute(f.ctx, f.stateDB, newContract(callerAddr, packCall(t, "confirmName", id)), false)
		require.NoError(t, err)
	}
	out, err = f.precompile.Execute(f.ctx, f.stateDB, newContract(callerAddr, packCall(t, "namesOf", callerAddr)), true)
	require.NoError(t, err)
	unpacked, err = namesOf.Outputs.Unpack(out)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"alice", "bob"}, unpacked[0].([]string))
}

func TestReadOnlyProtection(t *testing.T) {
	f := initFixture(t)
	seedAwaitingRegistration(t, f, 0, "alice", callerAddr)

	// Tx methods under STATICCALL are rejected before touching state.
	_, err := f.precompile.Execute(f.ctx, f.stateDB, newContract(callerAddr, packCall(t, "confirmName", uint64(0))), true)
	require.ErrorIs(t, err, vm.ErrWriteProtection)

	amount := math.NewInt(1_000_000_000_000_000)
	_, err = f.precompile.Execute(f.ctx, f.stateDB, newContract(callerAddr, packCall(t, "bridgeOut", "bob", amount.BigInt(), "")), true)
	require.ErrorIs(t, err, vm.ErrWriteProtection)

	// Views succeed under STATICCALL (covered in other tests with readonly=true).
}
