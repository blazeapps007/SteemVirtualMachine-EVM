package keeper

import (
	"context"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"steemvm/x/steembridge/types"
)

// lookupNameRegistrationByTxid resolves the (txid, opIndex) dedup key to its
// full NameRegistration record, if one has ever been created for that key.
func (k Keeper) lookupNameRegistrationByTxid(ctx context.Context, txid string, opIndex uint32) (registration types.NameRegistration, found bool, err error) {
	id, err := k.NameRegistrationByTxid.Get(ctx, collections.Join(txid, opIndex))
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return types.NameRegistration{}, false, nil
		}
		return types.NameRegistration{}, false, err
	}
	registration, err = k.NameRegistration.Get(ctx, id)
	if err != nil {
		return types.NameRegistration{}, false, err
	}
	return registration, true, nil
}

// matchesPendingRegistration reports whether a new submission's raw facts
// exactly match an existing pending registration's stored raw facts.
// (txid, opIndex) are equal by construction since existing was looked up by
// that same key.
func matchesPendingRegistration(existing types.NameRegistration, msg *types.MsgSubmitNameRegistration) bool {
	return existing.SteemBlock == msg.SteemBlock &&
		existing.SteemTimestamp == msg.SteemTimestamp &&
		existing.SteemAccount == msg.SteemAccount &&
		existing.GatewayAccount == msg.GatewayAccount &&
		existing.AmountMillisteem == msg.AmountMillisteem &&
		existing.Memo == msg.Memo
}

// ValidateNameRegistrationAcceptance performs the read-only, stateful checks
// that decide whether a MsgSubmitNameRegistration submission would be
// accepted by SubmitNameRegistration: the name service must be enabled, the
// gateway account must match, the amount must reach the registration
// minimum, this validator must not have already attested this key, the key
// must not already be resolved, and if a pending registration already exists
// its raw facts must exactly match this submission.
//
// This is the single source of truth shared by both SubmitNameRegistration
// itself and the ante handler's fee-exemption decorator
// (app/ante_steembridge.go), so the two can never drift on what counts as
// "would be accepted." It performs no state mutation.
//
// Unlike deposits, the minimum amount is checked here (not only at
// resolution): a below-minimum registration can never become valid, so it is
// rejected before it consumes free-tx quota or creates state.
func (k Keeper) ValidateNameRegistrationAcceptance(ctx context.Context, msg *types.MsgSubmitNameRegistration) error {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	if !params.NameServiceEnabled {
		return types.ErrNameServiceDisabled
	}
	if msg.GatewayAccount != params.GatewayAccount {
		return types.ErrInvalidGatewayAccount
	}
	if msg.AmountMillisteem < params.NameRegistrationMinMillisteem {
		return types.ErrRegistrationBelowMinimum
	}

	validatorAddr, err := k.addressCodec.StringToBytes(msg.Validator)
	if err != nil {
		return err
	}

	alreadyConfirmed, err := k.NameRegistrationConfirmedBy.Has(ctx, collections.Join3(msg.Txid, msg.OpIndex, validatorAddr))
	if err != nil {
		return err
	}
	if alreadyConfirmed {
		return types.ErrDuplicateConfirmation
	}

	existing, found, err := k.lookupNameRegistrationByTxid(ctx, msg.Txid, msg.OpIndex)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}

	if existing.Status != types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_PENDING {
		// AWAITING_CONFIRMATION, ACTIVE, UNCLAIMABLE and SUPERSEDED all block
		// resubmission; EXPIRED never reaches here because expiry removes the
		// by-txid entry so the key looks fresh.
		return types.ErrRegistrationAlreadyResolved
	}

	if !matchesPendingRegistration(existing, msg) {
		return types.ErrRegistrationMismatch
	}

	return nil
}

