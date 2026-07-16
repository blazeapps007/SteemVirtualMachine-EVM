package types_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"steemvm/x/steembridge/types"
)

func validSubmitDepositMsg() *types.MsgSubmitSteemDeposit {
	return &types.MsgSubmitSteemDeposit{
		Validator:        sdk.AccAddress(make([]byte, 20)).String(),
		Txid:             "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // 40 lowercase hex
		OpIndex:          0,
		SteemBlock:       1,
		SteemTimestamp:   "2024-01-01T00:00:00",
		SteemSender:      "alice",
		GatewayAccount:   "gateway",
		AmountMillisteem: 1000,
		Memo:             "",
	}
}

func TestMsgSubmitSteemDeposit_ValidateBasic(t *testing.T) {
	require.NoError(t, validSubmitDepositMsg().ValidateBasic())

	t.Run("uppercase txid rejected", func(t *testing.T) {
		msg := validSubmitDepositMsg()
		msg.Txid = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("short txid rejected", func(t *testing.T) {
		msg := validSubmitDepositMsg()
		msg.Txid = "abc"
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("invalid steem sender rejected", func(t *testing.T) {
		msg := validSubmitDepositMsg()
		msg.SteemSender = "AB"
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("invalid gateway account rejected", func(t *testing.T) {
		msg := validSubmitDepositMsg()
		msg.GatewayAccount = "AB"
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("zero amount rejected", func(t *testing.T) {
		msg := validSubmitDepositMsg()
		msg.AmountMillisteem = 0
		require.Error(t, msg.ValidateBasic())
	})
}
