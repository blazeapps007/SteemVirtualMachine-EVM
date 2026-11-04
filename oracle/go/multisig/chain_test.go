package multisig

import (
	"context"
	"encoding/json"
	"fmt"
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
	if h.NextSeq != 250 || len(h.Paid) != 250 || h.Paid["0"] != "trx000" || h.Paid["249"] != "trx249" {
		t.Fatalf("after sync: next %d paid %d", h.NextSeq, len(h.Paid))
	}
	node.addHistory(testBank, "trx250", ts, "custom_json", PayoutOps(testBank, "rcpt", "1.000 STEEM", "", 250)[1].Value)
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
