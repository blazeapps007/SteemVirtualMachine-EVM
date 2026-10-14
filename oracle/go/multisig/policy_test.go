package multisig

import (
	"context"
	"errors"
	"testing"
	"time"

	"steemvm/oracle/go/steemtx"
)

// Withdrawal 7: 12.345 STEEM net to @rcpt, created at SVM height 90000. With
// the 50400-block timeout the refund fires at 140400, so with the default
// 14400-block margin a payout is allowed up to SVM height 126000 inclusive.
func payoutFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.withdrawal(7, "rcpt", 12345, "STEEM", 90000)
	return f
}

func review(t *testing.T, f *fixture, pol *Policy, id string, myVotes map[string]Vote) (Decision, string) {
	t.Helper()
	v := f.view()
	if myVotes == nil {
		myVotes = map[string]Vote{}
	}
	return pol.Review(context.Background(), find(t, v, id), v, f.state(), myVotes)
}

func TestReviewPayout(t *testing.T) {
	exact := transferOp("rcpt", "12.345 STEEM", "svm-withdrawal 7")
	cases := []struct {
		name   string
		ops    []steemtx.Operation
		setup  func(*fixture)
		want   Decision
		reason string
	}{
		{name: "exact payout", ops: []steemtx.Operation{exact}, want: Approve},
		{name: "margin boundary: last allowed height", ops: []steemtx.Operation{exact}, setup: func(f *fixture) { f.svm.height = 126000 }, want: Approve},
		{name: "margin boundary: one block late", ops: []steemtx.Operation{exact}, setup: func(f *fixture) { f.svm.height = 126001 }, want: Deny},
		{name: "relay memo with leading zero", ops: []steemtx.Operation{transferOp("rcpt", "12.345 STEEM", "svm-withdrawal 07")}, want: Deny},
		{name: "guide's old memo format", ops: []steemtx.Operation{transferOp("rcpt", "12.345 STEEM", "svm-withdraw 7")}, want: Deny},
		{name: "memo trailing space", ops: []steemtx.Operation{transferOp("rcpt", "12.345 STEEM", "svm-withdrawal 7 ")}, want: Deny},
		{name: "wrong recipient", ops: []steemtx.Operation{transferOp("rcpt2", "12.345 STEEM", "svm-withdrawal 7")}, want: Deny},
		{name: "one millisteem more", ops: []steemtx.Operation{transferOp("rcpt", "12.346 STEEM", "svm-withdrawal 7")}, want: Deny},
		{name: "one millisteem less", ops: []steemtx.Operation{transferOp("rcpt", "12.344 STEEM", "svm-withdrawal 7")}, want: Deny},
		{name: "wrong asset", ops: []steemtx.Operation{transferOp("rcpt", "12.345 SBD", "svm-withdrawal 7")}, want: Deny},
		{name: "two operations", ops: []steemtx.Operation{exact, transferOp("rcpt", "0.001 STEEM", "x")}, want: Deny},
		{name: "already paid on Steem", ops: []steemtx.Operation{exact}, setup: func(f *fixture) { f.hist.Paid["7"] = "abc" }, want: Deny},
		{name: "SVM withdrawal processed", ops: []steemtx.Operation{exact}, setup: func(f *fixture) {
			w := f.svm.all[7]
			w.Status = "PROCESSED"
			f.svm.all[7] = w
		}, want: Deny},
		{name: "SVM withdrawal refunded", ops: []steemtx.Operation{exact}, setup: func(f *fixture) {
			w := f.svm.all[7]
			w.Status = "REFUNDED"
			f.svm.all[7] = w
		}, want: Deny},
		{name: "SVM node hasn't seen it yet", ops: []steemtx.Operation{exact}, setup: func(f *fixture) { delete(f.svm.all, 7) }, want: Abstain},
		{name: "SVM read fails", ops: []steemtx.Operation{exact}, setup: func(f *fixture) {
			w := f.svm.all[7]
			w.Status = "PROCESSED" // not in the requested list, so it is looked up
			f.svm.all[7] = w
			f.svm.err = errors.New("connection refused")
		}, want: Abstain},
		{name: "treasury operation needs a human", ops: []steemtx.Operation{{Type: "transfer_to_vesting", Value: steemtx.TransferToVesting{From: testBank, To: testBank, Amount: "1.000 STEEM"}}}, want: Abstain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := payoutFixture(t)
			if tc.setup != nil {
				tc.setup(f)
			}
			id := f.propose("alice", "payout", "", 58*time.Minute, tc.ops...)
			if got, reason := review(t, f, f.policy("bob"), id, nil); got != tc.want {
				t.Fatalf("decision %v (%s), want %v", got, reason, tc.want)
			}
		})
	}
}

