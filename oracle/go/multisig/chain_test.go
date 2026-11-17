package multisig

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"steemvm/oracle/go/steemtx"
)

func TestParsePayoutLink(t *testing.T) {
	good := func(body string) steemtx.CustomJSON {
		return steemtx.CustomJSON{RequiredAuths: []string{testBank}, RequiredPostingAuths: []string{}, ID: PayoutLinkID, JSON: body}
	}
	for want, body := range map[uint64]string{
		0: `{"withdrawal_id":"0"}`, 7: `{"withdrawal_id":"7"}`, 18446744073709551615: `{"withdrawal_id":"18446744073709551615"}`,
	} {
		if got, ok := ParsePayoutLink(good(body), testBank); !ok || got != want {
			t.Errorf("%s: %d %v, want %d", body, got, ok, want)
		}
		if PayoutLinkJSON(want) != body {
			t.Errorf("PayoutLinkJSON(%d) = %s", want, PayoutLinkJSON(want))
		}
	}
	for _, body := range []string{
		`{"withdrawal_id":"07"}`, `{"withdrawal_id":7}`, `{"withdrawal_id":"+7"}`, `{"withdrawal_id":"-1"}`, `{"withdrawal_id":"0x7"}`,
		`{"withdrawal_id":""}`, `{ "withdrawal_id":"7"}`, `{"withdrawal_id":"7"} `, `{"withdrawal_id":"7","x":1}`, `{"id":"7"}`,
		`{"withdrawal_id":"18446744073709551616"}`, "svm-withdrawal 7", "",
	} {
		if id, ok := ParsePayoutLink(good(body), testBank); ok {
			t.Errorf("%s accepted as %d", body, id)
		}
	}
	for name, c := range map[string]steemtx.CustomJSON{
		"other id":        {RequiredAuths: []string{testBank}, ID: "other", JSON: PayoutLinkJSON(7)},
		"other signer":    {RequiredAuths: []string{"alice"}, ID: PayoutLinkID, JSON: PayoutLinkJSON(7)},
		"posting signer":  {RequiredPostingAuths: []string{testBank}, ID: PayoutLinkID, JSON: PayoutLinkJSON(7)},
		"two signers":     {RequiredAuths: []string{testBank, "alice"}, ID: PayoutLinkID, JSON: PayoutLinkJSON(7)},
		"posting as well": {RequiredAuths: []string{testBank}, RequiredPostingAuths: []string{"alice"}, ID: PayoutLinkID, JSON: PayoutLinkJSON(7)},
	} {
		if _, ok := ParsePayoutLink(c, testBank); ok {
			t.Errorf("%s accepted", name)
		}
	}
}

func histEntry(trx, ts string, opType string, op any) historyEntry {
	t, _ := json.Marshal(opType)
	b, _ := json.Marshal(op)
	return historyEntry{TrxID: trx, Timestamp: ts, Op: [2]json.RawMessage{t, b}}
}

func TestHistoryApply(t *testing.T) {
	h := &HistoryIndex{Paid: map[string]string{}, Executed: map[string]string{}}
	ts := testNow.Format(steemtx.TimeLayout)
	link := func(id uint64) steemtx.CustomJSON {
		return PayoutOps(testBank, "rcpt", "1.000 STEEM", "", id)[1].Value.(steemtx.CustomJSON)
	}
	h.apply(testBank, histEntry("t1", ts, "custom_json", link(5)))
	h.apply(testBank, histEntry("t2", ts, "custom_json", link(9)))
	// A memo is the user's own text: it never marks a withdrawal paid.
	h.apply(testBank, histEntry("t3", ts, "transfer", steemtx.Transfer{From: testBank, To: "rcpt", Amount: "1.000 STEEM", Memo: "svm-withdrawal 11"}))
	// Anyone can post a custom_json with this id, but not as svm.bank.
	h.apply(testBank, histEntry("t4", ts, "custom_json", steemtx.CustomJSON{RequiredAuths: []string{"user"}, ID: PayoutLinkID, JSON: PayoutLinkJSON(11)}))
	h.apply(testBank, histEntry(zeroTxID, ts, "fill_vesting_withdraw", map[string]string{}))
	for id, want := range map[uint64]bool{5: true, 9: true, 11: false} {
		if h.IsPaid(id) != want {
			t.Errorf("IsPaid(%d) = %v, want %v", id, !want, want)
		}
	}
	if h.Paid["5"] != "t1" || len(h.Executed) != 4 || h.Executed[zeroTxID] != "" {
		t.Errorf("paid %v executed %v", h.Paid, h.Executed)
	}

	auth := bankAuthority(testWeights)
	old := testNow.Add(-keepExecuted - time.Hour).Format(steemtx.TimeLayout)
	h.apply(testBank, histEntry("u1", old, "account_update2", map[string]any{"account": testBank, "posting_json_metadata": "{}"}))
	if h.LastOwnerChange != "" {
		t.Fatal("a metadata update counted as an owner change")
	}
	h.apply(testBank, histEntry("u2", old, "account_update", map[string]any{"account": testBank, "owner": auth}))
	if h.LastOwnerChange != old {
		t.Fatalf("owner change not recorded: %q", h.LastOwnerChange)
	}
	h.apply(testBank, histEntry("u3", ts, "account_update", map[string]any{"account": "someone", "owner": auth}))
	if h.LastOwnerChange != old {
		t.Fatal("another account's owner change was recorded")
	}

	// The executed-trx set keeps only recent entries (proposals live an hour).
	h.pruneExecuted(testNow)
	if len(h.Executed) != 5 || h.Executed["u1"] != "" || h.Executed["u3"] == "" {
		t.Fatalf("executed after prune: %v", h.Executed)
	}
}

