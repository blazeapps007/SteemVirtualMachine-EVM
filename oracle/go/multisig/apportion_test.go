package multisig

import (
	"math/big"
	"testing"

	"steemvm/oracle/go/steemtx"
	msvectors "steemvm/oracle/multisig"
)

func TestApportionVectors(t *testing.T) {
	if err := SelftestApportion(msvectors.JSON); err != nil {
		t.Fatal(err)
	}
}

func TestApportionRejectsEmptyAndBadThreshold(t *testing.T) {
	if _, err := Apportion(nil, 75); err == nil {
		t.Fatal("empty input accepted")
	}
	if _, err := Apportion([]StakeEntry{{"a", big.NewInt(1)}}, 0); err == nil {
		t.Fatal("threshold 0 accepted")
	}
	if _, err := Apportion([]StakeEntry{{"a", big.NewInt(1)}, {"a", big.NewInt(2)}}, 75); err == nil {
		t.Fatal("duplicate account accepted")
	}
}

// TestApportionAlwaysSumsTo10000 hammers the algorithm with awkward stakes:
// every result must sum to exactly 10000 with weights in [1, 65535].
func TestApportionAlwaysSumsTo10000(t *testing.T) {
	e18 := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	for n := 1; n <= 45; n++ {
		var entries []StakeEntry
		for i := 0; i < n; i++ {
			tok := new(big.Int).Mul(big.NewInt(int64(i*i*7919+13)), e18)
			tok.Add(tok, big.NewInt(int64(i))) // break exact multiples
			entries = append(entries, StakeEntry{Account: "v" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Tokens: tok})
		}
		a, err := Apportion(entries, 75)
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if a.Total() != TotalWeight {
			t.Fatalf("n=%d: total %d", n, a.Total())
		}
		if a.WeightThreshold != 7500 {
			t.Fatalf("n=%d: threshold %d", n, a.WeightThreshold)
		}
		want := n
		if want > MaxAccountAuths {
			want = MaxAccountAuths
		}
		if len(a.AccountAuths) != want {
			t.Fatalf("n=%d: %d accounts", n, len(a.AccountAuths))
		}
	}
}

func TestExceedsTolerance(t *testing.T) {
	cur := steemtx.Authority{WeightThreshold: 7500, AccountAuths: []steemtx.AccountAuth{{Name: "a", Weight: 5000}, {Name: "b", Weight: 5000}}}
	same := cur
	if ExceedsTolerance(cur, same, 50) {
		t.Fatal("identical authority flagged")
	}
	small := steemtx.Authority{WeightThreshold: 7500, AccountAuths: []steemtx.AccountAuth{{Name: "a", Weight: 5040}, {Name: "b", Weight: 4960}}}
	if ExceedsTolerance(cur, small, 50) {
		t.Fatal("40-point move flagged at tolerance 50")
	}
	big := steemtx.Authority{WeightThreshold: 7500, AccountAuths: []steemtx.AccountAuth{{Name: "a", Weight: 5051}, {Name: "b", Weight: 4949}}}
	if !ExceedsTolerance(cur, big, 50) {
		t.Fatal("51-point move not flagged")
	}
	swapped := steemtx.Authority{WeightThreshold: 7500, AccountAuths: []steemtx.AccountAuth{{Name: "a", Weight: 5000}, {Name: "c", Weight: 5000}}}
	if !ExceedsTolerance(cur, swapped, 50) {
		t.Fatal("membership change not flagged")
	}
}
