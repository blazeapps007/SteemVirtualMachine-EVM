// Package types holds the parent x/oracle engine's shared types. The parent
// module owns ONE unified per-validator performance counter that both oracle
// duty modules — x/oracle/data (price feeds) and x/oracle/bridge (Steem bridge
// attestations) — report into, and it slashes + jails on a combined score once
// per SlashWindow. This file is the pure evaluation core (no store, no keeper),
// so the plan-critical scoring math (§2b) is unit-testable in isolation.
package types

import "cosmossdk.io/math"

// DutyTally is a validator's participation in ONE duty over a slash window:
// how many opportunities it had (finalized bridge events / price vote periods,
// counted only while bonded) and how many it hit (attested / voted in-band).
// A duty with zero opportunities that window is INACTIVE and excluded from
// judgment, so a validator is never penalized for a duty that had nothing to do.
type DutyTally struct {
	Opportunities uint64
	Hits          uint64
}

// Active reports whether the duty had any opportunity this window.
func (d DutyTally) Active() bool { return d.Opportunities > 0 }

// Rate is hits/opportunities as a decimal in [0,1]; an inactive duty has no
// rate and returns zero (callers must gate on Active()).
func (d DutyTally) Rate() math.LegacyDec {
	if d.Opportunities == 0 {
		return math.LegacyZeroDec()
	}
	return math.LegacyNewDec(int64(d.Hits)).Quo(math.LegacyNewDec(int64(d.Opportunities)))
}

// WindowResult is the verdict for one validator over one slash window.
type WindowResult struct {
	// Judged is false when the validator had NO active duty this window (nothing
	// to score) — it is neither rewarded nor slashed.
	Judged bool
	// Score is the combined weighted participation rate over the active duties,
	// in [0,1]. Meaningful only when Judged.
	Score math.LegacyDec
	// ShouldSlash is the final decision: slash + jail this validator.
	ShouldSlash bool
	// Reason is a short machine-readable cause for the slash (for events/logs).
	Reason string
}

// Slash reason codes.
const (
	ReasonNone            = ""
	ReasonBelowMinValid   = "below_min_valid"   // combined score under MinValidPerWindow
	ReasonPriceDutyFloor  = "price_duty_floor"  // active price duty at/below DutyFloor
	ReasonBridgeDutyFloor = "bridge_duty_floor" // active bridge duty at/below DutyFloor
)

// EvaluateValidatorWindow applies the unified §2b rule to one validator's
// per-duty tallies:
//
//   - Each ACTIVE duty (opportunities > 0) contributes its hit rate. Inactive
//     duties are dropped, and the remaining duties' weights are renormalized so a
//     window with only one active duty judges on that duty alone.
//   - combined score = Σ(w_d · rate_d) / Σ(w_d) over active duties d.
//   - Slash if the combined score is below MinValidPerWindow, OR if ANY active
//     duty's rate is at or below DutyFloor. The DutyFloor is the anti-masking
//     guard: a validator that votes prices perfectly but attests NONE of an
//     active bridge duty (bridge rate 0 ≤ floor) is slashed regardless of how
//     high its combined score is — a naive pooled counter would hide this.
//
// Weights and thresholds are passed explicitly (the keeper reads them from
// gov-tunable params) so this core stays storeless and deterministic.
func EvaluateValidatorWindow(price, bridge DutyTally, wPrice, wBridge, minValidPerWindow, dutyFloor math.LegacyDec) WindowResult {
	if !price.Active() && !bridge.Active() {
		return WindowResult{Judged: false, Score: math.LegacyOneDec(), Reason: ReasonNone}
	}

	weightSum := math.LegacyZeroDec()
	weighted := math.LegacyZeroDec()
	if price.Active() {
		weightSum = weightSum.Add(wPrice)
		weighted = weighted.Add(wPrice.Mul(price.Rate()))
	}
	if bridge.Active() {
		weightSum = weightSum.Add(wBridge)
		weighted = weighted.Add(wBridge.Mul(bridge.Rate()))
	}

	// Guard against zero total weight (e.g. both weights configured 0): with no
	// usable weighting, fall back to an unweighted mean of the active rates so a
	// misconfiguration can't divide-by-zero or silently disable judgment.
	var score math.LegacyDec
	if weightSum.IsZero() {
		n := int64(0)
		sum := math.LegacyZeroDec()
		if price.Active() {
			sum = sum.Add(price.Rate())
			n++
		}
		if bridge.Active() {
			sum = sum.Add(bridge.Rate())
			n++
		}
		score = sum.Quo(math.LegacyNewDec(n))
	} else {
		score = weighted.Quo(weightSum)
	}

	result := WindowResult{Judged: true, Score: score, Reason: ReasonNone}

	switch {
	case price.Active() && price.Rate().LTE(dutyFloor):
		result.ShouldSlash = true
		result.Reason = ReasonPriceDutyFloor
	case bridge.Active() && bridge.Rate().LTE(dutyFloor):
		result.ShouldSlash = true
		result.Reason = ReasonBridgeDutyFloor
	case score.LT(minValidPerWindow):
		result.ShouldSlash = true
		result.Reason = ReasonBelowMinValid
	}

	return result
}
