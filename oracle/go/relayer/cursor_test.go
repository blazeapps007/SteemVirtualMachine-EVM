package relayer

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitialCursor(t *testing.T) {
	const lib = 107800000

	tests := []struct {
		desc       string
		localStart uint64
		chainStart uint64
		want       uint64
	}{
		{
			desc:       "local override wins over chain anchor and LIB",
			localStart: 107796360,
			chainStart: 107796345,
			want:       107796359, // scanning begins at cursor+1 = the start block itself
		},
		{
			desc:       "chain anchor used when no local override",
			localStart: 0,
			chainStart: 107796345,
			want:       107796344,
		},
		{
			desc:       "LIB fallback when both are unset",
			localStart: 0,
			chainStart: 0,
			want:       lib,
		},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			require.Equal(t, tc.want, initialCursor(tc.localStart, tc.chainStart, lib))
		})
	}
}
