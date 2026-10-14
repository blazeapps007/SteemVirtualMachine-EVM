package multisig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"steemvm/oracle/go/steemtx"
)

// fakeSteem is a minimal Steem node: it serves the condenser_api calls the
// signer uses and applies broadcasts under the chain's rules that matter here
// (signature authority with active satisfying posting, no unnecessary
// signatures, expiry, duplicate rejection, one owner change per hour, and
// account history paging that asserts start >= limit).
type fakeSteem struct {
	t         *testing.T
	mu        sync.Mutex
	srv       *httptest.Server
	head      uint32
	now       time.Time
	accounts  map[string]*Account
	history   map[string][]historyEntry
	seen      map[string]bool
	lastOwner map[string]time.Time
	transfers []steemtx.Transfer // executed transfers from svm.bank
	rejected  []string           // broadcast errors other than duplicates
	starts    []int64            // get_account_history start params, in order
}

func newFakeSteem(t *testing.T) *fakeSteem {
	n := &fakeSteem{t: t, head: 100000, now: testNow, accounts: map[string]*Account{}, history: map[string][]historyEntry{},
		seen: map[string]bool{}, lastOwner: map[string]time.Time{}}
	n.srv = httptest.NewServer(http.HandlerFunc(n.serve))
	t.Cleanup(n.srv.Close)
	return n
}

func (n *fakeSteem) URL() string { return n.srv.URL }

func (n *fakeSteem) addHistory(account, trx, ts, opType string, op any) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.history[account] = append(n.history[account], histEntry(trx, ts, opType, op))
}

func (n *fakeSteem) advance(blocks uint32) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.head += blocks
	n.now = n.now.Add(3 * time.Second * time.Duration(blocks))
}

func (n *fakeSteem) props() steemtx.DynamicProps {
	return steemtx.DynamicProps{HeadBlockNumber: n.head, HeadBlockID: fmt.Sprintf("%08x", n.head) + strings.Repeat("cd", 16),
		Time: n.now.Format(steemtx.TimeLayout)}
}

