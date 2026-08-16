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

// duty selects which pair of tally counters a report advances.
type duty int

const (
	dutyPrice duty = iota
	dutyBridge
)

// RecordPriceParticipation records one price vote period against the current
// slash window: every BONDED validator gains one price OPPORTUNITY, and those
// in `voters` (the in-band voters this period) gain one price HIT. `voters` are
// validator operator addresses (bytes). This is the only entry point
// x/oracle/data uses to feed the unified counter — the parent module never
// imports the duty modules (avoids an import cycle; see plan §2b).
func (k Keeper) RecordPriceParticipation(ctx context.Context, voters [][]byte) error {
	return k.recordDuty(ctx, voters, dutyPrice)
}

// RecordBridgeEvent records one finalized (2/3-confirmed) bridge event against
// the current slash window: every bonded validator gains one bridge
// OPPORTUNITY, and each attester in `attesters` gains one bridge HIT.
// x/oracle/bridge calls this at each deposit/withdrawal finalization.
func (k Keeper) RecordBridgeEvent(ctx context.Context, attesters [][]byte) error {
	return k.recordDuty(ctx, attesters, dutyBridge)
}

// recordDuty is the shared accrual: the bonded set is the opportunity set (the
// parent module owns this so the duty modules only report who participated), and
// participants who are currently bonded score a hit. A participant that is not
// in the bonded set carries no weight and is ignored — matching the live-power
// convention the duty tallies use elsewhere.
func (k Keeper) recordDuty(ctx context.Context, participants [][]byte, d duty) error {
	bonded, err := k.stakingKeeper.GetBondedValidatorsByPower(ctx)
	if err != nil {
		return err
	}
	if len(bonded) == 0 {
		return nil
	}
	valCodec := k.stakingKeeper.ValidatorAddressCodec()
	did := toSet(participants)

	for _, v := range bonded {
		valoper, err := valCodec.StringToBytes(v.GetOperator())
		if err != nil {
			return err
		}
		t, err := k.getTally(ctx, valoper)
		if err != nil {
			return err
		}
		switch d {
		case dutyPrice:
			t.PriceOpportunities++
			if did[string(valoper)] {
				t.PriceHits++
			}
		case dutyBridge:
			t.BridgeOpportunities++
			if did[string(valoper)] {
				t.BridgeHits++
			}
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
