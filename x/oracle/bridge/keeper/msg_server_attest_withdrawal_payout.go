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
	// A payout from before the Steem anchor predates this chain: its
	// "svm-withdrawal <id>" memo refers to another chain's withdrawal IDs.
	if err := params.CheckSteemBlock(msg.SteemBlock); err != nil {
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
	// Already auto-refunded (WithdrawalTimeoutBlocks elapsed, see
	// RefundExpiredWithdrawals): still a benign no-op for the tx (never
	// re-processed, never paid twice from this side), but a loud audit event
	// — the payout apparently DID land on Steem after all, just after the
	// refund already fired. This is the double-spend risk the timeout-refund
	// design deliberately accepts; logging it is the only follow-up, by design.
	if withdrawal.Status == types.WithdrawalStatus_WITHDRAWAL_STATUS_REFUNDED {
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
			types.EventTypeWithdrawalPayoutAttestedAfterRefund,
			sdk.NewAttribute(types.AttributeKeyWithdrawalID, strconv.FormatUint(msg.WithdrawalId, 10)),
			sdk.NewAttribute(types.AttributeKeyValidator, msg.Validator),
			sdk.NewAttribute(types.AttributeKeySteemTxid, msg.SteemTxid),
		))
		return &types.MsgAttestWithdrawalPayoutResponse{}, nil
	}

	// The chain already knows the correct payout from the Withdrawal record
	// itself (it was fixed at bridge-out time, not externally observed like a
	// deposit) — so unlike txid/op_index, this isn't "first attestation wins,"
	// it's a hard check against ground truth. A validator reporting any other
	// payout doesn't get to fix the record or count toward confirmation: this
	// is exactly what stops a wrong amount, asset, recipient or memo on Steem
	// from ever being confirmed as a valid payout.
	if !payoutMatches(withdrawal, msg) {
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
			types.EventTypeWithdrawalPayoutAssetMismatch,
			sdk.NewAttribute(types.AttributeKeyWithdrawalID, strconv.FormatUint(msg.WithdrawalId, 10)),
			sdk.NewAttribute(types.AttributeKeyValidator, msg.Validator),
			sdk.NewAttribute(types.AttributeKeyStoredAmount, strconv.FormatUint(withdrawal.AmountMillisteem, 10)),
			sdk.NewAttribute(types.AttributeKeySubmittedAmount, strconv.FormatUint(msg.AmountMillisteem, 10)),
			sdk.NewAttribute(types.AttributeKeyStoredAsset, types.DenomForAsset(withdrawal.Asset)),
			sdk.NewAttribute(types.AttributeKeySubmittedAsset, types.DenomForAsset(msg.Asset)),
			sdk.NewAttribute(types.AttributeKeyStoredDestination, withdrawal.DestinationSteemAccount),
			sdk.NewAttribute(types.AttributeKeySubmittedDestination, msg.DestinationSteemAccount),
			sdk.NewAttribute(types.AttributeKeyStoredMemo, withdrawal.Memo),
			sdk.NewAttribute(types.AttributeKeySubmittedMemo, msg.Memo),
		))
		return &types.MsgAttestWithdrawalPayoutResponse{}, nil
	}

	confirmedKey := collections.Join(msg.WithdrawalId, validatorAddr)
	already, err := k.WithdrawalConfirmedBy.Has(ctx, confirmedKey)
	if err != nil {
		return nil, err
	}
	if already {
		// This validator already attested a payout for this withdrawal. It gets
		// exactly one vote, which is what stops two different payout candidates
		// from both reaching the threshold.
		return &types.MsgAttestWithdrawalPayoutResponse{}, nil
	}

	// Record this validator's vote for its observed payout candidate. Payout
	// attestations are tallied PER (txid, op-index) candidate — the first
	// attester no longer fixes the facts, so a minority validator reporting a
	// bogus txid cannot block the honest majority's real payout.
	candidate := payoutCandidateKey(msg.SteemTxid, msg.OpIndex)
	if err := k.WithdrawalConfirmedBy.Set(ctx, confirmedKey); err != nil {
		return nil, err
	}
	if err := k.WithdrawalPayoutVote.Set(ctx, collections.Join3(msg.WithdrawalId, candidate, validatorAddr)); err != nil {
		return nil, err
	}
	confirmingValidator := sdk.ValAddress(validatorAddr).String()
	withdrawal.ValidatorConfirmations = append(withdrawal.ValidatorConfirmations, &types.Confirmation{
		ValidatorAddress: confirmingValidator,
		Timestamp:        sdkCtx.BlockTime().Unix(),
	})

	ratio, err := k.candidatePayoutRatio(ctx, msg.WithdrawalId, candidate)
	if err != nil {
		return nil, err
	}

	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeWithdrawalConfirmed,
		sdk.NewAttribute(types.AttributeKeyWithdrawalID, strconv.FormatUint(msg.WithdrawalId, 10)),
		sdk.NewAttribute(types.AttributeKeyValidator, confirmingValidator),
		sdk.NewAttribute(types.AttributeKeySteemTxid, msg.SteemTxid),
		sdk.NewAttribute(types.AttributeKeyConfirmedRatio, ratio.String()),
	))

	// Flip to PROCESSED the first time ONE payout candidate crosses the
	// threshold, recording that candidate's facts as the canonical payout.
	if withdrawal.Status == types.WithdrawalStatus_WITHDRAWAL_STATUS_REQUESTED && ratio.GTE(params.BridgeConfirmationThreshold) {
		withdrawal.SteemPayoutTxid = msg.SteemTxid
		withdrawal.PayoutOpIndex = msg.OpIndex
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
		// A finalized payout is a bridge-duty opportunity. Queue it for the
		// EndBlocker to report (full same-block attester set); see
		// enqueueBridgeDuty.
		if err := k.enqueueBridgeDuty(ctx, bridgeDutyKindWithdrawal, withdrawal.Id); err != nil {
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
// mismatch does not (it stays fee-paying, like a deposit mismatch) — and
// neither does an asset/amount mismatch against the withdrawal record.
func (k Keeper) ValidateWithdrawalProcessedAcceptance(ctx context.Context, msg *types.MsgAttestWithdrawalPayout) error {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	if !params.BridgeOutEnabled {
		return types.ErrBridgeOutDisabled
	}
	if err := params.CheckSteemBlock(msg.SteemBlock); err != nil {
		return err
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
	if withdrawal.Status == types.WithdrawalStatus_WITHDRAWAL_STATUS_REFUNDED {
		return types.ErrWithdrawalAlreadyRefunded
	}
	already, err := k.WithdrawalConfirmedBy.Has(ctx, collections.Join(msg.WithdrawalId, validatorAddr))
	if err != nil {
		return err
	}
	if already {
		return types.ErrDuplicateWithdrawalConfirmation
	}
	// Payout txid/op-index are tallied per candidate, so a value differing from
	// one another validator already recorded is NOT a rejection — it is a valid
	// attestation for a different candidate and stays fee-exempt. Only a
	// mismatch against the withdrawal's own record is rejected (and so falls
	// back to the fee-paying path).
	if !payoutMatches(withdrawal, msg) {
		return types.ErrWithdrawalPayoutMismatch
	}
	return nil
}

// payoutMatches reports whether an attested Steem transfer is the payout the
// withdrawal asked for: the same amount and asset, to the same account, with
// the same memo. Anything else must not mark the withdrawal paid, or it would
// cancel the refund of a user who never got their funds.
func payoutMatches(withdrawal types.Withdrawal, msg *types.MsgAttestWithdrawalPayout) bool {
	return withdrawal.AmountMillisteem == msg.AmountMillisteem &&
		withdrawal.Asset == msg.Asset &&
		withdrawal.DestinationSteemAccount == msg.DestinationSteemAccount &&
		withdrawal.Memo == msg.Memo
}
