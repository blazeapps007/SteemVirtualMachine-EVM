package relayer

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"steemvm/x/oracle/bridge/types"
)

func TestParseSteemAmount(t *testing.T) {
	tests := []struct {
		in     string
		symbol string
		want   uint64
		wantOK bool
	}{
		{"70.561 STEEM", "STEEM", 70561, true},
		{"0.001 STEEM", "STEEM", 1, true},
		{"1 STEEM", "STEEM", 1000, true},
		{"1.5 STEEM", "STEEM", 1500, true},
		{"5538.235 STEEM", "STEEM", 5538235, true},
		{"  1.000 STEEM  ", "STEEM", 1000, true},
		{"0.000 STEEM", "STEEM", 0, true}, // zero parses; the chain rejects it
		{"1.000 SBD", "STEEM", 0, false},
		{"1.000000 VESTS", "STEEM", 0, false},
		{"1.2345 STEEM", "STEEM", 0, false}, // more than 3 decimals
		{"STEEM", "STEEM", 0, false},
		{"", "STEEM", 0, false},
		{". STEEM", "STEEM", 0, false},
		{"-1.000 STEEM", "STEEM", 0, false},
		{"1,000.000 STEEM", "STEEM", 0, false},
		{"1.0e3 STEEM", "STEEM", 0, false},
		// Steem testnet: bridgeable symbol is TESTS; TBD (testnet SBD) is
		// ignored, and mainnet "STEEM" no longer qualifies when watching testnet.
		{"12.345 TESTS", "TESTS", 12345, true},
		{"1 TESTS", "TESTS", 1000, true},
		{"1.000 TBD", "TESTS", 0, false},
		{"1.000 STEEM", "TESTS", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := ParseSteemAmount(tc.in, tc.symbol)
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
	dep, ok := msg.(*types.MsgAttestDeposit)
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

	transfers := ExtractGatewayTransfers(107795182, block, "blaze.apps", "STEEM", "")
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
	require.Nil(t, ExtractGatewayTransfers(1, nil, "blaze.apps", "STEEM", ""))

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
	transfers := ExtractGatewayTransfers(2, block, "gw", "STEEM", "")
	require.Len(t, transfers, 1)
	require.Equal(t, "aabbccddeeff00112233445566778899aabbccdd", transfers[0].Txid)
}

func TestParsePayoutLink(t *testing.T) {
	op := func(auths, posting, id, body string) json.RawMessage {
		return json.RawMessage(`["custom_json",{"required_auths":` + auths + `,"required_posting_auths":` + posting + `,"id":"` + id + `","json":` + strconv.Quote(body) + `}]`)
	}
	for want, body := range map[uint64]string{0: `{"withdrawal_id":"0"}`, 7: `{"withdrawal_id":"7"}`, 18446744073709551615: `{"withdrawal_id":"18446744073709551615"}`} {
		got, ok := ParsePayoutLink(op(`["gw"]`, `[]`, PayoutLinkID, body), "gw")
		require.True(t, ok, body)
		require.Equal(t, want, got)
	}
	for _, body := range []string{`{"withdrawal_id":"07"}`, `{"withdrawal_id":7}`, `{"withdrawal_id":"+7"}`, `{ "withdrawal_id":"7"}`, `{"withdrawal_id":"7","x":1}`, `{"withdrawal_id":"18446744073709551616"}`, "svm-withdrawal 7"} {
		_, ok := ParsePayoutLink(op(`["gw"]`, `[]`, PayoutLinkID, body), "gw")
		require.False(t, ok, body)
	}
	for name, raw := range map[string]json.RawMessage{
		"other id":       op(`["gw"]`, `[]`, "other", `{"withdrawal_id":"7"}`),
		"other signer":   op(`["alice"]`, `[]`, PayoutLinkID, `{"withdrawal_id":"7"}`),
		"posting signer": op(`[]`, `["gw"]`, PayoutLinkID, `{"withdrawal_id":"7"}`),
		"two signers":    op(`["gw","alice"]`, `[]`, PayoutLinkID, `{"withdrawal_id":"7"}`),
		"also posting":   op(`["gw"]`, `["alice"]`, PayoutLinkID, `{"withdrawal_id":"7"}`),
		"a transfer":     json.RawMessage(`["transfer",{"from":"gw","to":"a","amount":"1.000 STEEM","memo":"svm-withdrawal 7"}]`),
	} {
		_, ok := ParsePayoutLink(raw, "gw")
		require.False(t, ok, name)
	}
}

func TestExtractGatewayPayouts(t *testing.T) {
	const link = `["custom_json", {"required_auths": ["blaze.apps"], "required_posting_auths": [], "id": "svm-withdrawal", "json": "{\"withdrawal_id\":\"7\"}"}]`
	const raw = `{
	  "timestamp": "2026-07-14T14:00:00",
	  "transactions": [
	    {"transaction_id": "bbbb111111111111111111111111111111111111", "operations": [
	      ["transfer", {"from": "blaze.apps", "to": "alice", "amount": "10.000 STEEM", "memo": "exchange-tag-42"}],
	      ` + link + `
	    ]},
	    {"transaction_id": "cccc", "operations": [
	      ["transfer", {"from": "blaze.apps", "to": "bob", "amount": "1.000 STEEM", "memo": "svm-withdrawal 8"}]
	    ]},
	    {"transaction_id": "dddd", "operations": [
	      ["transfer", {"from": "someone", "to": "carol", "amount": "1.000 STEEM", "memo": ""}],
	      ` + link + `
	    ]},
	    {"transaction_id": "eeee", "operations": [
	      ` + link + `,
	      ["transfer", {"from": "blaze.apps", "to": "dan", "amount": "1.000 STEEM", "memo": ""}]
	    ]},
	    {"transaction_id": "ffff", "operations": [
	      ["transfer", {"from": "blaze.apps", "to": "erin", "amount": "1.000 STEEM", "memo": ""}],
	      ` + link + `,
	      ["vote", {"voter": "a"}]
	    ]},
	    {"transaction_id": "gggg", "operations": [
	      ["transfer", {"from": "blaze.apps", "to": "alice", "amount": "1.000 VESTS", "memo": ""}],
	      ` + link + `
	    ]}
	  ]
	}`
	var block *steemBlock
	require.NoError(t, json.Unmarshal([]byte(raw), &block))

	payouts := ExtractGatewayPayouts(500, block, "blaze.apps", "STEEM", "SBD")
	require.Len(t, payouts, 1, "only a gateway transfer followed by its link, alone in the tx, counts; a memo alone never does")
	require.Equal(t, uint64(7), payouts[0].WithdrawalID)
	require.Equal(t, "bbbb111111111111111111111111111111111111", payouts[0].Txid)
	require.Equal(t, uint32(0), payouts[0].OpIndex)
	require.Equal(t, uint64(500), payouts[0].SteemBlock)
	require.Equal(t, "2026-07-14T14:00:00", payouts[0].SteemTimestamp)
	require.Equal(t, "alice", payouts[0].Destination)
	require.Equal(t, "exchange-tag-42", payouts[0].Memo)
	require.Equal(t, uint64(10000), payouts[0].AmountMillisteem, "10.000 STEEM -> 10000 millisteem")
	require.Equal(t, types.BridgeAsset_BRIDGE_ASSET_STEEM, payouts[0].Asset)

	require.Nil(t, ExtractGatewayPayouts(1, nil, "blaze.apps", "STEEM", "SBD"))
}

// A payout's memo is the user's choice, so one sent to the gateway itself must
// never read as a deposit.
func TestExtractGatewayTransfers_GatewayToItselfIsNotADeposit(t *testing.T) {
	const raw = `{"timestamp": "2026-07-14T14:00:00", "transactions": [{"transaction_id": "aaaa", "operations": [
	  ["transfer", {"from": "gw", "to": "gw", "amount": "1.000 STEEM", "memo": "svm-deposit 0x1111111111111111111111111111111111111111"}]
	]}]}`
	var block *steemBlock
	require.NoError(t, json.Unmarshal([]byte(raw), &block))
	require.Empty(t, ExtractGatewayTransfers(1, block, "gw", "STEEM", ""))
}

func TestComparePayout(t *testing.T) {
	w := types.Withdrawal{DestinationSteemAccount: "alice", Memo: "tag", AmountMillisteem: 1000, Asset: types.BridgeAsset_BRIDGE_ASSET_STEEM}
	good := Payout{Destination: "alice", Memo: "tag", AmountMillisteem: 1000, Asset: types.BridgeAsset_BRIDGE_ASSET_STEEM}
	ok, _ := comparePayout(good, w)
	require.True(t, ok)

	for name, mutate := range map[string]func(*Payout){
		"recipient": func(p *Payout) { p.Destination = "mallory" },
		"memo":      func(p *Payout) { p.Memo = "" },
		"amount":    func(p *Payout) { p.AmountMillisteem = 999 },
		"asset":     func(p *Payout) { p.Asset = types.BridgeAsset_BRIDGE_ASSET_SBD },
	} {
		bad := good
		mutate(&bad)
		ok, why := comparePayout(bad, w)
		require.False(t, ok, name)
		require.Contains(t, why, name)
	}
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
