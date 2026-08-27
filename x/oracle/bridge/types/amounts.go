package types

import (
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// DenomSBD is the base denom of bridged SBD (18 decimals, display "sbd").
// Bridged STEEM uses sdk.DefaultBondDenom ("asteem").
const DenomSBD = "asbd"

// MillisteemToAsteemFactor is 10^15: millisteem is STEEM's own 10^-3 unit,
// asteem is this chain's 10^-18 unit, so converting between them scales by
// 10^(18-3) = 10^15. SBD is also 3-dec on Steem, so the same factor applies to
// millisbd -> asbd.
var MillisteemToAsteemFactor = math.NewInt(1_000_000_000_000_000)

// MillisteemToAsteem converts an integer millisteem (or millisbd) amount to the
// chain's 18-decimal base unit. This MUST be done in math.Int, never native
// uint64 multiplication: uint64 overflows for any deposit above roughly 18 STEEM
// (10^15 scaled by even a modest number of millisteem quickly exceeds uint64's
// ~1.8x10^19 max).
func MillisteemToAsteem(amountMillisteem uint64) math.Int {
	return math.NewIntFromUint64(amountMillisteem).Mul(MillisteemToAsteemFactor)
}

// DenomForAsset maps a BridgeAsset to its on-chain base denom.
func DenomForAsset(asset BridgeAsset) string {
	if asset == BridgeAsset_BRIDGE_ASSET_SBD {
		return DenomSBD
	}
	return sdk.DefaultBondDenom
}

// ApplyBridgeFee splits a gross millisteem amount into the net payable amount and
// the withheld bridge fee, using feeBps basis points (25 = 0.25%). Integer floor;
// any rounding remainder stays with net. Consensus-safe integer math — millisteem
// amounts are bounded well under the uint64 overflow point for the gross*bps step.
func ApplyBridgeFee(grossMilli uint64, feeBps uint32) (net uint64, fee uint64) {
	if feeBps == 0 {
		return grossMilli, 0
	}
	if feeBps >= 10000 {
		return 0, grossMilli
	}
	fee = grossMilli * uint64(feeBps) / 10000
	net = grossMilli - fee
	return net, fee
}
