package keeper

import (
	"context"
	"strconv"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"steemvm/x/steembridge/types"
)

// BridgeOut implements the module's bridge-out flow: burn asteem from the
// sender and create an immutable withdrawal record for validators to relay
// to Steem manually. Bridging out to "null" is the sanctioned provable-burn
// path (Steem destroys transfers to @null) and is deliberately handled with
// no special-cased branch: it is an ordinary withdrawal whose destination
// happens to be "null".
func (k msgServer) BridgeOut(ctx context.Context, msg *types.MsgBridgeOut) (*types.MsgBridgeOutResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	senderAddr, err := k.addressCodec.StringToBytes(msg.Sender)
	if err != nil {
		return nil, errorsmod.Wrap(err, "invalid sender address")
	}

	params, err := k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}
	if !params.BridgeOutEnabled {
		return nil, types.ErrBridgeOutDisabled
	}

	if err := types.ValidateSteemAccountName(msg.DestinationSteemAccount); err != nil {
		return nil, err
	}

	if msg.AmountAsteem.IsNil() || !msg.AmountAsteem.IsPositive() {
		return nil, errorsmod.Wrap(types.ErrInvalidAmount, "amount must be positive")
	}
	if !msg.AmountAsteem.Mod(types.MillisteemToAsteemFactor).IsZero() {
		return nil, errorsmod.Wrap(types.ErrInvalidAmount, "amount must be a whole multiple of 10^15 asteem (one millisteem)")
	}

	coins := sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, msg.AmountAsteem))
	if err := k.bankKeeper.SendCoinsFromAccountToModule(ctx, senderAddr, types.ModuleName, coins); err != nil {
		return nil, err
	}
	if err := k.bankKeeper.BurnCoins(ctx, types.ModuleName, coins); err != nil {
		return nil, err
	}

	totals, err := k.Totals.Get(ctx)
	if err != nil {
		return nil, err
	}
	totals.TotalBurnedAsteem = totals.TotalBurnedAsteem.Add(msg.AmountAsteem)
	if err := k.Totals.Set(ctx, totals); err != nil {
		return nil, err
	}

	withdrawalID, err := k.WithdrawalSeq.Next(ctx)
	if err != nil {
		return nil, err
	}

	withdrawal := types.Withdrawal{
		Id:                      withdrawalID,
		Sender:                  msg.Sender,
		DestinationSteemAccount: msg.DestinationSteemAccount,
		AmountAsteem:            msg.AmountAsteem,
		AmountMillisteem:        msg.AmountAsteem.Quo(types.MillisteemToAsteemFactor).Uint64(),
		Memo:                    msg.Memo,
		BurnTxHash:              txHashHex(sdkCtx),
		Status:                  types.WithdrawalStatus_WITHDRAWAL_STATUS_PENDING,
		CreatedAt:               uint64(sdkCtx.BlockHeight()),
	}

	if err := k.Withdrawal.Set(ctx, withdrawalID, withdrawal); err != nil {
		return nil, err
	}
	if err := k.WithdrawalByStatus.Set(ctx, collections.Join(int32(types.WithdrawalStatus_WITHDRAWAL_STATUS_PENDING), withdrawalID)); err != nil {
		return nil, err
	}

	sdkCtx.EventManager().EmitEvents(sdk.Events{
		sdk.NewEvent(
			types.EventTypeWithdrawalCreated,
			sdk.NewAttribute(types.AttributeKeyWithdrawalID, strconv.FormatUint(withdrawalID, 10)),
			sdk.NewAttribute(types.AttributeKeyDestination, msg.DestinationSteemAccount),
			sdk.NewAttribute(types.AttributeKeyAmount, msg.AmountAsteem.String()),
		),
		sdk.NewEvent(
			types.EventTypeWithdrawalBurned,
			sdk.NewAttribute(types.AttributeKeyWithdrawalID, strconv.FormatUint(withdrawalID, 10)),
			sdk.NewAttribute(types.AttributeKeyAmount, msg.AmountAsteem.String()),
		),
	})

	return &types.MsgBridgeOutResponse{}, nil
}
