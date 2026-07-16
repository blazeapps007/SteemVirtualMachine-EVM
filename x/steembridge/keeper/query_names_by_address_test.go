package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"steemvm/x/steembridge/keeper"
	"steemvm/x/steembridge/types"
)

// TestNamesByAddress_AcceptsBech32AndHex verifies the address argument is
// auto-detected: the bech32 (steem1...) and the equivalent 0x EVM hex form of
// the same account return identical results, and garbage is rejected.
func TestNamesByAddress_AcceptsBech32AndHex(t *testing.T) {
	f := initFixtureWithFakes(t)
	qs := keeper.NewQueryServerImpl(f.keeper)

	addr := sdk.AccAddress([]byte("destination_20_bytes")[:20])
	steemAccount := "upex"
	record := types.NameRecord{
		SteemAccount:   steemAccount,
		Address:        addr.String(),
		RegistrationId: 1,
		LinkedAt:       35,
	}
	require.NoError(t, f.keeper.ActiveName.Set(f.ctx, steemAccount, record))
	require.NoError(t, f.keeper.ActiveNameByAddress.Set(f.ctx, collections.Join([]byte(addr), steemAccount)))

	bech32Form := addr.String()
	hexForm := common.BytesToAddress(addr).Hex() // 0x-prefixed, EIP-55 checksummed

	viaBech32, err := qs.NamesByAddress(f.ctx, &types.QueryNamesByAddressRequest{Address: bech32Form})
	require.NoError(t, err)
	viaHex, err := qs.NamesByAddress(f.ctx, &types.QueryNamesByAddressRequest{Address: hexForm})
	require.NoError(t, err)

	require.Equal(t, []types.NameRecord{record}, viaBech32.Names)
	require.Equal(t, viaBech32.Names, viaHex.Names, "hex and bech32 forms must resolve to the same account")

	_, err = qs.NamesByAddress(f.ctx, &types.QueryNamesByAddressRequest{Address: "not-an-address"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestAwaitingNameRegistrationsByDestination_AcceptsHex verifies the same
// dual-format acceptance on the awaiting-by-destination query.
func TestAwaitingNameRegistrationsByDestination_AcceptsHex(t *testing.T) {
	f := initFixtureWithFakes(t)
	qs := keeper.NewQueryServerImpl(f.keeper)

	addr := sdk.AccAddress([]byte("awaiting_dest_20byte")[:20])
	reg := types.NameRegistration{
		Id:                 7,
		DerivedDestination: addr.String(),
		Status:             types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_AWAITING_CONFIRMATION,
	}
	require.NoError(t, f.keeper.NameRegistration.Set(f.ctx, reg.Id, reg))
	require.NoError(t, f.keeper.NameRegistrationAwaitingByDest.Set(f.ctx, collections.Join([]byte(addr), reg.Id)))

	viaHex, err := qs.AwaitingNameRegistrationsByDestination(f.ctx,
		&types.QueryAwaitingNameRegistrationsByDestinationRequest{Address: common.BytesToAddress(addr).Hex()})
	require.NoError(t, err)
	require.Len(t, viaHex.Registrations, 1)
	require.Equal(t, uint64(7), viaHex.Registrations[0].Id)
}
