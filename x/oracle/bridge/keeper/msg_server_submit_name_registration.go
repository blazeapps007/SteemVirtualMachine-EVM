package keeper

import (
	"context"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"steemvm/x/oracle/bridge/types"
)

// SubmitNameRegistration implements the name-service attestation flow,
// mirroring AttestDeposit: verify the signer is a bonded validator,
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
	// duplicate attestation, resolved key) live in the shared validator. Two
	// classes of non-nil result are NOT hard rejections:
	//   - ErrRegistrationMismatch is re-detected below as a benign no-op event.
	//   - already-resolved / duplicate-confirmation (IsBenignAttestationError)
	//     are the natural outcome of validators racing to attest the same
	//     transfer; succeed silently so the redundant zero-fee attestation
	//     does not fail.
	if err := k.ValidateNameRegistrationAcceptance(ctx, msg); err != nil && !errors.Is(err, types.ErrRegistrationMismatch) {
		if types.IsBenignAttestationError(err) {
			return &types.MsgSubmitNameRegistrationResponse{}, nil
		}
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
			// This attestation reports different facts than the record's
			// provisional ones. It is NOT rejected: it votes for its own
			// fact-set candidate below and may yet win the tally. The event is
			// informational for auditing which facts differ.
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
		}
	}

	if err := k.NameRegistrationConfirmedBy.Set(ctx, collections.Join3(msg.Txid, msg.OpIndex, validatorAddr)); err != nil {
		return nil, err
	}
	// Record this validator's vote for the exact fact-set it observed and tally
	// per fact-set, mirroring the deposit flow: the registration resolves to
	// whichever fact-set reaches the threshold.
	factsHash := types.NameFactsHash(msg.SteemBlock, msg.SteemTimestamp, msg.SteemAccount, msg.GatewayAccount, msg.AmountMillisteem, msg.Memo)
	dedup := dedupKeyString(msg.Txid, msg.OpIndex)
	if err := k.NameRegistrationFactsVote.Set(ctx, collections.Join3(dedup, factsHash, validatorAddr)); err != nil {
		return nil, err
	}

	confirmingValidator := sdk.ValAddress(validatorAddr).String()
	registration.ValidatorConfirmations = append(registration.ValidatorConfirmations, &types.Confirmation{
		ValidatorAddress: confirmingValidator,
		Timestamp:        sdkCtx.BlockTime().Unix(),
	})

	ratio, err := k.factsCandidateRatio(ctx, k.NameRegistrationFactsVote, dedup, factsHash)
	if err != nil {
		return nil, err
	}

	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeNameRegistrationConfirmed,
		sdk.NewAttribute(types.AttributeKeyRegistrationID, strconv.FormatUint(registrationID, 10)),
		sdk.NewAttribute(types.AttributeKeyValidator, confirmingValidator),
		sdk.NewAttribute(types.AttributeKeyConfirmedRatio, ratio.String()),
	))

	// Resolve the first time ONE fact-set crosses the threshold, adopting that
	// fact-set's facts as the registration's canonical facts.
	if registration.Status == types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_PENDING && ratio.GTE(params.BridgeConfirmationThreshold) {
		// If the winning fact-set names a different Steem account than the
		// provisional (first-attester) one, move the permanent by-account
		// history index to the winning account so it stays consistent with the
		// record's canonical facts.
		if registration.SteemAccount != msg.SteemAccount {
			if err := k.NameRegistrationByAccount.Remove(ctx, collections.Join(registration.SteemAccount, registrationID)); err != nil {
				return nil, err
			}
			if err := k.NameRegistrationByAccount.Set(ctx, collections.Join(msg.SteemAccount, registrationID)); err != nil {
				return nil, err
			}
		}
		registration.SteemBlock = msg.SteemBlock
		registration.SteemTimestamp = msg.SteemTimestamp
		registration.SteemAccount = msg.SteemAccount
		registration.GatewayAccount = msg.GatewayAccount
		registration.AmountMillisteem = msg.AmountMillisteem
		registration.Memo = msg.Memo
		if err := k.resolveNameRegistration(ctx, &registration, params); err != nil {
			return nil, err
		}
	}

	if err := k.NameRegistration.Set(ctx, registrationID, registration); err != nil {
		return nil, err
	}

	return &types.MsgSubmitNameRegistrationResponse{}, nil
}
