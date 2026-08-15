package keeper

import (
	"context"

	"steemvm/x/oracle/types"
)

// getTally reads a validator's current-window tally, returning a zero tally if
// none is stored yet.
func (k Keeper) getTally(ctx context.Context, valoper []byte) (types.ValidatorTally, error) {
	t, err := k.Tally.Get(ctx, valoper)
	if err != nil {
		if isNotFound(err) {
			return types.ValidatorTally{}, nil
		}
		return types.ValidatorTally{}, err
	}
	return t, nil
}

// RecordPriceParticipation records one price vote period against the current
// slash window: every bonded validator gains one price OPPORTUNITY, and those
// in `voters` (the in-band voters this period) gain one price HIT. This is the
// only entry point x/oracle/data uses to feed the unified counter — the parent
// module never imports the duty modules (avoids an import cycle; see plan §2b).
func (k Keeper) RecordPriceParticipation(ctx context.Context, bonded [][]byte, voters [][]byte) error {
	voted := toSet(voters)
	for _, valoper := range bonded {
		t, err := k.getTally(ctx, valoper)
		if err != nil {
			return err
		}
		t.PriceOpportunities++
		if voted[string(valoper)] {
			t.PriceHits++
		}
		if err := k.Tally.Set(ctx, valoper, t); err != nil {
			return err
		}
	}
	return nil
}

// RecordBridgeEvent records one finalized (2/3-confirmed) bridge event against
// the current slash window: every validator bonded at finalization gains one
// bridge OPPORTUNITY, and each attester in `attesters` gains one bridge HIT.
// x/oracle/bridge calls this at each deposit/withdrawal finalization.
func (k Keeper) RecordBridgeEvent(ctx context.Context, bonded [][]byte, attesters [][]byte) error {
	attested := toSet(attesters)
	for _, valoper := range bonded {
		t, err := k.getTally(ctx, valoper)
		if err != nil {
			return err
		}
		t.BridgeOpportunities++
		if attested[string(valoper)] {
			t.BridgeHits++
		}
		if err := k.Tally.Set(ctx, valoper, t); err != nil {
			return err
		}
	}
	return nil
}

func toSet(addrs [][]byte) map[string]bool {
	set := make(map[string]bool, len(addrs))
	for _, a := range addrs {
		set[string(a)] = true
	}
	return set
}
