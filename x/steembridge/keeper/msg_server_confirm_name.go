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

// ConfirmName activates a name registration that has reached the validator
// confirmation threshold. Only the memo-derived destination address may
// confirm (enforced by ValidateNameConfirmationAcceptance, shared with the
// ante fee exemption). If the Steem account already has an active link, the
// old link is superseded and replaced atomically within this message —
// re-linking uses the Steem account as the root of trust.
func (k msgServer) ConfirmName(ctx context.Context, msg *types.MsgConfirmName) (*types.MsgConfirmNameResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := k.ValidateNameConfirmationAcceptance(ctx, msg); err != nil {
		return nil, err
	}

	registration, err := k.NameRegistration.Get(ctx, msg.RegistrationId)
	if err != nil {
		return nil, err
	}

	confirmerAddr, err := k.addressCodec.StringToBytes(msg.Confirmer)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid confirmer address")
	}

	// Supersede an existing active link for this Steem account, if any.
	existing, err := k.ActiveName.Get(ctx, registration.SteemAccount)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return nil, err
	}
	if err == nil {
		oldAddr, err := k.addressCodec.StringToBytes(existing.Address)
		if err != nil {
			return nil, err
		}
		if err := k.ActiveNameByAddress.Remove(ctx, collections.Join(oldAddr, existing.SteemAccount)); err != nil {
			return nil, err
		}

		oldRegistration, err := k.NameRegistration.Get(ctx, existing.RegistrationId)
		if err != nil && !errors.Is(err, collections.ErrNotFound) {
			return nil, err
		}
		if err == nil && oldRegistration.Status == types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_ACTIVE {
			if err := k.setNameRegistrationStatus(ctx, &oldRegistration, types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_SUPERSEDED); err != nil {
				return nil, err
			}
			if err := k.NameRegistration.Set(ctx, oldRegistration.Id, oldRegistration); err != nil {
				return nil, err
			}
		}

		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
			types.EventTypeNameSuperseded,
			sdk.NewAttribute(types.AttributeKeySteemAccount, registration.SteemAccount),
			sdk.NewAttribute(types.AttributeKeyOldRegistrationID, strconv.FormatUint(existing.RegistrationId, 10)),
			sdk.NewAttribute(types.AttributeKeyOldAddress, existing.Address),
			sdk.NewAttribute(types.AttributeKeyRegistrationID, strconv.FormatUint(registration.Id, 10)),
		))
	}

	// Install the new active link. The stored address is the canonical
	// derived destination (identical account bytes to the confirmer, per the
	// acceptance check).
	record := types.NameRecord{
		SteemAccount:   registration.SteemAccount,
		Address:        registration.DerivedDestination,
		RegistrationId: registration.Id,
		LinkedAt:       uint64(sdkCtx.BlockHeight()),
	}
	if err := k.ActiveName.Set(ctx, registration.SteemAccount, record); err != nil {
		return nil, err
	}
	if err := k.ActiveNameByAddress.Set(ctx, collections.Join([]byte(confirmerAddr), registration.SteemAccount)); err != nil {
		return nil, err
	}

	// Move the registration to ACTIVE. The by-txid and confirmed-by entries
	// are kept forever: a resolved key is never resubmittable, same as a
	// MINTED deposit.
	registration.ConfirmedAt = uint64(sdkCtx.BlockHeight())
	registration.ConfirmTxHash = txHashHex(sdkCtx)
	if err := k.setNameRegistrationStatus(ctx, &registration, types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_ACTIVE); err != nil {
		return nil, err
	}
	if err := k.NameRegistrationAwaitingByDest.Remove(ctx, collections.Join([]byte(confirmerAddr), registration.Id)); err != nil {
		return nil, err
	}
	if err := k.NameRegistration.Set(ctx, registration.Id, registration); err != nil {
		return nil, err
	}

	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeNameLinked,
		sdk.NewAttribute(types.AttributeKeyRegistrationID, strconv.FormatUint(registration.Id, 10)),
		sdk.NewAttribute(types.AttributeKeySteemAccount, registration.SteemAccount),
		sdk.NewAttribute(types.AttributeKeyAddress, registration.DerivedDestination),
	))

	return &types.MsgConfirmNameResponse{}, nil
}
