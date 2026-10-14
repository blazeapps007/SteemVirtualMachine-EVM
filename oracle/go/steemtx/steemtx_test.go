package steemtx

import (
	"crypto/rand"
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"

	msvectors "steemvm/oracle/multisig"
)

func TestSelftestVectors(t *testing.T) {
	if err := Selftest(msvectors.JSON); err != nil {
		t.Fatal(err)
	}
}

// TestSignRecoverRandom signs many random digests with many random keys: every
// signature must be canonical (header 1f/20) and recover to the signing key.
func TestSignRecoverRandom(t *testing.T) {
	for k := 0; k < 20; k++ {
		key, err := secp256k1.GeneratePrivateKey()
		if err != nil {
			t.Fatal(err)
		}
		want := EncodePublicKey(key.PubKey())
		for i := 0; i < 250; i++ {
			var d [32]byte
			if _, err := rand.Read(d[:]); err != nil {
				t.Fatal(err)
			}
			sig, err := Sign(key, d)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(sig, "1f") && !strings.HasPrefix(sig, "20") {
				t.Fatalf("header %s", sig[:2])
			}
			got, err := RecoverSigner(d, sig)
			if err != nil || got != want {
				t.Fatalf("recover %s (%v), want %s", got, err, want)
			}
		}
	}
}

func TestRecoverRejectsNonCanonical(t *testing.T) {
	d := sha256.Sum256([]byte("x"))
	// r with its high bit set is never canonical.
	bad := "1f" + strings.Repeat("ff", 32) + strings.Repeat("01", 32)
	if _, err := RecoverSigner(d, bad); err == nil {
		t.Fatal("non-canonical signature accepted")
	}
}

func TestParseAssetStrict(t *testing.T) {
	for _, s := range []string{"1 STEEM", "1.0000 STEEM", "1.00 STEEM", "-1.000 STEEM", "+1.000 STEEM", "1.000 BTC", "1,000 STEEM", "1.00a STEEM", " STEEM"} {
		if _, err := ParseAsset(s); err == nil {
			t.Errorf("ParseAsset(%q) accepted", s)
		}
	}
}

func TestSortedOrdersByteWise(t *testing.T) {
	a := Authority{AccountAuths: []AccountAuth{{"blazed007", 1}, {"blaze.apps", 1}, {"a", 1}}}.Sorted()
	got := []string{a.AccountAuths[0].Name, a.AccountAuths[1].Name, a.AccountAuths[2].Name}
	// '.' (0x2e) sorts before 'd' (0x64).
	if strings.Join(got, ",") != "a,blaze.apps,blazed007" {
		t.Fatalf("order %v", got)
	}
}
