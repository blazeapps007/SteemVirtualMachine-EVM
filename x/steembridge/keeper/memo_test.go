package keeper_test

import (
	"strings"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"steemvm/x/steembridge/keeper"
	"steemvm/x/steembridge/types"
)

func TestDeriveDestination(t *testing.T) {
	rawAcc := make([]byte, 20)
	rawAcc[0] = 0x01
	validBech32 := sdk.AccAddress(rawAcc).String()

	fortyHex := strings.Repeat("ab", 20) // exactly 40 hex chars
	require.Len(t, fortyHex, 40)

	tests := []struct {
		desc         string
		memo         string
		wantOK       bool
		wantDestType types.DestinationType
	}{
		{
			desc:         "valid bech32 cosmos address",
			memo:         validBech32,
			wantOK:       true,
			wantDestType: types.DestinationType_DESTINATION_TYPE_COSMOS,
		},
		{
			desc:         "valid bech32 with surrounding whitespace is trimmed",
			memo:         "  " + validBech32 + "  \n",
			wantOK:       true,
			wantDestType: types.DestinationType_DESTINATION_TYPE_COSMOS,
		},
		{
			desc:         "valid lowercase 0x address",
			memo:         "0x" + fortyHex,
			wantOK:       true,
			wantDestType: types.DestinationType_DESTINATION_TYPE_EVM,
		},
		{
			desc:         "valid uppercase 0x address (EIP-55 case not enforced)",
			memo:         "0x" + strings.ToUpper(fortyHex),
			wantOK:       true,
			wantDestType: types.DestinationType_DESTINATION_TYPE_EVM,
		},
		{
			desc:         "unparseable garbage",
			memo:         "not-an-address",
			wantOK:       false,
			wantDestType: types.DestinationType_DESTINATION_TYPE_NONE,
		},
		{
			desc:         "empty memo",
			memo:         "",
			wantOK:       false,
			wantDestType: types.DestinationType_DESTINATION_TYPE_NONE,
		},
		{
			desc:         "0x address one char too short is unparseable",
			memo:         "0x" + fortyHex[:39],
			wantOK:       false,
			wantDestType: types.DestinationType_DESTINATION_TYPE_NONE,
		},
		{
			desc:         "0x address one char too long is unparseable",
			memo:         "0x" + fortyHex + "0",
			wantOK:       false,
			wantDestType: types.DestinationType_DESTINATION_TYPE_NONE,
		},
		{
			desc:         "0x address with non-hex char is unparseable",
			memo:         "0x" + "zz" + fortyHex[2:],
			wantOK:       false,
			wantDestType: types.DestinationType_DESTINATION_TYPE_NONE,
		},
		{
			desc:         "bech32 with wrong prefix is unparseable",
			memo:         "wrongprefix1" + validBech32[strings.Index(validBech32, "1")+1:],
			wantOK:       false,
			wantDestType: types.DestinationType_DESTINATION_TYPE_NONE,
		},
		{
			desc:         "svm-deposit prefix with bech32 address",
			memo:         "svm-deposit " + validBech32,
			wantOK:       true,
			wantDestType: types.DestinationType_DESTINATION_TYPE_COSMOS,
		},
		{
			desc:         "svm-deposit prefix with 0x address",
			memo:         "svm-deposit 0x" + fortyHex,
			wantOK:       true,
			wantDestType: types.DestinationType_DESTINATION_TYPE_EVM,
		},
		{
			desc:         "svm-register prefix with bech32 address",
			memo:         "svm-register " + validBech32,
			wantOK:       true,
			wantDestType: types.DestinationType_DESTINATION_TYPE_COSMOS,
		},
		{
			desc:         "svm-register prefix with 0x address and extra whitespace",
			memo:         "  svm-register \t 0x" + fortyHex + " ",
			wantOK:       true,
			wantDestType: types.DestinationType_DESTINATION_TYPE_EVM,
		},
		{
			desc:         "prefix with garbage remainder is unparseable",
			memo:         "svm-deposit not-an-address",
			wantOK:       false,
			wantDestType: types.DestinationType_DESTINATION_TYPE_NONE,
		},
		{
			desc:         "prefix alone is unparseable",
			memo:         "svm-register",
			wantOK:       false,
			wantDestType: types.DestinationType_DESTINATION_TYPE_NONE,
		},
		{
			desc:         "prefix glued to address without separator is unparseable",
			memo:         "svm-deposit" + validBech32,
			wantOK:       false,
			wantDestType: types.DestinationType_DESTINATION_TYPE_NONE,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			addr, destType, ok := keeper.DeriveDestination(tc.memo)
			require.Equal(t, tc.wantOK, ok)
			require.Equal(t, tc.wantDestType, destType)
			if ok {
				require.Len(t, addr.Bytes(), 20)
			}
		})
	}
}

func TestDeriveDestination_EVMAndCosmosAgreeOnRawBytes(t *testing.T) {
	// A "0x..." memo must derive the account whose 20 address bytes equal
	// the EVM address bytes directly - no separate erc20 hop.
	fortyHex := strings.Repeat("cd", 20)
	addr, destType, ok := keeper.DeriveDestination("0x" + fortyHex)
	require.True(t, ok)
	require.Equal(t, types.DestinationType_DESTINATION_TYPE_EVM, destType)
	require.Equal(t, strings.Repeat("\xcd", 20), string(addr.Bytes()))
}
