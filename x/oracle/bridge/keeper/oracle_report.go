package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"steemvm/x/oracle/bridge/types"
)

// Bridge-duty queue kinds (the Pair's first element).
const (
	bridgeDutyKindDeposit    int32 = 0
	bridgeDutyKindWithdrawal int32 = 1
)

// enqueueBridgeDuty marks a just-finalized bridge event so the module
// EndBlocker reports its full same-block attester set exactly once. Enqueuing
// the same (kind, id) twice in a block is harmless: the queue is a set and the
// EndBlocker loads the attesters fresh at drain time.
func (k Keeper) enqueueBridgeDuty(ctx context.Context, kind int32, id uint64) error {
	return k.BridgeDutyQueue.Set(ctx, collections.Join(kind, id))
}

// FlushBridgeDuty drains the per-block bridge-duty queue: for each bridge
// event that finalized this block it reports the event's complete recorded
// attester set (all confirmations present by end of block, not just those
// present at the instant the 2/3 threshold was crossed) to the unified
// slashing engine as a single duty opportunity, then clears the queue. Called
// from the module EndBlocker.
func (k Keeper) FlushBridgeDuty(ctx context.Context) error {
	type item struct {
		kind int32
		id   uint64
	}
	var items []item
	if err := k.BridgeDutyQueue.Walk(ctx, nil, func(key collections.Pair[int32, uint64]) (bool, error) {
		items = append(items, item{kind: key.K1(), id: key.K2()})
		return false, nil
	}); err != nil {
		return err
	}

	for _, it := range items {
		var confirmations []*types.Confirmation
		switch it.kind {
		case bridgeDutyKindDeposit:
			d, err := k.Deposit.Get(ctx, it.id)
			if err != nil {
				return err
			}
			confirmations = d.ValidatorConfirmations
		case bridgeDutyKindWithdrawal:
			w, err := k.Withdrawal.Get(ctx, it.id)
			if err != nil {
				return err
			}
			confirmations = w.ValidatorConfirmations
		default:
			continue
		}
		attesters := make([]string, 0, len(confirmations))
		for _, c := range confirmations {
			attesters = append(attesters, c.ValidatorAddress)
		}
		if err := k.reportBridgeEvent(ctx, attesters); err != nil {
			return err
		}
	}

	return k.BridgeDutyQueue.Clear(ctx, nil)
}

// reportBridgeEvent tells the parent x/oracle engine which validators attested a
// just-finalized (2/3-confirmed) bridge event, so it can accrue the bridge duty
// of the unified slashing counter (plan §2b). attesterStrs are validator
// OPERATOR addresses (valoper bech32, as stored on the Confirmation records);
// they decode to the same bytes the parent keys its tally by. It is a no-op when
// the parent keeper is not wired (standalone/tests).
//
// NOTE: only attesters present AT finalization are reported. Late-but-honest
// attestations that arrive within the plan's grace window (Params.BridgeGraceBlocks
// on the parent) are recorded on the Deposit/Withdrawal audit list but are not
// yet counted as hits here — a v1 simplification to avoid a deferred-evaluation
// pass; the grace window is a follow-up refinement.
func (k Keeper) reportBridgeEvent(ctx context.Context, attesterStrs []string) error {
	if k.oracleKeeper == nil {
		return nil
	}
	attesters := make([][]byte, 0, len(attesterStrs))
	for _, s := range attesterStrs {
		valAddr, err := sdk.ValAddressFromBech32(s)
		if err != nil {
			continue
		}
		attesters = append(attesters, valAddr.Bytes())
	}
	return k.oracleKeeper.RecordBridgeEvent(ctx, attesters)
}
