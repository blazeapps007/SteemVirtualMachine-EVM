package types_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"steemvm/x/steembridge/types"
)

func validSubmitNameRegistrationMsg() *types.MsgSubmitNameRegistration {
	return &types.MsgSubmitNameRegistration{
		Validator:        sdk.AccAddress(make([]byte, 20)).String(),
		Txid:             "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // 40 lowercase hex
		OpIndex:          0,
		SteemBlock:       1,
		SteemTimestamp:   "2024-01-01T00:00:00",
		SteemAccount:     "alice",
		GatewayAccount:   "gateway",
		AmountMillisteem: 1,
		Memo:             "0x1111111111111111111111111111111111111111",
	}
}

func TestMsgSubmitNameRegistration_ValidateBasic(t *testing.T) {
	require.NoError(t, validSubmitNameRegistrationMsg().ValidateBasic())

	t.Run("uppercase txid rejected", func(t *testing.T) {
		msg := validSubmitNameRegistrationMsg()
		msg.Txid = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("short txid rejected", func(t *testing.T) {
		msg := validSubmitNameRegistrationMsg()
		msg.Txid = "abc"
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("invalid steem account rejected", func(t *testing.T) {
		msg := validSubmitNameRegistrationMsg()
		msg.SteemAccount = "AB"
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("invalid gateway account rejected", func(t *testing.T) {
		msg := validSubmitNameRegistrationMsg()
		msg.GatewayAccount = "AB"
		require.Error(t, msg.ValidateBasic())
	})

	t.Run("zero amount rejected", func(t *testing.T) {
		msg := validSubmitNameRegistrationMsg()
		msg.AmountMillisteem = 0
		require.Error(t, msg.ValidateBasic())
	})
}

func TestMsgConfirmName_ValidateBasic(t *testing.T) {
	// registration_id 0 is valid: the registration sequence starts at 0.
	msg := &types.MsgConfirmName{
		Confirmer:      sdk.AccAddress(make([]byte, 20)).String(),
		RegistrationId: 0,
	}
	require.NoError(t, msg.ValidateBasic())
}
