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

// AttestWithdrawalPayout records a bonded validator's attestation that the
// gateway paid a bridge-out to the user on Steem. Mirrors the deposit attestation
// flow: dedup per (withdrawalID, validator), fix/match the payout facts, accumulate
// confirmations, and at >=2/3 bonded power flip the withdrawal REQUESTED->PROCESSED.
func (k msgServer) AttestWithdrawalPayout(ctx context.Context, msg *types.MsgAttestWithdrawalPayout) (*types.MsgAttestWithdrawalPayoutResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	params, err := k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}
	if !params.BridgeOutEnabled {
		return nil, types.ErrBridgeOutDisabled
	}

	validatorAddr, err := k.addressCodec.StringToBytes(msg.Validator)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid validator address")
	}
	validator, err := k.stakingKeeper.GetValidator(ctx, sdk.ValAddress(validatorAddr))
	if err != nil || !validator.IsBonded() {
		return nil, types.ErrNotBondedValidator
	}

	withdrawal, err := k.Withdrawal.Get(ctx, msg.WithdrawalId)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return nil, types.ErrWithdrawalNotFound
		}
		return nil, err
	}

	// Already paid out: benign no-op so a late attestation succeeds, not fails.
	if withdrawal.Status == types.WithdrawalStatus_WITHDRAWAL_STATUS_PROCESSED {
		return &types.MsgAttestWithdrawalPayoutResponse{}, nil
	}

	confirmedKey := collections.Join(msg.WithdrawalId, validatorAddr)
	already, err := k.WithdrawalConfirmedBy.Has(ctx, confirmedKey)
	if err != nil {
		return nil, err
	}
	if already {
		return &types.MsgAttestWithdrawalPayoutResponse{}, nil
	}

	// The first attestation fixes the payout facts (txid, op_index); later ones
	// must match or become a benign mismatch event with no record.
	if withdrawal.SteemPayoutTxid == "" {
		withdrawal.SteemPayoutTxid = msg.SteemTxid
		withdrawal.PayoutOpIndex = msg.OpIndex
	} else if withdrawal.SteemPayoutTxid != msg.SteemTxid || withdrawal.PayoutOpIndex != msg.OpIndex {
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
			types.EventTypeWithdrawalPayoutMismatch,
			sdk.NewAttribute(types.AttributeKeyWithdrawalID, strconv.FormatUint(msg.WithdrawalId, 10)),
			sdk.NewAttribute(types.AttributeKeyStoredTxid, withdrawal.SteemPayoutTxid),
			sdk.NewAttribute(types.AttributeKeySubmittedTxid, msg.SteemTxid),
		))
		return &types.MsgAttestWithdrawalPayoutResponse{}, nil
	}

	if err := k.WithdrawalConfirmedBy.Set(ctx, confirmedKey); err != nil {
		return nil, err
	}
	confirmingValidator := sdk.ValAddress(validatorAddr).String()
	withdrawal.ValidatorConfirmations = append(withdrawal.ValidatorConfirmations, &types.Confirmation{
		ValidatorAddress: confirmingValidator,
		Timestamp:        sdkCtx.BlockTime().Unix(),
	})

	ratio, err := k.confirmedPowerRatio(ctx, withdrawal.ValidatorConfirmations)
	if err != nil {
		return nil, err
	}

	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeWithdrawalConfirmed,
		sdk.NewAttribute(types.AttributeKeyWithdrawalID, strconv.FormatUint(msg.WithdrawalId, 10)),
		sdk.NewAttribute(types.AttributeKeyValidator, confirmingValidator),
		sdk.NewAttribute(types.AttributeKeyConfirmedRatio, ratio.String()),
	))

	// Flip to PROCESSED exactly once, the first time the threshold is crossed.
	if withdrawal.Status == types.WithdrawalStatus_WITHDRAWAL_STATUS_REQUESTED && ratio.GTE(params.BridgeConfirmationThreshold) {
		if err := k.WithdrawalByStatus.Remove(ctx, collections.Join(int32(types.WithdrawalStatus_WITHDRAWAL_STATUS_REQUESTED), msg.WithdrawalId)); err != nil {
			return nil, err
		}
		withdrawal.Status = types.WithdrawalStatus_WITHDRAWAL_STATUS_PROCESSED
		withdrawal.ProcessedAt = uint64(sdkCtx.BlockHeight())
		if err := k.WithdrawalByStatus.Set(ctx, collections.Join(int32(types.WithdrawalStatus_WITHDRAWAL_STATUS_PROCESSED), msg.WithdrawalId)); err != nil {
			return nil, err
		}
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
			types.EventTypeWithdrawalProcessed,
			sdk.NewAttribute(types.AttributeKeyWithdrawalID, strconv.FormatUint(msg.WithdrawalId, 10)),
			sdk.NewAttribute(types.AttributeKeySteemTxid, msg.SteemTxid),
		))
		// A finalized payout is a bridge-duty opportunity: report the attesters
		// that carried it to the unified slashing engine.
		attesterStrs := make([]string, 0, len(withdrawal.ValidatorConfirmations))
		for _, c := range withdrawal.ValidatorConfirmations {
			attesterStrs = append(attesterStrs, c.ValidatorAddress)
		}
		if err := k.reportBridgeEvent(ctx, attesterStrs); err != nil {
			return nil, err
		}
	}

	if err := k.Withdrawal.Set(ctx, msg.WithdrawalId, withdrawal); err != nil {
		return nil, err
	}
	return &types.MsgAttestWithdrawalPayoutResponse{}, nil
}

// ValidateWithdrawalProcessedAcceptance is the read-only acceptance check shared
// with the ante fee-exemption decorator: a payout attestation is fee-exempt if
// bridge-out is enabled, the withdrawal exists and is REQUESTED, this validator
// has not already attested it, and (if payout facts are already recorded) they
// match. Benign outcomes (already processed / duplicate) still qualify; a fact
// mismatch does not (it stays fee-paying, like a deposit mismatch).
func (k Keeper) ValidateWithdrawalProcessedAcceptance(ctx context.Context, msg *types.MsgAttestWithdrawalPayout) error {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	if !params.BridgeOutEnabled {
		return types.ErrBridgeOutDisabled
	}
	validatorAddr, err := k.addressCodec.StringToBytes(msg.Validator)
	if err != nil {
		return err
	}
	withdrawal, err := k.Withdrawal.Get(ctx, msg.WithdrawalId)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return types.ErrWithdrawalNotFound
		}
		return err
	}
	if withdrawal.Status == types.WithdrawalStatus_WITHDRAWAL_STATUS_PROCESSED {
		return types.ErrWithdrawalAlreadyProcessed
	}
	already, err := k.WithdrawalConfirmedBy.Has(ctx, collections.Join(msg.WithdrawalId, validatorAddr))
	if err != nil {
		return err
	}
	if already {
		return types.ErrDuplicateWithdrawalConfirmation
	}
	if withdrawal.SteemPayoutTxid != "" && (withdrawal.SteemPayoutTxid != msg.SteemTxid || withdrawal.PayoutOpIndex != msg.OpIndex) {
		return types.ErrWithdrawalPayoutMismatch
	}
	return nil
}
