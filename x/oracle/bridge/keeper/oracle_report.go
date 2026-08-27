package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

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
