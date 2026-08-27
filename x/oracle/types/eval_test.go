package types

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"
)

func d(s string) math.LegacyDec { return math.LegacyMustNewDecFromStr(s) }

// Default §2b params used across the scenarios.
var (
	wPrice   = d("0.5")
	wBridge  = d("0.5")
	minValid = d("0.5")
	floor    = d("0") // a validator that attested NONE of an active duty (rate 0) is slashed
)

func eval(price, bridge DutyTally) WindowResult {
	return EvaluateValidatorWindow(price, bridge, wPrice, wBridge, minValid, floor)
}

func TestEval_NoActiveDuties_NotJudged(t *testing.T) {
	r := eval(DutyTally{}, DutyTally{})
	require.False(t, r.Judged)
	require.False(t, r.ShouldSlash)
}

func TestEval_BothPerfect_NotSlashed(t *testing.T) {
	r := eval(DutyTally{Opportunities: 100, Hits: 100}, DutyTally{Opportunities: 10, Hits: 10})
	require.True(t, r.Judged)
	require.True(t, r.Score.Equal(math.LegacyOneDec()))
	require.False(t, r.ShouldSlash)
}

// The load-bearing anti-masking case: perfect price voting cannot hide a total
// absence from an ACTIVE bridge duty — DutyFloor slashes it regardless of score.
func TestEval_PerfectPrice_ZeroBridge_SlashedByDutyFloor(t *testing.T) {
	r := eval(DutyTally{Opportunities: 100, Hits: 100}, DutyTally{Opportunities: 10, Hits: 0})
	require.True(t, r.ShouldSlash)
	require.Equal(t, ReasonBridgeDutyFloor, r.Reason)
	// Combined score is a healthy 0.5, yet the floor still fires.
	require.True(t, r.Score.Equal(d("0.5")))
}

func TestEval_PerfectBridge_ZeroPrice_SlashedByDutyFloor(t *testing.T) {
	r := eval(DutyTally{Opportunities: 100, Hits: 0}, DutyTally{Opportunities: 10, Hits: 10})
	require.True(t, r.ShouldSlash)
	require.Equal(t, ReasonPriceDutyFloor, r.Reason)
}

// An honest node that missed a single sporadic bridge event out of many is NOT
// slashed: its bridge rate is high and its combined score stays above MinValid.
func TestEval_HonestSingleMiss_NotSlashed(t *testing.T) {
	r := eval(DutyTally{Opportunities: 100, Hits: 100}, DutyTally{Opportunities: 20, Hits: 19})
	require.False(t, r.ShouldSlash)
}

// Only one duty active this window: judged on that duty alone (weight renormalized).
func TestEval_OnlyPriceActive_JudgedOnPrice(t *testing.T) {
	// price rate 0.4 < MinValid 0.5 -> slashed on the single active duty.
	r := eval(DutyTally{Opportunities: 100, Hits: 40}, DutyTally{})
	require.True(t, r.Judged)
	require.True(t, r.Score.Equal(d("0.4")))
	require.True(t, r.ShouldSlash)
	require.Equal(t, ReasonBelowMinValid, r.Reason)

	// price rate 0.6 with no bridge activity -> fine.
	r = eval(DutyTally{Opportunities: 100, Hits: 60}, DutyTally{})
	require.False(t, r.ShouldSlash)
}

// Below MinValid via the weighted average (both duties active, both above floor).
func TestEval_BelowMinValid_Slashed(t *testing.T) {
	// price 0.4, bridge 0.5 -> score 0.45 < 0.5, neither at floor(0).
	r := eval(DutyTally{Opportunities: 100, Hits: 40}, DutyTally{Opportunities: 100, Hits: 50})
	require.True(t, r.ShouldSlash)
	require.Equal(t, ReasonBelowMinValid, r.Reason)
	require.True(t, r.Score.Equal(d("0.45")))
}

// A non-zero DutyFloor slashes a low-but-nonzero active duty even when the
// combined score clears MinValid.
func TestEval_NonZeroDutyFloor(t *testing.T) {
	// bridge rate 0.05 <= floor 0.10, though combined score 0.525 > MinValid.
	r := EvaluateValidatorWindow(
		DutyTally{Opportunities: 100, Hits: 100}, DutyTally{Opportunities: 100, Hits: 5},
		wPrice, wBridge, minValid, d("0.10"),
	)
	require.True(t, r.ShouldSlash)
	require.Equal(t, ReasonBridgeDutyFloor, r.Reason)
}

// Asymmetric weights renormalize correctly when only one duty is active.
func TestEval_AsymmetricWeights(t *testing.T) {
	// Heavily weight bridge, but only price is active -> judged purely on price.
	r := EvaluateValidatorWindow(
		DutyTally{Opportunities: 10, Hits: 7}, DutyTally{},
		d("0.1"), d("0.9"), minValid, floor,
	)
	require.True(t, r.Score.Equal(d("0.7")))
	require.False(t, r.ShouldSlash)
}

// Degenerate zero-weight params fall back to an unweighted mean (no divide-by-zero).
func TestEval_ZeroWeightsFallBackToMean(t *testing.T) {
	r := EvaluateValidatorWindow(
		DutyTally{Opportunities: 100, Hits: 60}, DutyTally{Opportunities: 100, Hits: 80},
		math.LegacyZeroDec(), math.LegacyZeroDec(), minValid, floor,
	)
	require.True(t, r.Judged)
	require.True(t, r.Score.Equal(d("0.7")), "mean of 0.6 and 0.8")
	require.False(t, r.ShouldSlash)
}
