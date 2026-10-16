package types_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"steemvm/x/oracle/bridge/types"
)

func TestParamsValidate_SteemAnchor(t *testing.T) {
	const block = 110435764
	id := fmt.Sprintf("%08x", block) + strings.Repeat("ab", 16)
	cases := []struct {
		name    string
		block   uint64
		id      string
		wantErr bool
	}{
		{"no anchor", 0, "", false},
		{"block without a recorded id", block, "", false},
		{"block and its id", block, id, false},
		{"id of a different block", block + 1, id, true},
		{"id without a block", 0, id, true},
		{"uppercase id", block, strings.ToUpper(id), true},
		{"short id", block, id[:39], true},
		{"non-hex id", block, id[:39] + "z", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := types.DefaultParams()
			p.RelayerStartBlock = tc.block
			p.RelayerStartBlockId = tc.id
			if err := p.Validate(); tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestParamsCheckSteemBlock(t *testing.T) {
	p := types.DefaultParams()
	require.NoError(t, p.CheckSteemBlock(1), "no anchor: nothing rejected")
	p.RelayerStartBlock = 500
	require.ErrorIs(t, p.CheckSteemBlock(499), types.ErrSteemBlockBeforeAnchor)
	require.NoError(t, p.CheckSteemBlock(500))
	require.NoError(t, p.CheckSteemBlock(501))
}
