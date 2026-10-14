package multisig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"steemvm/oracle/go/steemtx"
)

func TestMetadataRoundTrip(t *testing.T) {
	f := newFixture(t)
	id := f.propose("alice", "Withdrawal 1: 1.000 STEEM to @rcpt", "", 58*time.Minute, transferOp("rcpt", "1.000 STEEM", "svm-withdrawal 1"))
	f.vote("alice", id, true)

	// Other keys must survive byte for byte: no reordering inside them, no
	// HTML escaping of <, > or &.
	profile := `{"profile":{"name":"Alice <3","about":"a & b","z":1,"a":[1, 2]}}`
	raw, b, err := ParseMetadata(profile)
	if err != nil || len(b.Props) != 0 || len(b.Votes) != 0 {
		t.Fatalf("parse: %v %+v", err, b)
	}
	b.Props[id] = f.stored(id)
	b.Votes[id] = f.ballots["alice"].Votes[id]
	out, err := EncodeMetadata(raw, b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, `{"profile":{"name":"Alice <3","about":"a & b","z":1,"a":[1, 2]},"svm_ms":{`) {
		t.Fatalf("other keys not preserved: %s", out)
	}

	raw2, b2, err := ParseMetadata(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw2["profile"]) != string(raw["profile"]) {
		t.Fatalf("profile changed: %s", raw2["profile"])
	}
	sp, ok := b2.Props[id]
	if !ok {
		t.Fatal("proposal lost in the round trip")
	}
	if got, _ := sp.Tx.ID(); got != id || sp.T != b.Props[id].T || sp.C != b.Props[id].C {
		t.Fatalf("proposal changed in the round trip: %s %+v", got, sp)
	}
	if b2.Votes[id] != b.Votes[id] {
		t.Fatalf("vote changed: %+v", b2.Votes[id])
	}
	// Re-encoding is stable.
	if again, _ := EncodeMetadata(raw2, b2); again != out {
		t.Fatalf("re-encoding differs:\n%s\n%s", out, again)
	}
}

func TestParseMetadataRejects(t *testing.T) {
	for _, s := range []string{"[1,2]", "{bad", `"string"`, "42"} {
		if _, _, err := ParseMetadata(s); !errors.Is(err, ErrInvalidMetadata) {
			t.Errorf("%q: err %v, want ErrInvalidMetadata", s, err)
		}
	}
	for _, s := range []string{"", "  ", "{}", "null"} {
		raw, b, err := ParseMetadata(s)
		if err != nil || raw == nil || b.Props == nil || b.Votes == nil {
			t.Errorf("%q: %v (raw nil %v)", s, err, raw == nil)
		}
	}
	// A malformed svm_ms is an error (never overwrite it blindly) but the other
	// keys are still returned.
	raw, _, err := ParseMetadata(`{"profile":{},"svm_ms":"x"}`)
	if err == nil || raw["profile"] == nil {
		t.Fatalf("malformed svm_ms: err %v raw %v", err, raw)
	}
	// An unsupported operation in someone's proposal is skipped, not fatal.
	_, b, err := ParseMetadata(`{"svm_ms":{"props":{"x":{"t":"t","c":"c","tx":{"ref_block_num":1,"ref_block_prefix":1,"expiration":"2026-01-01T00:00:00","operations":[["vote",{}]],"extensions":[]}}},"votes":{}}}`)
	if err != nil || len(b.Props) != 0 {
		t.Fatalf("unsupported op: err %v props %d", err, len(b.Props))
	}
}

