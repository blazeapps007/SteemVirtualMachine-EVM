package keeper

import (
	"context"
	"strconv"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"steemvm/x/steembridge/types"
)

// ExpireDeposits sweeps pending deposits whose confirmation window
// (Params.DepositTimeoutBlocks) has elapsed without reaching threshold. Per
// the module's expiry design, this removes the (txid, opIndex) dedup index
// entry and every recorded confirmation for that key so the key becomes
// submittable fresh; the Deposit record itself is kept, marked EXPIRED, for
// audit history.
func (k Keeper) ExpireDeposits(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	height := uint64(sdkCtx.BlockHeight())

	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}

	// Collect candidates first: mutating the PENDING status index bucket
	// while walking it would be unsafe.
	var expiredIDs []uint64
	err = k.DepositByStatus.Walk(
		ctx,
		collections.NewPrefixedPairRange[int32, uint64](int32(types.DepositStatus_DEPOSIT_STATUS_PENDING)),
		func(key collections.Pair[int32, uint64]) (bool, error) {
			deposit, err := k.Deposit.Get(ctx, key.K2())
			if err != nil {
				return false, err
			}
			if deposit.CreatedAt+params.DepositTimeoutBlocks < height {
				expiredIDs = append(expiredIDs, key.K2())
			}
			return false, nil
		},
	)
	if err != nil {
		return err
	}

	for _, id := range expiredIDs {
		deposit, err := k.Deposit.Get(ctx, id)
		if err != nil {
			return err
		}

		if err := k.DepositByTxid.Remove(ctx, collections.Join(deposit.Txid, deposit.OpIndex)); err != nil {
			return err
		}
		for _, confirmation := range deposit.ValidatorConfirmations {
			valAddr, err := sdk.ValAddressFromBech32(confirmation.ValidatorAddress)
			if err != nil {
				return err
			}
			if err := k.DepositConfirmedBy.Remove(ctx, collections.Join3(deposit.Txid, deposit.OpIndex, []byte(valAddr))); err != nil {
				return err
			}
		}

		if err := k.setDepositStatus(ctx, &deposit, types.DepositStatus_DEPOSIT_STATUS_EXPIRED); err != nil {
			return err
		}
		if err := k.Deposit.Set(ctx, id, deposit); err != nil {
			return err
		}

		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
			types.EventTypeDepositExpired,
			sdk.NewAttribute(types.AttributeKeyDepositID, strconv.FormatUint(id, 10)),
			sdk.NewAttribute(types.AttributeKeyTxid, deposit.Txid),
			sdk.NewAttribute(types.AttributeKeyOpIndex, strconv.FormatUint(uint64(deposit.OpIndex), 10)),
		))
	}

	return nil
}

// ExpireNameRegistrations sweeps name registrations whose current phase has
// outlived Params.NamePendingTimeoutBlocks. The window applies to each phase
// separately: PENDING registrations are measured from CreatedAt, and
// AWAITING_CONFIRMATION registrations from AwaitingSince — reaching the
// validator threshold restarts the clock so the destination always gets the
// full window to confirm. Expiry removes the (txid, opIndex) dedup entry and
// every recorded attestation so the key becomes submittable fresh; the
// registration record itself is kept, marked EXPIRED, for audit history. The
// by-account index entry is also kept: it is a permanent history index.
func (k Keeper) ExpireNameRegistrations(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	height := uint64(sdkCtx.BlockHeight())

	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}

	// Collect candidates first: mutating the status index buckets while
	// walking them would be unsafe.
	var expiredIDs []uint64
	for _, status := range []types.NameRegistrationStatus{
		types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_PENDING,
		types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_AWAITING_CONFIRMATION,
	} {
		err = k.NameRegistrationByStatus.Walk(
			ctx,
			collections.NewPrefixedPairRange[int32, uint64](int32(status)),
			func(key collections.Pair[int32, uint64]) (bool, error) {
				registration, err := k.NameRegistration.Get(ctx, key.K2())
				if err != nil {
					return false, err
				}
				phaseStart := registration.CreatedAt
				if registration.Status == types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_AWAITING_CONFIRMATION {
					phaseStart = registration.AwaitingSince
				}
				if phaseStart+params.NamePendingTimeoutBlocks < height {
					expiredIDs = append(expiredIDs, key.K2())
				}
				return false, nil
			},
		)
		if err != nil {
			return err
		}
	}

	for _, id := range expiredIDs {
		registration, err := k.NameRegistration.Get(ctx, id)
		if err != nil {
			return err
		}

		if err := k.NameRegistrationByTxid.Remove(ctx, collections.Join(registration.Txid, registration.OpIndex)); err != nil {
			return err
		}
		for _, confirmation := range registration.ValidatorConfirmations {
			valAddr, err := sdk.ValAddressFromBech32(confirmation.ValidatorAddress)
			if err != nil {
				return err
			}
			if err := k.NameRegistrationConfirmedBy.Remove(ctx, collections.Join3(registration.Txid, registration.OpIndex, []byte(valAddr))); err != nil {
				return err
			}
		}
		if registration.Status == types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_AWAITING_CONFIRMATION {
			destAddr, err := k.addressCodec.StringToBytes(registration.DerivedDestination)
			if err != nil {
				return err
			}
			if err := k.NameRegistrationAwaitingByDest.Remove(ctx, collections.Join([]byte(destAddr), id)); err != nil {
				return err
			}
		}

		if err := k.setNameRegistrationStatus(ctx, &registration, types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_EXPIRED); err != nil {
			return err
		}
		if err := k.NameRegistration.Set(ctx, id, registration); err != nil {
			return err
		}

		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
			types.EventTypeNameRegistrationExpired,
			sdk.NewAttribute(types.AttributeKeyRegistrationID, strconv.FormatUint(id, 10)),
			sdk.NewAttribute(types.AttributeKeySteemAccount, registration.SteemAccount),
			sdk.NewAttribute(types.AttributeKeyTxid, registration.Txid),
			sdk.NewAttribute(types.AttributeKeyOpIndex, strconv.FormatUint(uint64(registration.OpIndex), 10)),
		))
	}

	return nil
}