func (n *fakeSteem) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	n.mu.Lock()
	result, err := n.call(req.Method, req.Params)
	n.mu.Unlock()
	resp := map[string]any{"jsonrpc": "2.0", "id": 1}
	if err != nil {
		resp["error"] = map[string]any{"code": -32000, "message": err.Error()}
	} else {
		resp["result"] = result
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (n *fakeSteem) call(method string, params json.RawMessage) (any, error) {
	switch method {
	case "condenser_api.get_dynamic_global_properties":
		return n.props(), nil
	case "condenser_api.get_accounts":
		var p [][]string
		if err := json.Unmarshal(params, &p); err != nil || len(p) != 1 {
			return nil, errors.New("bad params")
		}
		out := []Account{}
		for _, name := range p[0] {
			if a, ok := n.accounts[name]; ok {
				out = append(out, *a)
			}
		}
		return out, nil
	case "condenser_api.get_account_history":
		var p []json.RawMessage
		var account string
		var start, limit int64
		if json.Unmarshal(params, &p) != nil || len(p) != 3 || json.Unmarshal(p[0], &account) != nil ||
			json.Unmarshal(p[1], &start) != nil || json.Unmarshal(p[2], &limit) != nil {
			return nil, errors.New("bad params")
		}
		n.starts = append(n.starts, start)
		if start < limit {
			return nil, errors.New("Assert Exception:args.start >= args.limit: start must be greater than limit")
		}
		h := n.history[account]
		out := [][2]any{}
		if len(h) == 0 {
			return out, nil
		}
		top := min(start, int64(len(h)-1))
		for seq := max(0, top-limit); seq <= top; seq++ {
			out = append(out, [2]any{seq, h[seq]})
		}
		return out, nil
	case "condenser_api.broadcast_transaction_synchronous":
		var p []steemtx.Transaction
		if err := json.Unmarshal(params, &p); err != nil || len(p) != 1 {
			return nil, fmt.Errorf("bad transaction: %v", err)
		}
		id, err := n.apply(p[0])
		if err != nil {
			if !strings.Contains(err.Error(), "Duplicate transaction") {
				n.rejected = append(n.rejected, err.Error())
			}
			return nil, err
		}
		return map[string]any{"id": id, "block_num": n.head + 1}, nil
	}
	return nil, errors.New("unknown method " + method)
}

// satisfies walks an authority the way the chain's sign_state does: keys,
// then accounts in name order through their active authority (depth-limited),
// stopping at the threshold, and marking every key it relies on as used.
func (n *fakeSteem) satisfies(auth steemtx.Authority, keys, used map[string]bool, depth int) bool {
	var total uint32
	for _, k := range auth.KeyAuths {
		if keys[k.Key] {
			used[k.Key] = true
			if total += uint32(k.Weight); total >= auth.WeightThreshold {
				return true
			}
		}
	}
	if depth < 2 {
		for _, a := range auth.Sorted().AccountAuths {
			if acc, ok := n.accounts[a.Name]; ok && n.satisfies(acc.Active, keys, used, depth+1) {
				if total += uint32(a.Weight); total >= auth.WeightThreshold {
					return true
				}
			}
		}
	}
	return total >= auth.WeightThreshold
}

func (n *fakeSteem) apply(tx steemtx.Transaction) (string, error) {
	id, err := tx.ID()
	if err != nil {
		return "", err
	}
	if n.seen[id] {
		return "", errors.New("Duplicate transaction check failed")
	}
	exp, err := time.Parse(steemtx.TimeLayout, tx.Expiration)
	if err != nil || !n.now.Before(exp) || exp.Sub(n.now) > time.Hour {
		return "", errors.New("transaction expiration out of range")
	}
	if len(tx.Operations) != 1 {
		return "", errors.New("test node applies single-operation transactions only")
	}
	digest, _ := tx.Digest()
	keys, used := map[string]bool{}, map[string]bool{}
	for _, sig := range tx.Signatures {
		pub, err := steemtx.RecoverSigner(digest, sig)
		if err != nil || keys[pub] {
			return "", errors.New("bad or duplicate signature")
		}
		keys[pub] = true
	}

	var ok bool
	var account string
	switch op := tx.Operations[0].Value.(type) {
	case steemtx.AccountUpdate2: // posting-level when it only touches metadata
		acc := n.accounts[op.Account]
		ok = acc != nil && op.Owner == nil && op.Active == nil && op.Posting == nil &&
			(n.satisfies(acc.Posting, keys, used, 0) || n.satisfies(acc.Active, keys, used, 0) || n.satisfies(acc.Owner, keys, used, 0))
		account = op.Account
	case steemtx.Transfer:
		acc := n.accounts[op.From]
		ok = acc != nil && n.satisfies(acc.Active, keys, used, 0)
		account = op.From
	case steemtx.AccountUpdate:
		acc := n.accounts[op.Account]
		if acc != nil && op.Owner != nil {
			ok = n.satisfies(acc.Owner, keys, used, 0)
		} else {
			ok = acc != nil && n.satisfies(acc.Active, keys, used, 0)
		}
		account = op.Account
	default:
		return "", errors.New("operation not supported by the test node")
	}
	if !ok {
		return "", errors.New("Missing Authority")
	}
	if len(used) != len(keys) {
		return "", errors.New("Unnecessary signature(s) detected")
	}

	switch op := tx.Operations[0].Value.(type) {
	case steemtx.AccountUpdate2:
		acc := n.accounts[op.Account]
		if op.PostingJSONMetadata != "" {
			acc.PostingJSONMetadata = op.PostingJSONMetadata
		}
		if op.JSONMetadata != "" {
			acc.JSONMetadata = op.JSONMetadata
		}
	case steemtx.Transfer:
		amt, err := steemtx.ParseAsset(op.Amount)
		if err != nil {
			return "", err
		}
		if err := n.move(op.From, op.To, amt); err != nil {
			return "", err
		}
		n.transfers = append(n.transfers, op)
	case steemtx.AccountUpdate:
		acc := n.accounts[op.Account]
		if op.Owner != nil {
			if last, ok := n.lastOwner[op.Account]; ok && n.now.Sub(last) < time.Hour {
				return "", errors.New("Owner authority can only be updated once an hour")
			}
			acc.Owner, n.lastOwner[op.Account] = op.Owner.Sorted(), n.now
		}
		if op.Active != nil {
			acc.Active = op.Active.Sorted()
		}
		if op.Posting != nil {
			acc.Posting = op.Posting.Sorted()
		}
		acc.MemoKey = op.MemoKey
	}
	n.seen[id] = true
	h := histEntry(id, n.now.Format(steemtx.TimeLayout), tx.Operations[0].Type, tx.Operations[0].Value)
	n.history[account] = append(n.history[account], h)
	return id, nil
}

func (n *fakeSteem) move(from, to string, amt steemtx.Asset) error {
	field := func(a *Account) *string {
		if amt.Symbol == "SBD" {
			return &a.SBDBalance
		}
		return &a.Balance
	}
	src, dst := n.accounts[from], n.accounts[to]
	if src == nil || dst == nil {
		return errors.New("unknown account")
	}
	have, err := steemtx.ParseAsset(*field(src))
	if err != nil || have.Amount < amt.Amount {
		return errors.New("insufficient funds")
	}
	got, _ := steemtx.ParseAsset(*field(dst))
	*field(src) = steemtx.FormatMilli(uint64(have.Amount-amt.Amount), amt.Symbol)
	*field(dst) = steemtx.FormatMilli(uint64(got.Amount+amt.Amount), amt.Symbol)
	return nil
}

type testLogger struct {
	t    *testing.T
	name string
}

func (l testLogger) Info(msg string, kv ...any)  { l.t.Logf("INFO  %-5s %s %v", l.name, msg, kv) }
func (l testLogger) Error(msg string, kv ...any) { l.t.Logf("ERROR %-5s %s %v", l.name, msg, kv) }

// network is a fake Steem node plus one signer per svm.bank member.
type network struct {
	t       *testing.T
	node    *fakeSteem
	svm     *fakeSVM
	signers []*Signer
}

func newNetwork(t *testing.T) *network {
	node := newFakeSteem(t)
	auth := bankAuthority(testWeights)
	node.accounts[testBank] = &Account{Name: testBank, Owner: auth, Active: auth, Posting: auth, MemoKey: pubOf("memo"),
		Balance: "1000.000 STEEM", SBDBalance: "50.000 SBD"}
	node.accounts["rcpt"] = &Account{Name: "rcpt", Balance: "0.000 STEEM", SBDBalance: "0.000 SBD"}
	for _, w := range testWeights {
		node.accounts[w.Name] = &Account{Name: w.Name, Owner: keyAuthority(pubOf(w.Name + "/owner")), Active: keyAuthority(pubOf(w.Name)),
			Posting: keyAuthority(pubOf(w.Name + "/posting")), MemoKey: pubOf(w.Name),
			PostingJSONMetadata: `{"profile":{"name":"` + w.Name + ` <validator> & co"}}`, Balance: "0.000 STEEM", SBDBalance: "0.000 SBD"}
	}
	svm := &fakeSVM{svmData: &svmData{height: 100000, timeout: 50400, all: map[uint64]Withdrawal{},
		stakes: map[int64][]StakeEntry{99000: stakes("alice", 45, "bob", 30, "carol", 15, "dave", 10)}}}
	nw := &network{t: t, node: node, svm: svm}
	for _, w := range testWeights {
		nw.signers = append(nw.signers, nw.signer(w.Name, testWIF(testKey(w.Name))))
	}
	return nw
}

func (nw *network) signer(name, wif string) *Signer {
	nw.t.Helper()
	cfg := SignerConfig{Policy: DefaultConfig(), ActiveWIF: wif, Nodes: []string{nw.node.URL()}, StateDir: nw.t.TempDir(),
		Propose: true, Execute: true, RankDelay: time.Nanosecond}
	cfg.Policy.FallbackDelay = time.Hour // only the designated member proposes in these tests
	s, err := NewSigner(cfg, nw.svm.as(name), testLogger{nw.t, name})
	if err != nil {
		nw.t.Fatal(err)
	}
	if err := s.Init(context.Background()); err != nil {
		nw.t.Fatal(err)
	}
	nw.t.Cleanup(s.unlock)
	return s
}

// rounds runs every signer's pass in turn, one Steem block apart.
func (nw *network) rounds(k int) {
	nw.t.Helper()
	for i := 0; i < k; i++ {
		for _, s := range nw.signers {
			if err := s.Pass(context.Background()); err != nil {
				nw.t.Fatalf("%s: %v", s.self, err)
			}
			nw.node.advance(1)
		}
	}
}

func TestSignersPayWithdrawalsExactlyOnce(t *testing.T) {
	nw := newNetwork(t)
	nw.svm.all[6] = Withdrawal{ID: 6, Destination: "rcpt", AmountMilli: 12345, Symbol: "STEEM", CreatedAt: 90000, Status: "REQUESTED"}
	nw.svm.all[9] = Withdrawal{ID: 9, Destination: "rcpt", AmountMilli: 2500, Symbol: "SBD", CreatedAt: 95000, Status: "REQUESTED"}
	// Too close to its automatic refund: must never be paid.
	nw.svm.all[3] = Withdrawal{ID: 3, Destination: "rcpt", AmountMilli: 1000, Symbol: "STEEM", CreatedAt: 100000 + 14400 - 50400 - 1, Status: "REQUESTED"}

	nw.rounds(3)
	n := nw.node
	if len(n.rejected) != 0 {
		t.Fatalf("the node rejected broadcasts: %v", n.rejected)
	}
	want := map[string]steemtx.Transfer{
		"svm-withdrawal 6": {From: testBank, To: "rcpt", Amount: "12.345 STEEM", Memo: "svm-withdrawal 6"},
		"svm-withdrawal 9": {From: testBank, To: "rcpt", Amount: "2.500 SBD", Memo: "svm-withdrawal 9"},
	}
	if len(n.transfers) != len(want) {
		t.Fatalf("executed %d transfers %+v, want %d", len(n.transfers), n.transfers, len(want))
	}
	for _, tr := range n.transfers {
		if want[tr.Memo] != tr {
			t.Fatalf("unexpected transfer %+v", tr)
		}
	}
	if b := n.accounts[testBank]; b.Balance != "987.655 STEEM" || b.SBDBalance != "47.500 SBD" {
		t.Fatalf("bank balance %s %s", b.Balance, b.SBDBalance)
	}

	// The bridge relays attest the payouts; nothing else happens afterwards,
	// even while the SVM side still says REQUESTED for a while.
	nw.rounds(2)
	for _, id := range []uint64{6, 9} {
		w := nw.svm.all[id]
		w.Status = "PROCESSED"
		nw.svm.all[id] = w
	}
	nw.rounds(2)
	if len(n.transfers) != 2 || len(n.rejected) != 0 {
		t.Fatalf("after settling: %d transfers, rejected %v", len(n.transfers), n.rejected)
	}
	// Publishing votes never disturbed the rest of a validator's metadata.
	for _, w := range testWeights {
		if !strings.HasPrefix(n.accounts[w.Name].PostingJSONMetadata, `{"profile":{"name":"`+w.Name+` <validator> & co"},"svm_ms":`) {
			t.Fatalf("%s metadata: %s", w.Name, n.accounts[w.Name].PostingJSONMetadata)
		}
	}
	// Every approval a signer published is in its write-ahead journal.
	for _, s := range nw.signers {
		_, b, err := ParseMetadata(n.accounts[s.self].PostingJSONMetadata)
		if err != nil {
			t.Fatal(err)
		}
		for id, v := range b.Votes {
			if _, ok := s.journal.Approvals[id]; v.A == 1 && !ok {
				t.Fatalf("%s published an approval of %s that is not in its journal", s.self, id)
			}
		}
	}
}

func TestSignersSyncValidatorSet(t *testing.T) {
	nw := newNetwork(t)
	// Stake moves: at the 100000 checkpoint dave's share halves.
	nw.svm.height = 101000
	nw.svm.stakes[100000] = stakes("alice", 50, "bob", 30, "carol", 15, "dave", 5)
	want := bankAuthority([]steemtx.AccountAuth{{Name: "alice", Weight: 5000}, {Name: "bob", Weight: 3000}, {Name: "carol", Weight: 1500}, {Name: "dave", Weight: 500}})

	nw.rounds(3)
	n := nw.node
	if len(n.rejected) != 0 {
		t.Fatalf("the node rejected broadcasts: %v", n.rejected)
	}
	b := n.accounts[testBank]
	if !b.Owner.Equal(want) || !b.Active.Equal(want) || !b.Posting.Equal(want) || b.MemoKey != pubOf("memo") {
		t.Fatalf("bank authority not synced: owner %+v", b.Owner)
	}
	if b.Owner.Total() != TotalWeight || b.Owner.WeightThreshold != 7500 {
		t.Fatalf("sum %d threshold %d", b.Owner.Total(), b.Owner.WeightThreshold)
	}
	// Converged: further passes propose nothing (and no second owner change
	// inside the hour is ever attempted).
	before := len(n.history[testBank])
	nw.rounds(3)
	if len(n.history[testBank]) != before || len(n.rejected) != 0 {
		t.Fatalf("activity after convergence: %d new entries, rejected %v", len(n.history[testBank])-before, n.rejected)
	}
}

func TestSignerKeyGuards(t *testing.T) {
	nw := newNetwork(t)
	cfg := SignerConfig{Policy: DefaultConfig(), Nodes: []string{nw.node.URL()}, StateDir: t.TempDir()}

	// The owner key is refused even though it would sign valid transactions.
	cfg.ActiveWIF = testWIF(testKey("alice/owner"))
	s, err := NewSigner(cfg, nw.svm.as("alice"), testLogger{t, "guard"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(context.Background()); err == nil || !strings.Contains(err.Error(), "OWNER key") {
		t.Fatalf("owner key accepted: %v", err)
	}
	s.unlock()

	// A key that isn't the account's active key.
	cfg.ActiveWIF, cfg.StateDir = testWIF(testKey("bob")), t.TempDir()
	s, err = NewSigner(cfg, nw.svm.as("alice"), testLogger{t, "guard"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(context.Background()); err == nil || !strings.Contains(err.Error(), "does not satisfy") {
		t.Fatalf("foreign key accepted: %v", err)
	}
	s.unlock()

	// A posting or owner secret in the environment stops startup outright.
	for _, k := range []string{"ORACLE_STEEM_OWNER_WIF", "ORACLE_STEEM_POSTING_WIF", "MASTER_PASSWORD", "OWNER"} {
		t.Run(k, func(t *testing.T) {
			t.Setenv(k, "5Kxxxxxxxx")
			cfg.ActiveWIF = testWIF(testKey("alice"))
			if _, err := NewSigner(cfg, nw.svm.as("alice"), testLogger{t, "guard"}); err == nil || !strings.Contains(err.Error(), k) {
				t.Fatalf("started with %s set: %v", k, err)
			}
		})
	}
	for _, bad := range []string{"", "not-a-wif", testWIF(testKey("alice"))[:50]} {
		cfg.ActiveWIF = bad
		if _, err := NewSigner(cfg, nw.svm.as("alice"), testLogger{t, "guard"}); err == nil {
			t.Fatalf("started with ORACLE_STEEM_ACTIVE_WIF=%q", bad)
		}
	}
}

func TestSignerDryRunPublishesNothing(t *testing.T) {
	nw := newNetwork(t)
	nw.svm.all[6] = Withdrawal{ID: 6, Destination: "rcpt", AmountMilli: 12345, Symbol: "STEEM", CreatedAt: 90000, Status: "REQUESTED"}
	for _, s := range nw.signers {
		s.cfg.DryRun = true
	}
	before := map[string]string{}
	for name, a := range nw.node.accounts {
		before[name] = a.PostingJSONMetadata
	}
	nw.rounds(3)
	if len(nw.node.transfers) != 0 || len(nw.node.seen) != 0 {
		t.Fatalf("dry run broadcast %d transactions", len(nw.node.seen))
	}
	for name, a := range nw.node.accounts {
		if a.PostingJSONMetadata != before[name] {
			t.Fatalf("dry run changed %s's metadata", name)
		}
	}
	for _, s := range nw.signers {
		if len(s.journal.Approvals) != 0 {
			t.Fatalf("dry run journaled approvals for %s", s.self)
		}
	}
}