func TestReviewPayoutNotFromBank(t *testing.T) {
	f := payoutFixture(t)
	p := &Proposal{ID: "x", Status: Open, Tx: steemtx.Transaction{Operations: []steemtx.Operation{
		{Type: "transfer", Value: steemtx.Transfer{From: "alice", To: "rcpt", Amount: "12.345 STEEM", Memo: "svm-withdrawal 7"}},
	}}}
	if d, _ := f.policy("bob").Review(context.Background(), p, f.view(), f.state(), map[string]Vote{}); d != Deny {
		t.Fatalf("decision %v, want deny", d)
	}
}

func TestReviewPayoutCap(t *testing.T) {
	f := payoutFixture(t)
	id := f.propose("alice", "payout", "", 58*time.Minute, transferOp("rcpt", "12.345 STEEM", "svm-withdrawal 7"))
	pol := f.policy("bob")
	pol.Cfg.MaxPerTxMilli = map[string]uint64{"STEEM": 12344}
	if d, _ := review(t, f, pol, id, nil); d != Abstain {
		t.Fatalf("over cap: %v, want abstain", d)
	}
	pol.Cfg.MaxPerTxMilli["STEEM"] = 12345
	if d, _ := review(t, f, pol, id, nil); d != Approve {
		t.Fatalf("at cap: %v, want approve", d)
	}
}

