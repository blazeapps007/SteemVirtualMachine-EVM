package types

import "cosmossdk.io/math"

// MillisteemToAsteemFactor is 10^15: millisteem is STEEM's own 10^-3 unit,
// asteem is this chain's 10^-18 unit, so converting between them scales by
// 10^(18-3) = 10^15.
var MillisteemToAsteemFactor = math.NewInt(1_000_000_000_000_000)

// MillisteemToAsteem converts an integer millisteem amount to asteem.
// This MUST be done in math.Int, never native uint64 multiplication:
// uint64 overflows for any deposit above roughly 18 STEEM (10^15 scaled by
// even a modest number of millisteem quickly exceeds uint64's ~1.8x10^19 max).
func MillisteemToAsteem(amountMillisteem uint64) math.Int {
	return math.NewIntFromUint64(amountMillisteem).Mul(MillisteemToAsteemFactor)
}
