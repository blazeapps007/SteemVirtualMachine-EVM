package steembridge

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/core/vm"

	cmn "github.com/cosmos/evm/precompiles/common"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"steemvm/x/steembridge/types"
)

const (
	// ConfirmNameMethod defines the ABI method name for the steembridge
	// ConfirmName transaction.
	ConfirmNameMethod = "confirmName"
	// BridgeOutMethod defines the ABI method name for the steembridge
	// BridgeOut transaction.
	BridgeOutMethod = "bridgeOut"
)

// ConfirmName implements the confirmName precompile transaction, letting the
// memo-derived destination of a name registration confirm it from an EVM
// wallet. The caller is bound as the msg confirmer: this binding IS the
// authorization — the msg server's ValidateNameConfirmationAcceptance checks
// that confirmer == derived destination but trusts the Confirmer string,
// since on the Cosmos path the ante handler has already verified the
// signature. Here contract.Caller() plays that role.
func (p Precompile) ConfirmName(
	ctx sdk.Context,
	method *abi.Method,
	stateDB vm.StateDB,
	contract *vm.Contract,
	args []interface{},
) ([]byte, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf(cmn.ErrInvalidNumberOfArgs, 1, len(args))
	}

	registrationID, ok := args[0].(uint64)
	if !ok {
		return nil, fmt.Errorf("invalid registration id")
	}

	msgSender := contract.Caller()
	confirmer, err := p.addrCodec.BytesToString(msgSender.Bytes())
	if err != nil {
		return nil, fmt.Errorf("failed to convert caller address: %w", err)
	}

	msg := &types.MsgConfirmName{
		Confirmer:      confirmer,
		RegistrationId: registrationID,
	}

	if _, err := p.msgServer.ConfirmName(ctx, msg); err != nil {
		return nil, err
	}

	// The registration's SteemAccount is immutable, safe to read after the call.
	registration, err := p.keeper.NameRegistration.Get(ctx, registrationID)
	if err != nil {
		return nil, err
	}

	if err := p.EmitNameConfirmedEvent(ctx, stateDB, msgSender, registrationID, registration.SteemAccount); err != nil {
		return nil, err
	}

	return method.Outputs.Pack(true)
}

// BridgeOut implements the bridgeOut precompile transaction, burning asteem
// from the caller and recording a withdrawal for validators to relay to
// Steem. The caller is bound as the msg sender. The burned balance reaches
// the EVM stateDB via the shared balance handler, which replays the bank's
// coin_spent event — no bookkeeping is needed here.
func (p Precompile) BridgeOut(
	ctx sdk.Context,
	method *abi.Method,
	stateDB vm.StateDB,
	contract *vm.Contract,
	args []interface{},
) ([]byte, error) {
	if len(args) != 3 {
		return nil, fmt.Errorf(cmn.ErrInvalidNumberOfArgs, 3, len(args))
	}

	destination, ok := args[0].(string)
	if !ok {
		return nil, fmt.Errorf("invalid destination steem account")
	}
	amountBig, ok := args[1].(*big.Int)
	if !ok || amountBig == nil {
		return nil, fmt.Errorf("invalid amount")
	}
	if amountBig.Sign() <= 0 {
		return nil, fmt.Errorf("amount must be positive")
	}
	memo, ok := args[2].(string)
	if !ok {
		return nil, fmt.Errorf("invalid memo")
	}

	msgSender := contract.Caller()
	sender, err := p.addrCodec.BytesToString(msgSender.Bytes())
	if err != nil {
		return nil, fmt.Errorf("failed to convert caller address: %w", err)
	}

	// Peek returns exactly the id the msg server's WithdrawalSeq.Next will
	// assign within this same execution, giving the event a tracking handle
	// without changing the (empty) MsgBridgeOutResponse.
	withdrawalID, err := p.keeper.WithdrawalSeq.Peek(ctx)
	if err != nil {
		return nil, err
	}

	msg := &types.MsgBridgeOut{
		Sender:                  sender,
		DestinationSteemAccount: destination,
		AmountAsteem:            math.NewIntFromBigInt(amountBig),
		Memo:                    memo,
	}

	if _, err := p.msgServer.BridgeOut(ctx, msg); err != nil {
		return nil, err
	}

	if err := p.EmitBridgeOutRequestedEvent(ctx, stateDB, msgSender, destination, amountBig, memo, withdrawalID); err != nil {
		return nil, err
	}

	return method.Outputs.Pack(true)
}