// Two proposals for one withdrawal must never both be approved by one signer.
func TestReviewDuplicatePayouts(t *testing.T) {
	exact := transferOp("rcpt", "12.345 STEEM", "svm-withdrawal 7")
	earlier := testNow.Add(-time.Minute).Format(steemtx.TimeLayout)

	t.Run("only the earliest correct one", func(t *testing.T) {
		f := payoutFixture(t)
		a := f.propose("alice", "A", earlier, 58*time.Minute, exact)
		b := f.propose("bob", "B", "", 57*time.Minute, exact)
		pol := f.policy("carol")
		if d, r := review(t, f, pol, a, nil); d != Approve {
			t.Fatalf("A: %v (%s), want approve", d, r)
		}
		if d, r := review(t, f, pol, b, nil); d != Abstain {
			t.Fatalf("B: %v (%s), want abstain", d, r)
		}
	})

	t.Run("an earlier wrong one doesn't block a correct one", func(t *testing.T) {
		f := payoutFixture(t)
		f.propose("alice", "wrong amount", earlier, 58*time.Minute, transferOp("rcpt", "99.999 STEEM", "svm-withdrawal 7"))
		b := f.propose("bob", "B", "", 57*time.Minute, exact)
		if d, r := review(t, f, f.policy("carol"), b, nil); d != Approve {
			t.Fatalf("B: %v (%s), want approve", d, r)
		}
	})

	t.Run("my published approval of a later one blocks the earlier", func(t *testing.T) {
		f := payoutFixture(t)
		a := f.propose("alice", "A", earlier, 58*time.Minute, exact)
		b := f.propose("bob", "B", "", 57*time.Minute, exact)
		mine := map[string]Vote{b: {A: 1}}
		if d, _ := review(t, f, f.policy("carol"), a, mine); d != Abstain {
			t.Fatalf("A: %v, want abstain", d)
		}
	})

	t.Run("an approval on a rejected but unexpired proposal still blocks", func(t *testing.T) {
		f := payoutFixture(t)
		a := f.propose("alice", "A", earlier, 58*time.Minute, exact)
		f.vote("bob", a, false) // 3000 denied: rejected in the view, yet still executable if bob flips
		b := f.propose("bob", "B", "", 57*time.Minute, exact)
		if find(t, f.view(), a).Status != Rejected {
			t.Fatal("A should be rejected")
		}
		if d, _ := review(t, f, f.policy("carol"), b, map[string]Vote{a: {A: 1}}); d != Abstain {
			t.Fatalf("B: %v, want abstain", d)
		}
	})

	t.Run("an expired approval no longer blocks", func(t *testing.T) {
		f := payoutFixture(t)
		a := f.propose("alice", "A", earlier, time.Minute, exact)
		b := f.propose("bob", "B", "", 57*time.Minute, exact)
		f.now = testNow.Add(time.Minute)
		if find(t, f.view(), a).Status != Expired {
			t.Fatal("A should be expired")
		}
		if d, r := review(t, f, f.policy("carol"), b, map[string]Vote{a: {A: 1}}); d != Approve {
			t.Fatalf("B: %v (%s), want approve", d, r)
		}
	})

	t.Run("the local journal blocks even when the view lags", func(t *testing.T) {
		f := payoutFixture(t)
		b := f.propose("bob", "B", "", 57*time.Minute, exact)
		pol := f.policy("carol")
		// An approval of a proposal this (stale) view doesn't show at all.
		pol.Journal = &ApprovalJournal{Approvals: map[string]JournalEntry{
			"unseen": {Subject: "transfer:svm-withdrawal 7", Expiration: testNow.Add(time.Minute).Format(steemtx.TimeLayout)},
		}}
		if d, _ := review(t, f, pol, b, nil); d != Abstain {
			t.Fatalf("B with a live journal entry: %v, want abstain", d)
		}
		pol.Journal.Approvals["unseen"] = JournalEntry{Subject: "transfer:svm-withdrawal 7", Expiration: f.props.Time}
		if d, r := review(t, f, pol, b, nil); d != Approve {
			t.Fatalf("B once the journal entry expired: %v (%s), want approve", d, r)
		}
		// Its own approval is not a conflict (re-signing after a lost publish).
		pol.Journal.Record(b, f.stored(b).Tx)
		if d, r := review(t, f, pol, b, nil); d != Approve {
			t.Fatalf("B re-review: %v (%s), want approve", d, r)
		}
	})
}

// validator-set fixture: stakes at the 99000 checkpoint now give alice 5000,
// bob 3000, carol 1500, dave 500 (dave moves 500 points: over tolerance).
func reweightFixture(t *testing.T) (*fixture, steemtx.Authority) {
	f := newFixture(t)
	f.svm.stakes[99000] = stakes("alice", 50, "bob", 30, "carol", 15, "dave", 5)
	want := bankAuthority([]steemtx.AccountAuth{{Name: "alice", Weight: 5000}, {Name: "bob", Weight: 3000}, {Name: "carol", Weight: 1500}, {Name: "dave", Weight: 500}})
	return f, want
}

func updateOp(f *fixture, owner, active, posting *steemtx.Authority) steemtx.Operation {
	return steemtx.Operation{Type: "account_update", Value: steemtx.AccountUpdate{
		Account: testBank, Owner: owner, Active: active, Posting: posting, MemoKey: f.bank.MemoKey}}
}

