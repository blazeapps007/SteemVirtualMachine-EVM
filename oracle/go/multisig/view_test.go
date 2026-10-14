package multisig

import (
	"strings"
	"testing"
	"time"

	"steemvm/oracle/go/steemtx"
)

func TestBuildViewStatus(t *testing.T) {
	f := newFixture(t)
	pay := transferOp("rcpt", "1.000 STEEM", "svm-withdrawal 1")

	open := f.propose("alice", "open", "", 58*time.Minute, pay)
	for _, m := range []string{"alice", "carol", "dave"} { // 7000 < 7500
		f.vote(m, open, true)
	}
	ready := f.propose("alice", "ready", "", 57*time.Minute, pay)
	f.vote("alice", ready, true) // 4500 + 3000 = exactly the threshold
	f.vote("bob", ready, true)
	notRejected := f.propose("bob", "deny at the margin", "", 56*time.Minute, pay)
	f.vote("carol", notRejected, false) // 2500 denied: approvals can still reach 7500
	f.vote("dave", notRejected, false)
	rejected := f.propose("bob", "rejected", "", 55*time.Minute, pay)
	f.vote("bob", rejected, false) // 3000 denied: at most 7000 can approve
	executed := f.propose("carol", "executed", "", 54*time.Minute, pay)
	f.hist.Executed[executed] = f.props.Time

	v := f.view()
	for id, want := range map[string]Status{open: Open, ready: Ready, notRejected: Open, rejected: Rejected, executed: Executed} {
		if got := find(t, v, id).Status; got != want {
			t.Errorf("%s: status %v, want %v", f.stored(id).T, got, want)
		}
	}
	if got := find(t, v, open).Approve; got != 7000 {
		t.Errorf("open approvals = %d, want 7000", got)
	}
	if got := find(t, v, notRejected).Deny; got != 2500 {
		t.Errorf("deny tally = %d, want 2500", got)
	}

	// At (not after) the expiration Steem no longer accepts the transaction.
	f.now = testNow.Add(57 * time.Minute)
	v = f.view()
	for id, want := range map[string]Status{open: Open, ready: Expired, executed: Executed} {
		if got := find(t, v, id).Status; got != want {
			t.Errorf("at +57m %s: status %v, want %v", f.stored(id).T, got, want)
		}
	}
}

func TestBuildViewVerification(t *testing.T) {
	f := newFixture(t)
	pay := transferOp("rcpt", "1.000 STEEM", "svm-withdrawal 1")
	id := f.propose("alice", "good", "", 58*time.Minute, pay)
	f.vote("alice", id, true)
	f.vote("bob", id, true, "dave") // signed with someone else's key

	// Stored under an ID that doesn't match the transaction: dropped.
	forged := f.propose("alice", "forged", "", 50*time.Minute, pay)
	f.ballots["alice"].Props[strings.Repeat("0", 40)] = f.stored(forged)
	delete(f.ballots["alice"].Props, forged)
	// From a non-member: dropped.
	outsider := f.propose("mallory", "outsider", "", 49*time.Minute, pay)
	// Not acting on svm.bank: dropped.
	other := f.propose("alice", "other account", "", 48*time.Minute,
		steemtx.Operation{Type: "transfer", Value: steemtx.Transfer{From: "alice", To: "rcpt", Amount: "1.000 STEEM", Memo: "x"}})

	v := f.view()
	if len(v.Proposal) != 1 || v.Proposal[0].ID != id {
		t.Fatalf("view has %d proposals, want only the good one", len(v.Proposal))
	}
	p := v.Proposal[0]
	if !p.Votes["alice"].Valid || p.Votes["bob"].Valid {
		t.Fatalf("validity: alice %v bob %v, want true false", p.Votes["alice"].Valid, p.Votes["bob"].Valid)
	}
	if p.Approve != 4500 || p.Status != Open {
		t.Fatalf("approve %d status %v, want 4500 open (bob's forged signature must not count)", p.Approve, p.Status)
	}
	_, _ = outsider, other // present in ballots, absent from the view
}

func TestBuildViewOwnerAuthority(t *testing.T) {
	f := newFixture(t)
	// Owner and active differ: an owner change is tallied against owner weights.
	f.bank.Active = bankAuthority([]steemtx.AccountAuth{{Name: "alice", Weight: 1000}, {Name: "bob", Weight: 9000}})
	d := bankAuthority(testWeights)
	upd := steemtx.Operation{Type: "account_update", Value: steemtx.AccountUpdate{Account: testBank, Owner: &d, Active: &d, Posting: &d, MemoKey: f.bank.MemoKey}}
	id := f.propose("alice", "owner change", "", 58*time.Minute, upd)
	f.vote("alice", id, true)
	f.vote("bob", id, true)
	p := find(t, f.view(), id)
	if !p.Owner || p.Approve != 7500 || p.Status != Ready {
		t.Fatalf("owner %v approve %d status %v, want owner tally 7500 ready", p.Owner, p.Approve, p.Status)
	}
	pay := f.propose("alice", "payout", "", 57*time.Minute, transferOp("rcpt", "1.000 STEEM", "svm-withdrawal 1"))
	f.vote("alice", pay, true)
	if p := find(t, f.view(), pay); p.Owner || p.Approve != 1000 {
		t.Fatalf("payout owner %v approve %d, want active tally 1000", p.Owner, p.Approve)
	}
}

func TestMinimalSignatures(t *testing.T) {
	pay := transferOp("rcpt", "1.000 STEEM", "svm-withdrawal 1")
	cases := []struct {
		name     string
		approve  []string
		wantSigs []string // in authority (sorted) order; nil = not enough
	}{
		{"exactly the threshold", []string{"alice", "bob"}, []string{"alice", "bob"}},
		{"stops at the threshold", []string{"alice", "bob", "carol", "dave"}, []string{"alice", "bob"}},
		{"skips non-approvers", []string{"dave", "carol", "alice"}, nil}, // 7000
		{"walks in chain order", []string{"dave", "bob", "alice"}, []string{"alice", "bob"}},
		{"needs everyone but alice", []string{"bob", "carol", "dave"}, nil}, // 5500
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			id := f.propose("alice", "p", "", 58*time.Minute, pay)
			for _, m := range tc.approve {
				f.vote(m, id, true)
			}
			v := f.view()
			p := find(t, v, id)
			sigs := v.MinimalSignatures(p)
			if tc.wantSigs == nil {
				if sigs != nil {
					t.Fatalf("got %d signatures, want none", len(sigs))
				}
				return
			}
			if len(sigs) != len(tc.wantSigs) {
				t.Fatalf("got %d signatures, want %d", len(sigs), len(tc.wantSigs))
			}
			for i, name := range tc.wantSigs {
				pub, err := steemtx.RecoverSigner(p.Digest, sigs[i])
				if err != nil || pub != pubOf(name) {
					t.Fatalf("signature %d recovers to %s, want %s's key", i, pub, name)
				}
			}
		})
	}
}