func TestHistorySyncPaging(t *testing.T) {
	node := newFakeSteem(t)
	ts := testNow.Format(steemtx.TimeLayout)
	for i := 0; i < 250; i++ {
		node.addHistory(testBank, fmt.Sprintf("trx%03d", i), ts, "custom_json",
			PayoutOps(testBank, "rcpt", "1.000 STEEM", "", uint64(i))[1].Value)
	}
	rpc := steemtx.NewRPC(node.URL())
	h := &HistoryIndex{Paid: map[string]string{}, Executed: map[string]string{}}
	if err := h.Sync(context.Background(), rpc, testBank, 110000000, testNow); err != nil {
		t.Fatal(err)
	}
	// 251 entries: the account's creation (seq 0) plus 250 payouts.
	if h.NextSeq != 251 || len(h.Paid) != 250 || h.Paid["0"] != "trx000" || h.Paid["249"] != "trx249" {
		t.Fatalf("after sync: next %d paid %d", h.NextSeq, len(h.Paid))
	}
	node.addHistory(testBank, "trx250", ts, "custom_json", PayoutOps(testBank, "rcpt", "1.000 STEEM", "", 250)[1].Value)
	node.starts = nil
	if err := h.Sync(context.Background(), rpc, testBank, 110000001, testNow); err != nil {
		t.Fatal(err)
	}
	if h.NextSeq != 252 || !h.IsPaid(250) {
		t.Fatalf("incremental sync: next %d", h.NextSeq)
	}
	// In steady state one request per pass, keyed by the head block so a
	// caching node can't serve a stale newest window.
	if len(node.starts) != 1 || node.starts[0] != 110000001 {
		t.Fatalf("steady-state requests used starts %v", node.starts)
	}
	// An empty answer means the node has no account history (a real account's
	// history always holds its creation): it is an error, never "nothing paid".
	h2 := &HistoryIndex{Paid: map[string]string{}, Executed: map[string]string{}}
	if err := h2.Sync(context.Background(), rpc, "nobody", 110000001, testNow); err == nil || h2.NextSeq != 0 {
		t.Fatalf("empty history must not be trusted: %v next %d", err, h2.NextSeq)
	}
}

