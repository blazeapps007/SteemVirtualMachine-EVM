package types_test

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	"steemvm/x/steembridge/types"
)

func TestMillisteemToAsteem(t *testing.T) {
	largeWant, ok := math.NewIntFromString("1000000000000000000000000")
	require.True(t, ok)

	tests := []struct {
		desc             string
		amountMillisteem uint64
		want             math.Int
	}{
		{desc: "zero", amountMillisteem: 0, want: math.ZeroInt()},
		{desc: "one millisteem", amountMillisteem: 1, want: math.NewInt(1_000_000_000_000_000)},
		{
			desc:             "large amount that would overflow uint64 if multiplied natively",
			amountMillisteem: 1_000_000_000, // 1,000,000 STEEM in millisteem
			// 10^9 * 10^15 = 10^24, far beyond uint64's ~1.8x10^19 max - this
			// must be computed via math.Int, never native uint64 multiplication.
			want: largeWant,
		},
		{
			desc:             "max uint64 millisteem does not panic or wrap",
			amountMillisteem: 18_446_744_073_709_551_615, // math.MaxUint64
			want:             math.NewIntFromUint64(18_446_744_073_709_551_615).Mul(math.NewInt(1_000_000_000_000_000)),
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			got := types.MillisteemToAsteem(tc.amountMillisteem)
			require.True(t, tc.want.Equal(got), "want %s got %s", tc.want, got)
		})
	}
}