func TestReviewValidatorSet(t *testing.T) {
	f, want := reweightFixture(t)
	if got, err := f.policy("bob").DesiredAt(context.Background(), 99000); err != nil || !got.Equal(want) {
		t.Fatalf("DesiredAt = %+v, %v; want %+v", got, err, want)
	}
	off := bankAuthority([]steemtx.AccountAuth{{Name: "alice", Weight: 5001}, {Name: "bob", Weight: 2999}, {Name: "carol", Weight: 1500}, {Name: "dave", Weight: 500}})
	otherMemo := updateOp(f, &want, &want, &want)
	u := otherMemo.Value.(steemtx.AccountUpdate)
	u.MemoKey = pubOf("attacker")
	otherMemo.Value = u

	cases := []struct {
		name, title string
		op          steemtx.Operation
		setup       func(*fixture)
		want        Decision
	}{
		{"exact set", "Validator set @svm:99000", updateOp(f, &want, &want, &want), nil, Approve},
		{"one point off", "Validator set @svm:99000", updateOp(f, &off, &off, &off), nil, Deny},
		{"owner only", "Validator set @svm:99000", updateOp(f, &want, nil, nil), nil, Deny},
		{"memo key changed", "Validator set @svm:99000", otherMemo, nil, Deny},
		{"no snapshot in title", "Validator set", updateOp(f, &want, &want, &want), nil, Deny},
		{"snapshot not a checkpoint", "Validator set @svm:99500", updateOp(f, &want, &want, &want), nil, Deny},
		{"snapshot too old", "Validator set @svm:96000", updateOp(f, &want, &want, &want), nil, Deny},
		{"snapshot ahead of this node", "Validator set @svm:101000", updateOp(f, &want, &want, &want), nil, Abstain},
		{"snapshot height pruned", "Validator set @svm:98000", updateOp(f, &want, &want, &want), nil, Abstain},
		{"already the current set", "Validator set @svm:99000", updateOp(f, &want, &want, &want), func(f *fixture) {
			f.bank.Owner, f.bank.Active, f.bank.Posting = want, want, want
		}, Deny},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := reweightFixture(t)
			if tc.setup != nil {
				tc.setup(f)
			}
			id := f.propose("alice", tc.title, "", 58*time.Minute, tc.op)
			if got, reason := review(t, f, f.policy("bob"), id, nil); got != tc.want {
				t.Fatalf("decision %v (%s), want %v", got, reason, tc.want)
			}
		})
	}
}

func intentKeys(in []Intent) []string {
	var out []string
	for _, i := range in {
		out = append(out, i.Key)
	}
	return out
}

func TestIntentsPayoutProposer(t *testing.T) {
	// Withdrawal 6: members sorted alice,bob,carol,dave; 6 mod 4 = 2, so carol
	// is designated, dave steps in after 120s and alice after 240s.
	f := newFixture(t)
	f.withdrawal(6, "rcpt", 1000, "STEEM", 90000)
	t0 := time.Now()
	ctx := context.Background()
	for _, tc := range []struct {
		self  string
		after time.Duration
		want  int
	}{
		{"carol", 0, 1}, {"dave", 0, 0}, {"dave", 119 * time.Second, 0}, {"dave", 120 * time.Second, 1},
		{"alice", 120 * time.Second, 0}, {"alice", 240 * time.Second, 1}, {"mallory", time.Hour, 0},
	} {
		seen := map[string]time.Time{"w:6": t0}
		got, err := f.policy(tc.self).Intents(ctx, f.view(), f.state(), seen, t0.Add(tc.after))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != tc.want {
			t.Fatalf("%s after %v: %d intents %v, want %d", tc.self, tc.after, len(got), intentKeys(got), tc.want)
		}
	}

	seen := map[string]time.Time{}
	got, _ := f.policy("carol").Intents(ctx, f.view(), f.state(), seen, t0)
	op := got[0].Ops[0].Value.(steemtx.Transfer)
	if len(got[0].Ops) != 1 || op != (steemtx.Transfer{From: testBank, To: "rcpt", Amount: "1.000 STEEM", Memo: "svm-withdrawal 6"}) {
		t.Fatalf("intent op %+v", got[0].Ops)
	}
	// What a proposer builds, every other member approves.
	id := f.propose("carol", got[0].Title, "", 58*time.Minute, got[0].Ops...)
	if d, r := review(t, f, f.policy("alice"), id, nil); d != Approve {
		t.Fatalf("review of our own intent: %v (%s)", d, r)
	}
	// While it is live, nobody proposes it again and the fallback clock resets.
	seen["w:6"] = t0.Add(-time.Hour)
	if got, _ := f.policy("dave").Intents(ctx, f.view(), f.state(), seen, t0); len(got) != 0 {
		t.Fatalf("re-proposed a live withdrawal: %v", intentKeys(got))
	}
	if _, ok := seen["w:6"]; ok {
		t.Fatal("fallback clock not reset while a proposal is live")
	}
}

