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

// AttestDeposit implements the module's core bridge-in flow: verify the
// signer is a bonded validator, dedup on (txid, opIndex), record or match
// against the pending deposit, accumulate live bonded voting power, and mint
// once the confirmation threshold is first reached. See the module design
// note on ValidateDepositAcceptance for why the acceptance checks live there
// and are called from here rather than duplicated.
func (k msgServer) AttestDeposit(ctx context.Context, msg *types.MsgAttestDeposit) (*types.MsgAttestDepositResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	params, err := k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}
	if !params.BridgeEnabled {
		return nil, types.ErrBridgeDisabled
	}
	if msg.GatewayAccount != types.GatewayAccount {
		return nil, types.ErrInvalidGatewayAccount
	}

	validatorAddr, err := k.addressCodec.StringToBytes(msg.Validator)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid validator address")
	}

	validator, err := k.stakingKeeper.GetValidator(ctx, sdk.ValAddress(validatorAddr))
	if err != nil || !validator.IsBonded() {
		return nil, types.ErrNotBondedValidator
	}

	confirmedKey := collections.Join3(msg.Txid, msg.OpIndex, validatorAddr)
	alreadyConfirmed, err := k.DepositConfirmedBy.Has(ctx, confirmedKey)
	if err != nil {
		return nil, err
	}
	if alreadyConfirmed {
		// Benign no-op: this validator already confirmed this key (a same-block
		// or retried resubmission). Not an error, so the redundant zero-fee
		// attestation tx succeeds rather than failing.
		return &types.MsgAttestDepositResponse{}, nil
	}

	dedupKey := collections.Join(msg.Txid, msg.OpIndex)
	depositID, err := k.DepositByTxid.Get(ctx, dedupKey)
	isNew := errors.Is(err, collections.ErrNotFound)
	if err != nil && !isNew {
		return nil, err
	}

	var deposit types.Deposit
	if isNew {
		depositID, err = k.DepositSeq.Next(ctx)
		if err != nil {
			return nil, err
		}

		deposit = types.Deposit{
			Id:               depositID,
			Txid:             msg.Txid,
			OpIndex:          msg.OpIndex,
			SteemBlock:       msg.SteemBlock,
			SteemTimestamp:   msg.SteemTimestamp,
			SteemSender:      msg.SteemSender,
			GatewayAccount:   msg.GatewayAccount,
			AmountMillisteem: msg.AmountMillisteem,
			Memo:             msg.Memo,
			Asset:            msg.Asset,
			Status:           types.DepositStatus_DEPOSIT_STATUS_PENDING,
			CreatedAt:        uint64(sdkCtx.BlockHeight()),
		}
		if err := k.DepositByTxid.Set(ctx, dedupKey, depositID); err != nil {
			return nil, err
		}
		if err := k.DepositByStatus.Set(ctx, collections.Join(int32(types.DepositStatus_DEPOSIT_STATUS_PENDING), depositID)); err != nil {
			return nil, err
		}

		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
			types.EventTypeDepositCreated,
			sdk.NewAttribute(types.AttributeKeyDepositID, strconv.FormatUint(depositID, 10)),
			sdk.NewAttribute(types.AttributeKeyTxid, msg.Txid),
			sdk.NewAttribute(types.AttributeKeyOpIndex, strconv.FormatUint(uint64(msg.OpIndex), 10)),
			sdk.NewAttribute(types.AttributeKeyAsset, types.DenomForAsset(msg.Asset)),
		))
	} else {
		deposit, err = k.Deposit.Get(ctx, depositID)
		if err != nil {
			return nil, err
		}

		// The deposit may already be resolved (MINTED or UNCLAIMABLE). We do NOT
		// stop here: attestations that arrive after the threshold was crossed
		// are still recorded below, so ValidatorConfirmations captures the full
		// set of validators that attested. Re-resolution is prevented by the
		// PENDING status guard further down.

		if !matchesPending(deposit, msg) {
			// This attestation reports different facts than the record's
			// current provisional ones (the first attester's). It is NOT
			// rejected: it votes for its own fact-set candidate below and may
			// yet win the tally. The event is purely informational for
			// auditing which facts differ.
			sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
				types.EventTypeDepositConfirmationMismatch,
				sdk.NewAttribute(types.AttributeKeyDepositID, strconv.FormatUint(depositID, 10)),
				sdk.NewAttribute(types.AttributeKeyStoredSteemBlock, strconv.FormatUint(deposit.SteemBlock, 10)),
				sdk.NewAttribute(types.AttributeKeySubmittedSteemBlock, strconv.FormatUint(msg.SteemBlock, 10)),
				sdk.NewAttribute(types.AttributeKeyStoredTimestamp, deposit.SteemTimestamp),
				sdk.NewAttribute(types.AttributeKeySubmittedTimestamp, msg.SteemTimestamp),
				sdk.NewAttribute(types.AttributeKeyStoredSender, deposit.SteemSender),
				sdk.NewAttribute(types.AttributeKeySubmittedSender, msg.SteemSender),
				sdk.NewAttribute(types.AttributeKeyStoredGateway, deposit.GatewayAccount),
				sdk.NewAttribute(types.AttributeKeySubmittedGateway, msg.GatewayAccount),
				sdk.NewAttribute(types.AttributeKeyStoredAmount, strconv.FormatUint(deposit.AmountMillisteem, 10)),
				sdk.NewAttribute(types.AttributeKeySubmittedAmount, strconv.FormatUint(msg.AmountMillisteem, 10)),
				sdk.NewAttribute(types.AttributeKeyStoredMemo, deposit.Memo),
				sdk.NewAttribute(types.AttributeKeySubmittedMemo, msg.Memo),
			))
		}
	}

	if err := k.DepositConfirmedBy.Set(ctx, confirmedKey); err != nil {
		return nil, err
	}
	// Record this validator's vote for the exact fact-set it observed, and
	// tally per fact-set: the deposit resolves to whichever fact-set reaches
	// the threshold, so an attester reporting wrong facts (or front-running
	// with them) cannot block or redirect the honest majority.
	factsHash := types.DepositFactsHash(msg.SteemBlock, msg.SteemTimestamp, msg.SteemSender, msg.GatewayAccount, msg.AmountMillisteem, msg.Memo, msg.Asset)
	dedup := dedupKeyString(msg.Txid, msg.OpIndex)
	if err := k.DepositFactsVote.Set(ctx, collections.Join3(dedup, factsHash, validatorAddr)); err != nil {
		return nil, err
	}

	confirmingValidator := sdk.ValAddress(validatorAddr).String()
	deposit.ValidatorConfirmations = append(deposit.ValidatorConfirmations, &types.Confirmation{
		ValidatorAddress: confirmingValidator,
		Timestamp:        sdkCtx.BlockTime().Unix(),
	})

	// Live bonded power behind THIS attestation's fact-set.
	ratio, err := k.factsCandidateRatio(ctx, k.DepositFactsVote, dedup, factsHash)
	if err != nil {
		return nil, err
	}

	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeDepositConfirmed,
		sdk.NewAttribute(types.AttributeKeyDepositID, strconv.FormatUint(depositID, 10)),
		sdk.NewAttribute(types.AttributeKeyValidator, confirmingValidator),
		sdk.NewAttribute(types.AttributeKeyConfirmedRatio, ratio.String()),
	))

	// Resolve exactly once, the first time ONE fact-set crosses the threshold.
	// The winning fact-set is this message's facts — adopt them as the record's
	// canonical facts (overwriting any provisional facts a front-running
	// attester wrote) before minting.
	if deposit.Status == types.DepositStatus_DEPOSIT_STATUS_PENDING && ratio.GTE(params.BridgeConfirmationThreshold) {
		deposit.SteemBlock = msg.SteemBlock
		deposit.SteemTimestamp = msg.SteemTimestamp
		deposit.SteemSender = msg.SteemSender
		deposit.GatewayAccount = msg.GatewayAccount
		deposit.AmountMillisteem = msg.AmountMillisteem
		deposit.Memo = msg.Memo
		deposit.Asset = msg.Asset
		if err := k.resolveDeposit(ctx, &deposit, params); err != nil {
			return nil, err
		}
		// A finalized deposit is a bridge-duty opportunity. Queue it for the
		// EndBlocker to report, so every validator that attested this same
		// block — including ones whose attestation lands after this crossing
		// one — is credited, not just the set present at this instant.
		if err := k.enqueueBridgeDuty(ctx, bridgeDutyKindDeposit, deposit.Id); err != nil {
			return nil, err
		}
	}

	if err := k.Deposit.Set(ctx, depositID, deposit); err != nil {
		return nil, err
	}

	return &types.MsgAttestDepositResponse{}, nil
}
