package types_test

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"steemvm/x/steembridge/types"
)

func TestMsgBridgeOut_ValidateBasic(t *testing.T) {
	valid := &types.MsgBridgeOut{
		Sender:                  sdk.AccAddress(make([]byte, 20)).String(),
		DestinationSteemAccount: "null",
		AmountAsteem:            types.MillisteemToAsteem(1000),
	}
	require.NoError(t, valid.ValidateBasic())

	t.Run("nil amount rejected", func(t *testing.T) {
		msg := *valid
		msg.AmountAsteem = math.Int{}
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("zero amount rejected", func(t *testing.T) {
		msg := *valid
		msg.AmountAsteem = math.ZeroInt()
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("negative amount rejected", func(t *testing.T) {
		msg := *valid
		msg.AmountAsteem = math.NewInt(-1)
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("non-multiple of 10^15 rejected", func(t *testing.T) {
		msg := *valid
		msg.AmountAsteem = types.MillisteemToAsteem(1000).AddRaw(1)
		require.ErrorIs(t, msg.ValidateBasic(), types.ErrInvalidAmount)
	})

	t.Run("invalid destination account rejected", func(t *testing.T) {
		msg := *valid
		msg.DestinationSteemAccount = "X"
		require.Error(t, msg.ValidateBasic())
	})
}
