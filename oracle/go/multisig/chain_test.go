package multisig

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"steemvm/oracle/go/steemtx"
)

func TestParseWithdrawalMemo(t *testing.T) {
	for memo, want := range map[string]uint64{"svm-withdrawal 0": 0, "svm-withdrawal 7": 7, "svm-withdrawal 18446744073709551615": 18446744073709551615} {
		if got, ok := ParseWithdrawalMemo(memo); !ok || got != want {
			t.Errorf("%q: %d %v, want %d", memo, got, ok, want)
		}
		if WithdrawalMemo(want) != memo {
			t.Errorf("WithdrawalMemo(%d) = %q", want, WithdrawalMemo(want))
		}
	}
	for _, memo := range []string{
		"svm-withdrawal 07", "svm-withdrawal +7", "svm-withdrawal 7 ", " svm-withdrawal 7", "svm-withdrawal  7",
		"svm-withdrawal", "svm-withdrawal ", "svm-withdraw 7", "SVM-WITHDRAWAL 7", "svm-withdrawal 0x7",
		"svm-withdrawal 18446744073709551616", "svm-withdrawal -1", "svm-withdrawal 7\n",
	} {
		if id, ok := ParseWithdrawalMemo(memo); ok {
			t.Errorf("%q accepted as %d", memo, id)
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
	h.apply(testBank, histEntry("t1", ts, "transfer", steemtx.Transfer{From: testBank, To: "rcpt", Amount: "1.000 STEEM", Memo: "svm-withdrawal 5"}))
	// The bridge relay attests whitespace-lenient memos, so they count as paid.
	h.apply(testBank, histEntry("t2", ts, "transfer", steemtx.Transfer{From: testBank, To: "rcpt", Amount: "1.000 STEEM", Memo: "  svm-withdrawal   9 "}))
	h.apply(testBank, histEntry("t3", ts, "transfer", steemtx.Transfer{From: "user", To: testBank, Amount: "1.000 STEEM", Memo: "svm-withdrawal 11"}))
	h.apply(testBank, histEntry("t4", ts, "transfer", steemtx.Transfer{From: testBank, To: "rcpt", Amount: "1.000 STEEM", Memo: "svm-withdrawal x"}))
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
		node.addHistory(testBank, fmt.Sprintf("trx%03d", i), ts, "transfer",
			steemtx.Transfer{From: testBank, To: "rcpt", Amount: "1.000 STEEM", Memo: WithdrawalMemo(uint64(i))})
	}
	rpc := steemtx.NewRPC(node.URL())
	h := &HistoryIndex{Paid: map[string]string{}, Executed: map[string]string{}}
	if err := h.Sync(context.Background(), rpc, testBank, 110000000, testNow); err != nil {
		t.Fatal(err)
	}
	if h.NextSeq != 250 || len(h.Paid) != 250 || h.Paid["0"] != "trx000" || h.Paid["249"] != "trx249" {
		t.Fatalf("after sync: next %d paid %d", h.NextSeq, len(h.Paid))
	}
	node.addHistory(testBank, "trx250", ts, "transfer", steemtx.Transfer{From: testBank, To: "rcpt", Amount: "1.000 STEEM", Memo: WithdrawalMemo(250)})
	node.starts = nil
	if err := h.Sync(context.Background(), rpc, testBank, 110000001, testNow); err != nil {
		t.Fatal(err)
	}
	if h.NextSeq != 251 || !h.IsPaid(250) {
		t.Fatalf("incremental sync: next %d", h.NextSeq)
	}
	// In steady state one request per pass, keyed by the head block so a
	// caching node can't serve a stale newest window.
	if len(node.starts) != 1 || node.starts[0] != 110000001 {
		t.Fatalf("steady-state requests used starts %v", node.starts)
	}
	// A fresh index on an account with no history.
	h2 := &HistoryIndex{Paid: map[string]string{}, Executed: map[string]string{}}
	if err := h2.Sync(context.Background(), rpc, "nobody", 110000001, testNow); err != nil || h2.NextSeq != 0 {
		t.Fatalf("empty history: %v next %d", err, h2.NextSeq)
	}
}
