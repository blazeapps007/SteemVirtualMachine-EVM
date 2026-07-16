package keeper

import (
	"context"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"steemvm/x/steembridge/types"
)

// SubmitNameRegistration implements the name-service attestation flow,
// mirroring SubmitSteemDeposit: verify the signer is a bonded validator,
// dedup on (txid, opIndex), record or match against the pending
// registration, accumulate live bonded voting power, and — once the
// confirmation threshold is first reached — park the registration as
// AWAITING_CONFIRMATION for the memo-derived destination to accept via
// MsgConfirmName. Acceptance checks live in
// ValidateNameRegistrationAcceptance, shared with the ante fee exemption.
func (k msgServer) SubmitNameRegistration(ctx context.Context, msg *types.MsgSubmitNameRegistration) (*types.MsgSubmitNameRegistrationResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	params, err := k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}

	validatorAddr, err := k.addressCodec.StringToBytes(msg.Validator)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid validator address")
	}

	validator, err := k.stakingKeeper.GetValidator(ctx, sdk.ValAddress(validatorAddr))
	if err != nil || !validator.IsBonded() {
		return nil, types.ErrNotBondedValidator
	}

	// All read-only acceptance rules (enabled, gateway, minimum amount,
	// duplicate attestation, resolved key) live in the shared validator; the
	// mismatch case is re-detected below because it must be a benign
	// non-error outcome rather than a rejection.
	if err := k.ValidateNameRegistrationAcceptance(ctx, msg); err != nil && !errors.Is(err, types.ErrRegistrationMismatch) {
		return nil, err
	}

	dedupKey := collections.Join(msg.Txid, msg.OpIndex)
	registrationID, err := k.NameRegistrationByTxid.Get(ctx, dedupKey)
	isNew := errors.Is(err, collections.ErrNotFound)
	if err != nil && !isNew {
		return nil, err
	}

	var registration types.NameRegistration
	if isNew {
		registrationID, err = k.NameRegistrationSeq.Next(ctx)
		if err != nil {
			return nil, err
		}

		registration = types.NameRegistration{
			Id:               registrationID,
			Txid:             msg.Txid,
			OpIndex:          msg.OpIndex,
			SteemBlock:       msg.SteemBlock,
			SteemTimestamp:   msg.SteemTimestamp,
			SteemAccount:     msg.SteemAccount,
			GatewayAccount:   msg.GatewayAccount,
			AmountMillisteem: msg.AmountMillisteem,
			Memo:             msg.Memo,
			Status:           types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_PENDING,
			CreatedAt:        uint64(sdkCtx.BlockHeight()),
		}
		if err := k.NameRegistrationByTxid.Set(ctx, dedupKey, registrationID); err != nil {
			return nil, err
		}
		if err := k.NameRegistrationByStatus.Set(ctx, collections.Join(int32(types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_PENDING), registrationID)); err != nil {
			return nil, err
		}
		if err := k.NameRegistrationByAccount.Set(ctx, collections.Join(msg.SteemAccount, registrationID)); err != nil {
			return nil, err
		}

		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
			types.EventTypeNameRegistrationCreated,
			sdk.NewAttribute(types.AttributeKeyRegistrationID, strconv.FormatUint(registrationID, 10)),
			sdk.NewAttribute(types.AttributeKeySteemAccount, msg.SteemAccount),
			sdk.NewAttribute(types.AttributeKeyTxid, msg.Txid),
			sdk.NewAttribute(types.AttributeKeyOpIndex, strconv.FormatUint(uint64(msg.OpIndex), 10)),
		))
	} else {
		registration, err = k.NameRegistration.Get(ctx, registrationID)
		if err != nil {
			return nil, err
		}

		if !matchesPendingRegistration(registration, msg) {
			// The submission is rejected but the pending registration is left
			// completely untouched. This is deliberately a benign (non-error)
			// message outcome, not a failed tx, so the mismatch event below
			// is actually committed and observable on-chain for auditing —
			// an erroring message would revert its own events along with
			// everything else in the tx.
			sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
				types.EventTypeNameRegistrationMismatch,
				sdk.NewAttribute(types.AttributeKeyRegistrationID, strconv.FormatUint(registrationID, 10)),
				sdk.NewAttribute(types.AttributeKeyStoredSteemBlock, strconv.FormatUint(registration.SteemBlock, 10)),
				sdk.NewAttribute(types.AttributeKeySubmittedSteemBlock, strconv.FormatUint(msg.SteemBlock, 10)),
				sdk.NewAttribute(types.AttributeKeyStoredTimestamp, registration.SteemTimestamp),
				sdk.NewAttribute(types.AttributeKeySubmittedTimestamp, msg.SteemTimestamp),
				sdk.NewAttribute(types.AttributeKeyStoredAccount, registration.SteemAccount),
				sdk.NewAttribute(types.AttributeKeySubmittedAccount, msg.SteemAccount),
				sdk.NewAttribute(types.AttributeKeyStoredGateway, registration.GatewayAccount),
				sdk.NewAttribute(types.AttributeKeySubmittedGateway, msg.GatewayAccount),
				sdk.NewAttribute(types.AttributeKeyStoredAmount, strconv.FormatUint(registration.AmountMillisteem, 10)),
				sdk.NewAttribute(types.AttributeKeySubmittedAmount, strconv.FormatUint(msg.AmountMillisteem, 10)),
				sdk.NewAttribute(types.AttributeKeyStoredMemo, registration.Memo),
				sdk.NewAttribute(types.AttributeKeySubmittedMemo, msg.Memo),
			))
			return &types.MsgSubmitNameRegistrationResponse{}, nil
		}
	}

	if err := k.NameRegistrationConfirmedBy.Set(ctx, collections.Join3(msg.Txid, msg.OpIndex, validatorAddr)); err != nil {
		return nil, err
	}

	confirmingValidator := sdk.ValAddress(validatorAddr).String()
	registration.ValidatorConfirmations = append(registration.ValidatorConfirmations, &types.Confirmation{
		ValidatorAddress: confirmingValidator,
		Timestamp:        sdkCtx.BlockTime().Unix(),
	})

	ratio, err := k.confirmedPowerRatio(ctx, registration.ValidatorConfirmations)
	if err != nil {
		return nil, err
	}

	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeNameRegistrationConfirmed,
		sdk.NewAttribute(types.AttributeKeyRegistrationID, strconv.FormatUint(registrationID, 10)),
		sdk.NewAttribute(types.AttributeKeyValidator, confirmingValidator),
		sdk.NewAttribute(types.AttributeKeyConfirmedRatio, ratio.String()),
	))

	if ratio.GTE(params.BridgeConfirmationThreshold) {
		if err := k.resolveNameRegistration(ctx, &registration, params); err != nil {
			return nil, err
		}
	}

	if err := k.NameRegistration.Set(ctx, registrationID, registration); err != nil {
		return nil, err
	}

	return &types.MsgSubmitNameRegistrationResponse{}, nil
}