func TestBallotPrune(t *testing.T) {
	now := testNow
	ts := func(d time.Duration) string { return now.Add(d).Format(steemtx.TimeLayout) }
	b := Ballot{Props: map[string]StoredProposal{
		"oldp":   {Tx: steemtx.Transaction{Expiration: ts(-73 * time.Hour)}},
		"recentp": {Tx: steemtx.Transaction{Expiration: ts(-71 * time.Hour)}},
	}, Votes: map[string]Vote{
		"oldv":    {A: 0, X: ts(-73 * time.Hour)},
		"recentv": {A: 0, X: ts(-71 * time.Hour)},
		"livev":   {A: 1, S: "sig", X: ts(time.Hour)},
	}}
	b.Prune(now)
	for _, id := range []string{"oldp", "oldv"} {
		if _, ok := b.Props[id]; ok {
			t.Errorf("%s kept", id)
		}
		if _, ok := b.Votes[id]; ok {
			t.Errorf("%s kept", id)
		}
	}
	if _, ok := b.Props["recentp"]; !ok {
		t.Error("recentp dropped")
	}
	if len(b.Votes) != 2 {
		t.Errorf("votes %v", b.Votes)
	}

	// Over the size cap: oldest expired entries go first, live ones never.
	big := Ballot{Props: map[string]StoredProposal{}, Votes: map[string]Vote{}}
	sig := strings.Repeat("ab", 65)
	for i := 0; i < 400; i++ {
		big.Votes[fmt.Sprintf("%040d", i)] = Vote{A: 1, S: sig, X: ts(-time.Duration(i+1) * time.Minute)}
	}
	for i := 0; i < 20; i++ {
		big.Votes[fmt.Sprintf("live%036d", i)] = Vote{A: 1, S: sig, X: ts(time.Hour)}
	}
	big.Prune(now)
	if enc, _ := marshalNoEscape(big); len(enc) > maxBallotBytes {
		t.Fatalf("still %d bytes", len(enc))
	}
	for i := 0; i < 20; i++ {
		if _, ok := big.Votes[fmt.Sprintf("live%036d", i)]; !ok {
			t.Fatal("dropped a live vote")
		}
	}
	if _, ok := big.Votes[fmt.Sprintf("%040d", 0)]; !ok {
		t.Fatal("dropped the newest expired vote before older ones")
	}
	if _, ok := big.Votes[fmt.Sprintf("%040d", 399)]; ok {
		t.Fatal("kept the oldest expired vote")
	}
}

func TestJournal(t *testing.T) {
	dir := t.TempDir()
	j, err := LoadJournal(dir)
	if err != nil || len(j.Approvals) != 0 {
		t.Fatalf("empty load: %v", err)
	}
	f := newFixture(t)
	a := f.propose("alice", "A", "", 58*time.Minute, transferOp("rcpt", "1.000 STEEM", "svm-withdrawal 1"))
	j.Record(a, f.stored(a).Tx)
	if err := j.Save(dir); err != nil {
		t.Fatal(err)
	}
	j2, err := LoadJournal(dir)
	if err != nil || j2.Approvals[a].Subject != "transfer:svm-withdrawal 1" {
		t.Fatalf("reload: %v %+v", err, j2.Approvals)
	}
	if !j2.Conflicts("transfer:svm-withdrawal 1", "other", testNow) {
		t.Error("no conflict with a different proposal for the same withdrawal")
	}
	if j2.Conflicts("transfer:svm-withdrawal 1", a, testNow) {
		t.Error("a proposal conflicts with itself")
	}
	if j2.Conflicts("transfer:svm-withdrawal 2", "other", testNow) {
		t.Error("conflict across withdrawals")
	}
	if j2.Conflicts("transfer:svm-withdrawal 1", "other", testNow.Add(58*time.Minute)) {
		t.Error("conflict after the approved proposal expired")
	}
	if (*ApprovalJournal)(nil).Conflicts("transfer:svm-withdrawal 1", "x", testNow) {
		t.Error("nil journal conflicts")
	}
	j2.Prune(testNow.Add(58*time.Minute + PruneAfter + time.Second))
	if len(j2.Approvals) != 0 {
		t.Error("expired entry not pruned")
	}

	if err := os.WriteFile(filepath.Join(dir, journalFile), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadJournal(dir); err == nil {
		t.Fatal("a corrupt journal must stop the signer, not be ignored")
	}
}