// A node that rate-limits (HTML), lacks account history ("Internal Error" or
// an empty answer) or misbehaves ("Bad Cast") is skipped for the next node,
// which then serves the call; the bad node is benched for later calls.
func TestHistorySyncFailsOverBadNodes(t *testing.T) {
	good := newFakeSteem(t)
	ts := testNow.Format(steemtx.TimeLayout)
	good.addHistory(testBank, "trx000", ts, "custom_json", PayoutOps(testBank, "rcpt", "1.000 STEEM", "", 7)[1].Value)
	bad := map[string]http.HandlerFunc{
		"html": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("<html><body>429 Too Many Requests</body></html>"))
		},
		"internal error": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"Internal Error"}}`))
		},
		"bad cast": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"Bad Cast:Invalid cast from object_type to Array"}}`))
		},
		"empty": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[]}`))
		},
	}
	for name, handler := range bad {
		t.Run(name, func(t *testing.T) {
			hits := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; handler(w, r) }))
			t.Cleanup(srv.Close)
			rpc := steemtx.NewRPC(srv.URL, good.URL())
			h := &HistoryIndex{Paid: map[string]string{}, Executed: map[string]string{}}
			if err := h.Sync(context.Background(), rpc, testBank, 110000000, testNow); err != nil {
				t.Fatal(err)
			}
			if !h.IsPaid(7) {
				t.Fatalf("payout from the good node not indexed")
			}
			if err := h.Sync(context.Background(), rpc, testBank, 110000001, testNow); err != nil {
				t.Fatal(err)
			}
			if hits != 1 {
				t.Fatalf("benched node was asked %d times, want 1", hits)
			}
		})
	}
}

// blockNode is a fake Steem node serving only condenser_api.get_block, from a
// map of block number -> transactions (null for blocks above head), and an
// "Internal Error" for get_account_history like a node without the plugin.
type blockNode struct {
	mu    sync.Mutex
	head  uint32
	txs   map[uint32][]any
	calls map[uint32]int
}

func (n *blockNode) serve(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Method != "condenser_api.get_block" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"Internal Error"}}`))
			return
		}
		var num uint32
		_ = json.Unmarshal(req.Params[0], &num)
		n.mu.Lock()
		n.calls[num]++
		var result any
		if num <= n.head {
			txs := n.txs[num]
			ids := make([]string, len(txs))
			for i := range txs {
				ids[i] = fmt.Sprintf("%08x%032d", num, i)
			}
			result = map[string]any{"timestamp": testNow.Format(steemtx.TimeLayout), "transaction_ids": ids,
				"transactions": func() []any {
					out := []any{}
					for _, ops := range txs {
						out = append(out, map[string]any{"operations": ops})
					}
					return out
				}()}
		}
		n.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestScanBlocksRebuildsHistory(t *testing.T) {
	payout := PayoutOps(testBank, "rcpt", "1.000 STEEM", "", 42)
	node := &blockNode{head: 1000, calls: map[uint32]int{}, txs: map[uint32][]any{
		950: {[]any{
			[]any{"transfer", payout[0].Value},
			[]any{"custom_json", payout[1].Value},
		}},
		960: {[]any{[]any{"account_update", map[string]any{"account": testBank, "owner": map[string]any{"weight_threshold": 1}}}}},
		970: {[]any{[]any{"transfer", map[string]any{"from": "alice", "to": "bob", "amount": "1.000 STEEM", "memo": "svm.bank"}}}},
		// A payout link signed by someone else is not svm.bank's payout.
		980: {[]any{[]any{"custom_json", map[string]any{"required_auths": []string{"mallory"}, "required_posting_auths": []string{},
			"id": payout[1].Value.(steemtx.CustomJSON).ID, "json": `{"withdrawal_id":"43"}`}}}},
	}}
	other := &blockNode{head: 1000, calls: map[uint32]int{}, txs: node.txs}
	rpc := steemtx.NewRoundRobinRPC(node.serve(t), other.serve(t))
	h := &HistoryIndex{Paid: map[string]string{}, Executed: map[string]string{}}

	// Fresh index: starts lookback blocks below head, one batch per call.
	ready, err := h.ScanBlocks(context.Background(), rpc, testBank, 1000, 300, testNow)
	if err != nil || ready || h.ScannedBlock != 700+BlockScanBatch-1 {
		t.Fatalf("first batch: ready %v err %v scanned %d", ready, err, h.ScannedBlock)
	}
	for !ready {
		if ready, err = h.ScanBlocks(context.Background(), rpc, testBank, 1000, 300, testNow); err != nil {
			t.Fatal(err)
		}
	}
	if h.ScannedBlock != 1000 || node.calls[699] != 0 || other.calls[699] != 0 {
		t.Fatalf("scanned %d, block 699 read %d/%d times", h.ScannedBlock, node.calls[699], other.calls[699])
	}
	if h.Paid["42"] != "000003b600000000000000000000000000000000" || h.IsPaid(43) {
		t.Fatalf("paid %v", h.Paid)
	}
	if h.Executed["000003b600000000000000000000000000000000"] == "" || h.LastOwnerChange == "" {
		t.Fatalf("executed %v owner change %q", h.Executed, h.LastOwnerChange)
	}
	for _, n := range []*blockNode{node, other} {
		if len(n.calls) < 30 {
			t.Fatalf("round-robin: one node served only %d blocks", len(n.calls))
		}
	}

	// Caught up: the next call reads only the new blocks.
	node.head, other.head = 1003, 1003
	if ready, err := h.ScanBlocks(context.Background(), rpc, testBank, 1003, 300, testNow); err != nil || !ready || h.ScannedBlock != 1003 {
		t.Fatalf("incremental: ready %v err %v scanned %d", ready, err, h.ScannedBlock)
	}

	// A block no node has yet stops the scan there; progress is kept.
	if ready, err := h.ScanBlocks(context.Background(), rpc, testBank, 1005, 300, testNow); err == nil || ready || h.ScannedBlock != 1003 {
		t.Fatalf("missing block: ready %v err %v scanned %d", ready, err, h.ScannedBlock)
	}
}

func TestBlockLookback(t *testing.T) {
	// Default 50400-block (3.5 day) timeout at 10s per SVM block, plus an hour.
	if got := BlockLookback(50400); got != uint32((50400*10+3600)/3) {
		t.Fatalf("lookback %d", got)
	}
	// Never shorter than the executed-trx window.
	if got := BlockLookback(1); got != uint32((96*3600+3600)/3) {
		t.Fatalf("short timeout lookback %d", got)
	}
}
