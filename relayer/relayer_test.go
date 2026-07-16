package relayer

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"steemvm/x/steembridge/types"
)

func TestParseSteemAmount(t *testing.T) {
	tests := []struct {
		in     string
		want   uint64
		wantOK bool
	}{
		{"70.561 STEEM", 70561, true},
		{"0.001 STEEM", 1, true},
		{"1 STEEM", 1000, true},
		{"1.5 STEEM", 1500, true},
		{"5538.235 STEEM", 5538235, true},
		{"  1.000 STEEM  ", 1000, true},
		{"0.000 STEEM", 0, true}, // zero parses; the chain rejects it
		{"1.000 SBD", 0, false},
		{"1.000000 VESTS", 0, false},
		{"1.2345 STEEM", 0, false}, // more than 3 decimals
		{"STEEM", 0, false},
		{"", 0, false},
		{". STEEM", 0, false},
		{"-1.000 STEEM", 0, false},
		{"1,000.000 STEEM", 0, false},
		{"1.0e3 STEEM", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := ParseSteemAmount(tc.in)
			require.Equal(t, tc.wantOK, ok)
			if ok {
				require.Equal(t, tc.want, got)
			}
		})
	}
}

func TestRouteMemo(t *testing.T) {
	tests := []struct {
		memo string
		want Intent
	}{
		{"svm-register 0x1111111111111111111111111111111111111111", IntentRegister},
		{"  svm-register steem1abc  ", IntentRegister},
		{"svm-register", IntentRegister},
		{"svm-deposit 0x1111111111111111111111111111111111111111", IntentDeposit},
		{"0x1111111111111111111111111111111111111111", IntentDeposit},
		{"steem1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq", IntentDeposit},
		{"svm-registered-trademark", IntentDeposit}, // prefix must be a whole token
		{"random memo", IntentDeposit},
		{"", IntentDeposit},
	}
	for _, tc := range tests {
		t.Run(tc.memo, func(t *testing.T) {
			require.Equal(t, tc.want, RouteMemo(tc.memo))
		})
	}
}

func TestBuildMsg(t *testing.T) {
	validator := sdk.AccAddress(make([]byte, 20))
	transfer := Transfer{
		Txid:             "09fd1f259ad86b2ad021176da307af726b336e2c",
		OpIndex:          2,
		SteemBlock:       107791543,
		SteemTimestamp:   "2026-07-14T10:06:06",
		From:             "upex",
		AmountMillisteem: 70561,
		Memo:             "svm-register 0x1111111111111111111111111111111111111111",
	}

	msg := BuildMsg(transfer, IntentRegister, validator, "blaze.apps")
	reg, ok := msg.(*types.MsgSubmitNameRegistration)
	require.True(t, ok)
	require.Equal(t, validator.String(), reg.Validator)
	require.Equal(t, transfer.Txid, reg.Txid)
	require.Equal(t, transfer.OpIndex, reg.OpIndex)
	require.Equal(t, transfer.SteemBlock, reg.SteemBlock)
	require.Equal(t, transfer.SteemTimestamp, reg.SteemTimestamp)
	require.Equal(t, "upex", reg.SteemAccount)
	require.Equal(t, "blaze.apps", reg.GatewayAccount)
	require.Equal(t, transfer.AmountMillisteem, reg.AmountMillisteem)
	require.Equal(t, transfer.Memo, reg.Memo, "memo must pass through verbatim — the chain strips prefixes itself")

	msg = BuildMsg(transfer, IntentDeposit, validator, "blaze.apps")
	dep, ok := msg.(*types.MsgSubmitSteemDeposit)
	require.True(t, ok)
	require.Equal(t, "upex", dep.SteemSender)
	require.Equal(t, transfer.Memo, dep.Memo)
}

