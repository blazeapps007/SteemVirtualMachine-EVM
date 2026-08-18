package types_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"steemvm/x/oracle/bridge/types"
)

func nameGenesisFixture() *types.GenesisState {
	addr := sdk.AccAddress(make([]byte, 20)).String()
	gs := types.DefaultGenesis()
	gs.NameRegistrationCount = 2
	gs.NameRegistrationList = []types.NameRegistration{
		{
			Id: 0, Txid: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", OpIndex: 0,
			SteemAccount: "alice", DerivedDestination: addr,
			Status: types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_ACTIVE,
		},
		{
			Id: 1, Txid: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", OpIndex: 0,
			SteemAccount: "bob",
			Status:       types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_PENDING,
		},
	}
	gs.ActiveNameList = []types.NameRecord{
		{SteemAccount: "alice", Address: addr, RegistrationId: 0, LinkedAt: 1},
	}
	return gs
}

func TestGenesisState_ValidateNameService(t *testing.T) {
	require.NoError(t, nameGenesisFixture().Validate())

	t.Run("duplicate registration id rejected", func(t *testing.T) {
		gs := nameGenesisFixture()
		gs.NameRegistrationList[1].Id = 0
		gs.NameRegistrationList[1].Txid = "cccccccccccccccccccccccccccccccccccccccc"
		require.Error(t, gs.Validate())
	})

	t.Run("id beyond count rejected", func(t *testing.T) {
		gs := nameGenesisFixture()
		gs.NameRegistrationList[1].Id = 7
		require.Error(t, gs.Validate())
	})

	t.Run("duplicate dedup key rejected", func(t *testing.T) {
		gs := nameGenesisFixture()
		gs.NameRegistrationList[1].Txid = gs.NameRegistrationList[0].Txid
		require.Error(t, gs.Validate())
	})

	t.Run("duplicate validator confirmation rejected", func(t *testing.T) {
		gs := nameGenesisFixture()
		val := sdk.ValAddress(make([]byte, 20)).String()
		gs.NameRegistrationList[1].ValidatorConfirmations = []*types.Confirmation{
			{ValidatorAddress: val}, {ValidatorAddress: val},
		}
		require.Error(t, gs.Validate())
	})

	t.Run("awaiting without parseable destination rejected", func(t *testing.T) {
		gs := nameGenesisFixture()
		gs.NameRegistrationList[1].Status = types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_AWAITING_CONFIRMATION
		gs.NameRegistrationList[1].DerivedDestination = "garbage"
		require.Error(t, gs.Validate())
	})

	t.Run("duplicate active name rejected", func(t *testing.T) {
		gs := nameGenesisFixture()
		gs.ActiveNameList = append(gs.ActiveNameList, gs.ActiveNameList[0])
		require.Error(t, gs.Validate())
	})

	t.Run("active name referencing missing registration rejected", func(t *testing.T) {
		gs := nameGenesisFixture()
		gs.ActiveNameList[0].RegistrationId = 5
		require.Error(t, gs.Validate())
	})

	t.Run("active name referencing non-ACTIVE registration rejected", func(t *testing.T) {
		gs := nameGenesisFixture()
		gs.NameRegistrationList[0].Status = types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_AWAITING_CONFIRMATION
		require.Error(t, gs.Validate())
	})

	t.Run("active name with mismatched account rejected", func(t *testing.T) {
		gs := nameGenesisFixture()
		gs.NameRegistrationList[0].SteemAccount = "someone-else"
		require.Error(t, gs.Validate())
	})

	t.Run("active name with mismatched address rejected", func(t *testing.T) {
		gs := nameGenesisFixture()
		other := make([]byte, 20)
		other[0] = 9
		gs.ActiveNameList[0].Address = sdk.AccAddress(other).String()
		require.Error(t, gs.Validate())
	})
}

func TestParams_ValidateNameService(t *testing.T) {
	// GatewayAccount is a vestigial, unused-by-consensus param field (the
	// gateway account is the hardcoded types.GatewayAccount constant) — its
	// value, including empty, must never affect Validate().
	t.Run("gateway account param value never affects validation", func(t *testing.T) {
		p := types.DefaultParams()
		p.NameServiceEnabled = true
		p.GatewayAccount = ""
		require.NoError(t, p.Validate())

		p.GatewayAccount = "anything"
		require.NoError(t, p.Validate())
	})

	t.Run("name service enabled requires positive timeout", func(t *testing.T) {
		p := types.DefaultParams()
		p.NameServiceEnabled = true
		p.NamePendingTimeoutBlocks = 0
		require.Error(t, p.Validate())
	})

	t.Run("zero minimum rejected", func(t *testing.T) {
		p := types.DefaultParams()
		p.NameRegistrationMinMillisteem = 0
		require.Error(t, p.Validate())
	})

	t.Run("bridge disabled with name service enabled is valid", func(t *testing.T) {
		p := types.DefaultParams()
		p.BridgeEnabled = false
		p.NameServiceEnabled = true
		require.NoError(t, p.Validate())
	})
}
