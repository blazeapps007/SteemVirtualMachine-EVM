package keeper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"steemvm/x/steembridge/types"
)

// lookupDepositByTxid resolves the (txid, opIndex) dedup key to its full
// Deposit record, if one has ever been created for that key.
func (k Keeper) lookupDepositByTxid(ctx context.Context, txid string, opIndex uint32) (deposit types.Deposit, found bool, err error) {
	id, err := k.DepositByTxid.Get(ctx, collections.Join(txid, opIndex))
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return types.Deposit{}, false, nil
		}
		return types.Deposit{}, false, err
	}
	deposit, err = k.Deposit.Get(ctx, id)
	if err != nil {
		return types.Deposit{}, false, err
	}
	return deposit, true, nil
}

// matchesPending reports whether a new submission's raw facts exactly match
// an existing pending deposit's stored raw facts. (txid, opIndex) are equal
// by construction since existing was looked up by that same key.
func matchesPending(existing types.Deposit, msg *types.MsgAttestDeposit) bool {
	return existing.SteemBlock == msg.SteemBlock &&
		existing.SteemTimestamp == msg.SteemTimestamp &&
		existing.SteemSender == msg.SteemSender &&
		existing.GatewayAccount == msg.GatewayAccount &&
		existing.AmountMillisteem == msg.AmountMillisteem &&
		existing.Memo == msg.Memo &&
		existing.Asset == msg.Asset
}

// ValidateDepositAcceptance performs the read-only, stateful checks that
// decide whether a MsgAttestDeposit submission would be accepted by
// AttestDeposit: the bridge must be enabled, the gateway account must
// match, this validator must not have already confirmed this key, the key
// must not already be resolved (minted or unclaimable), and if a pending
// deposit already exists its raw facts must exactly match this submission.
//
// This is the single source of truth shared by both AttestDeposit
// itself and the ante handler's fee-exemption decorator (app/ante_steembridge.go),
// so the two can never drift on what counts as "would be accepted."
// It performs no state mutation.
func (k Keeper) ValidateDepositAcceptance(ctx context.Context, msg *types.MsgAttestDeposit) error {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return err
	}
	if !params.BridgeEnabled {
		return types.ErrBridgeDisabled
	}
	if msg.GatewayAccount != params.GatewayAccount {
		return types.ErrInvalidGatewayAccount
	}

	validatorAddr, err := k.addressCodec.StringToBytes(msg.Validator)
	if err != nil {
		return err
	}

	alreadyConfirmed, err := k.DepositConfirmedBy.Has(ctx, collections.Join3(msg.Txid, msg.OpIndex, validatorAddr))
	if err != nil {
		return err
	}
	if alreadyConfirmed {
		return types.ErrDuplicateConfirmation
	}

	existing, found, err := k.lookupDepositByTxid(ctx, msg.Txid, msg.OpIndex)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}

	switch existing.Status {
	case types.DepositStatus_DEPOSIT_STATUS_MINTED, types.DepositStatus_DEPOSIT_STATUS_UNCLAIMABLE:
		return types.ErrDepositAlreadyMinted
	}

	if !matchesPending(existing, msg) {
		return types.ErrDepositMismatch
	}

	return nil
}

// setDepositStatus moves a deposit between status index buckets and updates
// its in-memory Status field. Callers persist the record via k.Deposit.Set.
func (k Keeper) setDepositStatus(ctx context.Context, deposit *types.Deposit, newStatus types.DepositStatus) error {
	if err := k.DepositByStatus.Remove(ctx, collections.Join(int32(deposit.Status), deposit.Id)); err != nil {
		return err
	}
	deposit.Status = newStatus
	return k.DepositByStatus.Set(ctx, collections.Join(int32(newStatus), deposit.Id))
}