// blockFixture mirrors the condenser_api.get_block shape (as seen in the
// BridgeInfra probe output): a mix of transfer and non-transfer operations,
// multiple ops per tx, transfers to other accounts, and an SBD transfer.
const blockFixture = `{
  "timestamp": "2026-07-14T13:09:12",
  "witness": "kafio.wit",
  "transaction_ids": ["fallback00000000000000000000000000000000"],
  "transactions": [
    {
      "transaction_id": "d7fa48804a0f70cc0414e8938cf2d37404f0be7b",
      "operations": [
        ["vote", {"voter": "a", "author": "b", "permlink": "c", "weight": 10000}],
        ["transfer", {"from": "gateiodeposit", "to": "blaze.apps", "amount": "5538.235 STEEM", "memo": "svm-deposit 0x1111111111111111111111111111111111111111"}],
        ["transfer", {"from": "alice", "to": "someone-else", "amount": "1.000 STEEM", "memo": "not for us"}],
        ["transfer", {"from": "bob", "to": "blaze.apps", "amount": "2.000 SBD", "memo": "wrong asset"}]
      ]
    },
    {
      "transaction_id": "3aaa2aa9a1d0529dddefeecb1dddb308681df640",
      "operations": [
        ["transfer", {"from": "carol", "to": "blaze.apps", "amount": "0.001 STEEM", "memo": "svm-register steem1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq"}]
      ]
    }
  ]
}`

func TestExtractGatewayTransfers(t *testing.T) {
	var block *steemBlock
	require.NoError(t, json.Unmarshal([]byte(blockFixture), &block))

	transfers := ExtractGatewayTransfers(107795182, block, "blaze.apps")
	require.Len(t, transfers, 2)

	first := transfers[0]
	require.Equal(t, "d7fa48804a0f70cc0414e8938cf2d37404f0be7b", first.Txid)
	require.Equal(t, uint32(1), first.OpIndex, "op index counts ALL ops in the tx, not just transfers")
	require.Equal(t, uint64(107795182), first.SteemBlock)
	require.Equal(t, "2026-07-14T13:09:12", first.SteemTimestamp, "timestamp must be Steem's string verbatim")
	require.Equal(t, "gateiodeposit", first.From)
	require.Equal(t, uint64(5538235), first.AmountMillisteem)
	require.Equal(t, IntentDeposit, RouteMemo(first.Memo))

	second := transfers[1]
	require.Equal(t, "3aaa2aa9a1d0529dddefeecb1dddb308681df640", second.Txid)
	require.Equal(t, uint32(0), second.OpIndex)
	require.Equal(t, uint64(1), second.AmountMillisteem)
	require.Equal(t, IntentRegister, RouteMemo(second.Memo))
}

func TestExtractGatewayTransfers_TxidFallbackAndNilBlock(t *testing.T) {
	require.Nil(t, ExtractGatewayTransfers(1, nil, "blaze.apps"))

	// No per-tx transaction_id: fall back to the block-level list.
	raw := `{
	  "timestamp": "2026-07-14T13:09:15",
	  "transaction_ids": ["aabbccddeeff00112233445566778899aabbccdd"],
	  "transactions": [
	    {"operations": [["transfer", {"from": "x", "to": "gw", "amount": "1.000 STEEM", "memo": ""}]]}
	  ]
	}`
	var block *steemBlock
	require.NoError(t, json.Unmarshal([]byte(raw), &block))
	transfers := ExtractGatewayTransfers(2, block, "gw")
	require.Len(t, transfers, 1)
	require.Equal(t, "aabbccddeeff00112233445566778899aabbccdd", transfers[0].Txid)
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// Missing file: zero state, no error.
	s, err := LoadState(dir)
	require.NoError(t, err)
	require.Zero(t, s.LastScannedBlock)

	require.NoError(t, SaveState(dir, State{LastScannedBlock: 107795191}))
	s, err = LoadState(dir)
	require.NoError(t, err)
	require.Equal(t, uint64(107795191), s.LastScannedBlock)

	// Overwrite works (atomic rename over existing file).
	require.NoError(t, SaveState(dir, State{LastScannedBlock: 107795192}))
	s, err = LoadState(dir)
	require.NoError(t, err)
	require.Equal(t, uint64(107795192), s.LastScannedBlock)
}
