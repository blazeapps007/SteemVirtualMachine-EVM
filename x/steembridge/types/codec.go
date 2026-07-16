package types

import (
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/msgservice"
)

func RegisterInterfaces(registrar codectypes.InterfaceRegistry) {
	registrar.RegisterImplementations((*sdk.Msg)(nil),
		&MsgBridgeOut{},
	)

	registrar.RegisterImplementations((*sdk.Msg)(nil),
		&MsgSubmitSteemDeposit{},
	)

	registrar.RegisterImplementations((*sdk.Msg)(nil),
		&MsgSubmitNameRegistration{},
	)

	registrar.RegisterImplementations((*sdk.Msg)(nil),
		&MsgConfirmName{},
	)

	registrar.RegisterImplementations((*sdk.Msg)(nil),
		&MsgUpdateParams{},
	)
	msgservice.RegisterMsgServiceDesc(registrar, &_Msg_serviceDesc)
}