// ValidateNameConfirmationAcceptance performs the read-only, stateful checks
// that decide whether a MsgConfirmName would be accepted by ConfirmName.
// Shared by the msg server and the ante fee-exemption decorator; performs no
// state mutation.
func (k Keeper) ValidateNameConfirmationAcceptance(ctx context.Context, msg *types.MsgConfirmName) error {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	if !params.NameServiceEnabled {
		return types.ErrNameServiceDisabled
	}

	registration, err := k.NameRegistration.Get(ctx, msg.RegistrationId)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return types.ErrRegistrationNotFound
		}
		return err
	}
	if registration.Status != types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_AWAITING_CONFIRMATION {
		return types.ErrRegistrationNotAwaiting
	}

	confirmerAddr, err := k.addressCodec.StringToBytes(msg.Confirmer)
	if err != nil {
		return err
	}
	destAddr, err := k.addressCodec.StringToBytes(registration.DerivedDestination)
	if err != nil {
		return err
	}
	// Byte-wise comparison of the underlying 20-byte accounts, so a signer is
	// recognized regardless of the string form its address was rendered in.
	if !sdk.AccAddress(confirmerAddr).Equals(sdk.AccAddress(destAddr)) {
		return types.ErrConfirmerNotDestination
	}

	return nil
}

// setNameRegistrationStatus moves a registration between status index
// buckets and updates its in-memory Status field. Callers persist the record
// via k.NameRegistration.Set.
func (k Keeper) setNameRegistrationStatus(ctx context.Context, registration *types.NameRegistration, newStatus types.NameRegistrationStatus) error {
	if err := k.NameRegistrationByStatus.Remove(ctx, collections.Join(int32(registration.Status), registration.Id)); err != nil {
		return err
	}
	registration.Status = newStatus
	return k.NameRegistrationByStatus.Set(ctx, collections.Join(int32(newStatus), registration.Id))
}

// resolveNameRegistration is called exactly once per registration, the
// moment its confirmed voting power ratio first reaches the threshold. It
// derives the destination from the memo (in consensus, never trusting the
// client) and either parks the registration as AWAITING_CONFIRMATION for the
// destination to accept via MsgConfirmName, or marks it UNCLAIMABLE. The name
// link only activates once its destination consents, but the registration fee
// IS credited to the destination here (via the bridge's shared
// creditBridgedSteem path) so it can pay gas for an EVM confirmName call.
// Mutates registration in place; the caller persists it.
func (k Keeper) resolveNameRegistration(ctx context.Context, registration *types.NameRegistration, params types.Params) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	destAddr, destType, ok := DeriveDestination(registration.Memo)

	// Re-check the minimum in case params changed between acceptance and
	// resolution; a below-minimum registration must never activate.
	if !ok || registration.AmountMillisteem < params.NameRegistrationMinMillisteem {
		registration.DestinationType = destType
		if err := k.setNameRegistrationStatus(ctx, registration, types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_UNCLAIMABLE); err != nil {
			return err
		}
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
			types.EventTypeNameRegistrationUnclaimable,
			sdk.NewAttribute(types.AttributeKeyRegistrationID, strconv.FormatUint(registration.Id, 10)),
			sdk.NewAttribute(types.AttributeKeySteemAccount, registration.SteemAccount),
		))
		return nil
	}

	destAddrStr, err := k.addressCodec.BytesToString(destAddr)
	if err != nil {
		return err
	}

	// Credit the registration fee to the destination through the bridge's
	// shared mint path. The fee is real STEEM held at the gateway on Steem, so
	// this mint is backed 1:1 exactly like a deposit; it funds the destination
	// so it can pay gas for the EVM/MetaMask confirmName call (the Cosmos-CLI
	// confirm path is already fee-exempt, but the EVM path is not).
	mintAmount, err := k.creditBridgedSteem(ctx, destAddr, registration.AmountMillisteem)
	if err != nil {
		return err
	}

	registration.DerivedDestination = destAddrStr
	registration.DestinationType = destType
	registration.AwaitingSince = uint64(sdkCtx.BlockHeight())
	if err := k.setNameRegistrationStatus(ctx, registration, types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_AWAITING_CONFIRMATION); err != nil {
		return err
	}
	if err := k.NameRegistrationAwaitingByDest.Set(ctx, collections.Join([]byte(destAddr), registration.Id)); err != nil {
		return err
	}

	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeNameRegistrationAwaiting,
		sdk.NewAttribute(types.AttributeKeyRegistrationID, strconv.FormatUint(registration.Id, 10)),
		sdk.NewAttribute(types.AttributeKeySteemAccount, registration.SteemAccount),
		sdk.NewAttribute(types.AttributeKeyDestination, destAddrStr),
		sdk.NewAttribute(types.AttributeKeyDestinationType, destType.String()),
		sdk.NewAttribute(types.AttributeKeyAmount, mintAmount.String()),
	))
	return nil
}