func TestIntentsPayoutSkips(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		setup func(*fixture, *Policy)
	}{
		{"balance short by one millisteem", func(f *fixture, _ *Policy) { f.bank.Balance = "0.999 STEEM" }},
		{"inside the refund margin", func(f *fixture, _ *Policy) { f.svm.height = 126001 }},
		{"already paid", func(f *fixture, _ *Policy) { f.hist.Paid["6"] = "abc" }},
		{"over the cap", func(_ *fixture, p *Policy) { p.Cfg.MaxPerTxMilli = map[string]uint64{"STEEM": 999} }},
		{"journal holds an unexpired approval", func(f *fixture, p *Policy) {
			p.Journal = &ApprovalJournal{Approvals: map[string]JournalEntry{
				"x": {Subject: "transfer:svm-withdrawal 6", Expiration: testNow.Add(time.Minute).Format(steemtx.TimeLayout)}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.withdrawal(6, "rcpt", 1000, "STEEM", 90000)
			f.svm.stakes[126000] = f.svm.stakes[99000]
			f.svm.stakes[125000] = f.svm.stakes[99000]
			pol := f.policy("carol")
			tc.setup(f, pol)
			got, err := pol.Intents(ctx, f.view(), f.state(), map[string]time.Time{}, time.Now())
			if err != nil || len(got) != 0 {
				t.Fatalf("intents %v, err %v; want none", intentKeys(got), err)
			}
		})
	}
}

func TestIntentsValidatorSet(t *testing.T) {
	ctx := context.Background()
	// Checkpoint(100000) = 99000; 99 mod 4 = 3, so dave is designated.
	f, want := reweightFixture(t)
	got, err := f.policy("dave").Intents(ctx, f.view(), f.state(), map[string]time.Time{}, time.Now())
	if err != nil || len(got) != 1 || got[0].Key != "v:99000" || got[0].Title != "Validator set @svm:99000" {
		t.Fatalf("intents %v (%v), want v:99000", intentKeys(got), err)
	}
	u := got[0].Ops[0].Value.(steemtx.AccountUpdate)
	if !u.Owner.Equal(want) || !u.Active.Equal(want) || !u.Posting.Equal(want) || u.MemoKey != f.bank.MemoKey || u.JSONMetadata != "" {
		t.Fatalf("intent update %+v", u)
	}
	id := f.propose("dave", got[0].Title, "", 58*time.Minute, got[0].Ops...)
	if d, r := review(t, f, f.policy("alice"), id, nil); d != Approve {
		t.Fatalf("review of the reweight intent: %v (%s)", d, r)
	}

	// Steem allows one owner change per hour.
	f, _ = reweightFixture(t)
	f.hist.LastOwnerChange = testNow.Add(-59 * time.Minute).Format(steemtx.TimeLayout)
	if got, _ := f.policy("dave").Intents(ctx, f.view(), f.state(), map[string]time.Time{}, time.Now()); len(got) != 0 {
		t.Fatalf("proposed inside the owner cooldown: %v", intentKeys(got))
	}
	f.hist.LastOwnerChange = testNow.Add(-61 * time.Minute).Format(steemtx.TimeLayout)
	if got, _ := f.policy("dave").Intents(ctx, f.view(), f.state(), map[string]time.Time{}, time.Now()); len(got) != 1 {
		t.Fatalf("after the cooldown: %v, want one", intentKeys(got))
	}

	// Within tolerance (the default fixture): nothing to do.
	f = newFixture(t)
	if got, _ := f.policy("dave").Intents(ctx, f.view(), f.state(), map[string]time.Time{}, time.Now()); len(got) != 0 {
		t.Fatalf("proposed a no-op reweight: %v", intentKeys(got))
	}
}