// resolveDeposit is called exactly once per deposit, the moment its
// confirmed voting power ratio first reaches the threshold. It derives the
// destination from the memo (in consensus, never trusting the client),
// enforces the min/max bridge amount policy, and either mints or marks the
// deposit UNCLAIMABLE. Mutates deposit in place; the caller persists it.
func (k Keeper) resolveDeposit(ctx context.Context, deposit *types.Deposit, params types.Params) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	destAddr, destType, ok := types.DeriveDestination(deposit.Memo)

	withinRange := true
	if params.MinimumBridgeAmount != 0 && deposit.AmountMillisteem < params.MinimumBridgeAmount {
		withinRange = false
	}
	if params.MaximumBridgeAmount != 0 && deposit.AmountMillisteem > params.MaximumBridgeAmount {
		withinRange = false
	}

	if !ok || !withinRange {
		deposit.DestinationType = destType
		if err := k.setDepositStatus(ctx, deposit, types.DepositStatus_DEPOSIT_STATUS_UNCLAIMABLE); err != nil {
			return err
		}
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
			types.EventTypeDepositUnclaimable,
			sdk.NewAttribute(types.AttributeKeyDepositID, strconv.FormatUint(deposit.Id, 10)),
		))
		return nil
	}

	mintAmount, err := k.creditBridged(ctx, destAddr, deposit.Asset, deposit.AmountMillisteem, params.BridgeFeeBps)
	if err != nil {
		return err
	}

	destAddrStr, err := k.addressCodec.BytesToString(destAddr)
	if err != nil {
		return err
	}

	deposit.DerivedDestination = destAddrStr
	deposit.DestinationType = destType
	deposit.Minted = true
	deposit.MintedAt = uint64(sdkCtx.BlockHeight())
	deposit.MintTxHash = txHashHex(sdkCtx)
	if err := k.setDepositStatus(ctx, deposit, types.DepositStatus_DEPOSIT_STATUS_MINTED); err != nil {
		return err
	}

	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeDepositMinted,
		sdk.NewAttribute(types.AttributeKeyDepositID, strconv.FormatUint(deposit.Id, 10)),
		sdk.NewAttribute(types.AttributeKeyDestination, destAddrStr),
		sdk.NewAttribute(types.AttributeKeyAmount, mintAmount.String()),
	))
	return nil
}

// creditBridged is the single place bridged STEEM/SBD enters circulation. Per the
// "virtual transaction" model it mints the FULL gross amount into the
// STEEMBLACKHOLE module account, then sends the net (gross - fee) to destAddr — so
// on-chain the deposit reads as a transfer *from* the black hole, not a bare mint —
// and routes the fee to the bridge_reward account (100% staking reward, §4b). The
// black hole nets to zero (all minted is sent out). Name-registration resolution
// also goes through here with feeBps=0 and STEEM, so the paths can't drift. Returns
// the net amount credited to destAddr for event emission. The bank balance change
// reaches the EVM stateDB automatically via the balance handler.
func (k Keeper) creditBridged(ctx context.Context, destAddr sdk.AccAddress, asset types.BridgeAsset, grossMilli uint64, feeBps uint32) (math.Int, error) {
	denom := types.DenomForAsset(asset)
	netMilli, feeMilli := types.ApplyBridgeFee(grossMilli, feeBps)

	grossAmt := types.MillisteemToAsteem(grossMilli)
	netAmt := types.MillisteemToAsteem(netMilli)
	feeAmt := types.MillisteemToAsteem(feeMilli)

	if err := k.bankKeeper.MintCoins(ctx, types.BlackHoleModuleName, sdk.NewCoins(sdk.NewCoin(denom, grossAmt))); err != nil {
		return math.Int{}, err
	}
	if netAmt.IsPositive() {
		if err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.BlackHoleModuleName, destAddr, sdk.NewCoins(sdk.NewCoin(denom, netAmt))); err != nil {
			return math.Int{}, err
		}
	}
	if feeAmt.IsPositive() {
		if err := k.bankKeeper.SendCoinsFromModuleToModule(ctx, types.BlackHoleModuleName, types.BridgeRewardModuleName, sdk.NewCoins(sdk.NewCoin(denom, feeAmt))); err != nil {
			return math.Int{}, err
		}
	}

	totals, err := k.Totals.Get(ctx)
	if err != nil {
		return math.Int{}, err
	}
	if asset == types.BridgeAsset_BRIDGE_ASSET_SBD {
		totals.TotalMintedAsbd = totals.TotalMintedAsbd.Add(grossAmt)
	} else {
		totals.TotalMintedAsteem = totals.TotalMintedAsteem.Add(grossAmt)
	}
	if err := k.Totals.Set(ctx, totals); err != nil {
		return math.Int{}, err
	}
	return netAmt, nil
}

// txHashHex derives an audit-trail reference for the current transaction.
// The mint happens within this same tx (there is no separate "mint tx"), so
// this is the standard sha256-of-tx-bytes convention used elsewhere in the
// SDK ecosystem to give on-chain records a stable tx reference.
func txHashHex(ctx sdk.Context) string {
	hash := sha256.Sum256(ctx.TxBytes())
	return hex.EncodeToString(hash[:])
}
