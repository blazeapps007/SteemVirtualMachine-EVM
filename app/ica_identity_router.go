package app

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	"github.com/cosmos/cosmos-sdk/baseapp"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	icatypes "github.com/cosmos/ibc-go/v11/modules/apps/27-interchain-accounts/types"

	steembridgekeeper "steemvm/x/oracle/bridge/keeper"
)

// icaIdentityRouter is the message router handed to the interchain-accounts
// host keeper. The host executes packet messages straight through the router
// (ibc-go host keeper relay.go), never through the ante handler, so the
// validator identity gate there (steembridgeValidatorGateDecorator) would not
// see an interchain account's MsgCreateValidator/MsgEditValidator. This
// wrapper applies the same two keeper checks the gate uses before dispatching
// those, and passes every other message through untouched — so ICA can keep
// allow_messages ["*"] without opening an identity bypass.
type icaIdentityRouter struct {
	inner    icatypes.MessageRouter
	identity validatorIdentityChecker
}

// validatorIdentityChecker is the slice of the steembridge keeper this router
// needs — the same two checks steembridgeValidatorGateDecorator calls.
type validatorIdentityChecker interface {
	ValidateValidatorCreationEligibility(ctx context.Context, operatorValBytes []byte, moniker, details string) error
	ValidateValidatorEdit(ctx context.Context, operatorValBytes []byte, editMoniker, editDetails string) error
}

var (
	_ icatypes.MessageRouter   = icaIdentityRouter{}
	_ validatorIdentityChecker = steembridgekeeper.Keeper{}
)

func newICAIdentityRouter(inner icatypes.MessageRouter, identity validatorIdentityChecker) icaIdentityRouter {
	return icaIdentityRouter{inner: inner, identity: identity}
}

func (r icaIdentityRouter) Handler(msg sdk.Msg) baseapp.MsgServiceHandler {
	handler := r.inner.Handler(msg)
	if handler == nil {
		return nil
	}

	switch msg.(type) {
	case *stakingtypes.MsgCreateValidator, *stakingtypes.MsgEditValidator:
		return func(ctx sdk.Context, req sdk.Msg) (*sdk.Result, error) {
			if err := r.checkValidatorIdentity(ctx, req); err != nil {
				return nil, err
			}
			return handler(ctx, req)
		}
	default:
		return handler
	}
}

// checkValidatorIdentity mirrors steembridgeValidatorGateDecorator.AnteHandle
// exactly, so a message is accepted here if and only if it would be accepted
// as a direct Cosmos tx.
func (r icaIdentityRouter) checkValidatorIdentity(ctx sdk.Context, req sdk.Msg) error {
	switch m := req.(type) {
	case *stakingtypes.MsgCreateValidator:
		valAddr, err := sdk.ValAddressFromBech32(m.ValidatorAddress)
		if err != nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid validator address: %s", err)
		}
		if err := r.identity.ValidateValidatorCreationEligibility(ctx, valAddr.Bytes(), m.Description.Moniker, m.Description.Details); err != nil {
			return errorsmod.Wrap(sdkerrors.ErrUnauthorized, err.Error())
		}
	case *stakingtypes.MsgEditValidator:
		valAddr, err := sdk.ValAddressFromBech32(m.ValidatorAddress)
		if err != nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid validator address: %s", err)
		}
		if err := r.identity.ValidateValidatorEdit(ctx, valAddr.Bytes(), m.Description.Moniker, m.Description.Details); err != nil {
			return errorsmod.Wrap(sdkerrors.ErrUnauthorized, err.Error())
		}
	}
	return nil
}
