package multisig

import (
	"context"
	"crypto/sha256"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"

	"steemvm/oracle/go/steemtx"
)

const testBank = "svm.bank"

// testNow is the fake Steem head time; real wall-clock based so the signer's
// own wall-clock checks (execution cutoff) agree with it.
var testNow = time.Now().UTC().Truncate(time.Second)

// fakeSVM is an in-memory SVM seen by one validator (self). Withdrawal lookups
// see every status; the requested list is derived from it.
type fakeSVM struct {
	self string
	*svmData
}

// svmData is the chain state shared by every validator's view.
type svmData struct {
	height  int64
	timeout uint64
	all     map[uint64]Withdrawal
	stakes  map[int64][]StakeEntry
	err     error // returned by Withdrawal lookups when set
}

func (f *fakeSVM) Height(context.Context) (int64, error)                 { return f.height, nil }
func (f *fakeSVM) WithdrawalTimeoutBlocks(context.Context) (uint64, error) { return f.timeout, nil }
func (f *fakeSVM) RequestedWithdrawals(context.Context) ([]Withdrawal, error) {
	var out []Withdrawal
	for _, w := range f.all {
		if w.Status == "REQUESTED" {
			out = append(out, w)
		}
	}
	return out, nil
}

func (f *fakeSVM) Withdrawal(_ context.Context, id uint64) (Withdrawal, bool, error) {
	if f.err != nil {
		return Withdrawal{}, false, f.err
	}
	w, ok := f.all[id]
	return w, ok, nil
}

func (f *fakeSVM) ValidatorStakes(_ context.Context, h int64) ([]StakeEntry, error) {
	s, ok := f.stakes[h]
	if !ok {
		return nil, errors.New("height pruned")
	}
	return s, nil
}

func (f *fakeSVM) SelfSteemAccount(context.Context) (string, error) { return f.self, nil }

// as returns a view of the same SVM state for another validator.
func (f *fakeSVM) as(self string) *fakeSVM { return &fakeSVM{self: self, svmData: f.svmData} }

func stakes(kv ...any) []StakeEntry {
	var out []StakeEntry
	for i := 0; i < len(kv); i += 2 {
		out = append(out, StakeEntry{Account: kv[i].(string), Tokens: big.NewInt(int64(kv[i+1].(int)))})
	}
	return out
}

func testKey(name string) *secp256k1.PrivateKey {
	seed := sha256.Sum256([]byte("svm.bank test key " + name))
	return secp256k1.PrivKeyFromBytes(seed[:])
}

func testWIF(k *secp256k1.PrivateKey) string {
	payload := append([]byte{0x80}, k.Serialize()...)
	sum := sha256.Sum256(payload)
	sum = sha256.Sum256(sum[:])
	return base58.Encode(append(payload, sum[:4]...))
}

func pubOf(name string) string { return steemtx.EncodePublicKey(testKey(name).PubKey()) }

func keyAuthority(pub string) steemtx.Authority {
	return steemtx.Authority{WeightThreshold: 1, AccountAuths: []steemtx.AccountAuth{}, KeyAuths: []steemtx.KeyAuth{{Key: pub, Weight: 1}}}
}

// fixture: svm.bank owned by alice 4500, bob 3000, carol 1500, dave 1000
// (threshold 7500, so alice+bob is exactly enough); SVM at height 100000 with
// the 3.5-day withdrawal timeout; stakes at the 99000 checkpoint reproduce the
// current weights, so no reweight is due unless a test changes them.
type fixture struct {
	t       *testing.T
	members map[string]Account
	bank    Account
	props   steemtx.DynamicProps
	now     time.Time
	svm     *fakeSVM
	hist    *HistoryIndex
	ballots map[string]Ballot
}

var testWeights = []steemtx.AccountAuth{{Name: "alice", Weight: 4500}, {Name: "bob", Weight: 3000}, {Name: "carol", Weight: 1500}, {Name: "dave", Weight: 1000}}

func bankAuthority(weights []steemtx.AccountAuth) steemtx.Authority {
	return steemtx.Authority{WeightThreshold: 7500, AccountAuths: append([]steemtx.AccountAuth(nil), weights...), KeyAuths: []steemtx.KeyAuth{}}.Sorted()
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	auth := bankAuthority(testWeights)
	f := &fixture{
		t:       t,
		members: map[string]Account{},
		bank: Account{Name: testBank, Owner: auth, Active: auth, Posting: auth, MemoKey: pubOf("memo"),
			Balance: "1000.000 STEEM", SBDBalance: "50.000 SBD"},
		props: steemtx.DynamicProps{HeadBlockNumber: 100000, HeadBlockID: "000186a0" + strings.Repeat("ab", 16),
			Time: testNow.Format(steemtx.TimeLayout)},
		now:     testNow,
		hist:    &HistoryIndex{Paid: map[string]string{}, Executed: map[string]string{}},
		ballots: map[string]Ballot{},
		svm: &fakeSVM{svmData: &svmData{height: 100000, timeout: 50400, all: map[uint64]Withdrawal{},
			stakes: map[int64][]StakeEntry{99000: stakes("alice", 45, "bob", 30, "carol", 15, "dave", 10)}}},
	}
	for _, w := range testWeights {
		f.members[w.Name] = Account{Name: w.Name, Active: keyAuthority(pubOf(w.Name))}
		f.ballots[w.Name] = Ballot{Props: map[string]StoredProposal{}, Votes: map[string]Vote{}}
	}
	return f
}

func transferOp(to, amount, memo string) steemtx.Operation {
	return steemtx.Operation{Type: "transfer", Value: steemtx.Transfer{From: testBank, To: to, Amount: amount, Memo: memo}}
}

func (f *fixture) withdrawal(id uint64, dest string, milli uint64, sym string, createdAt uint64) {
	f.svm.all[id] = Withdrawal{ID: id, Destination: dest, AmountMilli: milli, Symbol: sym, CreatedAt: createdAt, Status: "REQUESTED"}
}

// propose stores a proposal in creator's ballot. ttl varies the transaction
// (and so its ID) between otherwise identical proposals.
func (f *fixture) propose(creator, title, created string, ttl time.Duration, ops ...steemtx.Operation) string {
	f.t.Helper()
	tx, err := steemtx.NewTransaction(f.props, ops, ttl)
	if err != nil {
		f.t.Fatal(err)
	}
	id, err := tx.ID()
	if err != nil {
		f.t.Fatal(err)
	}
	if created == "" {
		created = f.props.Time
	}
	if _, ok := f.ballots[creator]; !ok {
		f.ballots[creator] = Ballot{Props: map[string]StoredProposal{}, Votes: map[string]Vote{}}
	}
	f.ballots[creator].Props[id] = StoredProposal{T: title, Tx: tx, C: created}
	return id
}

func (f *fixture) stored(id string) StoredProposal {
	f.t.Helper()
	for _, b := range f.ballots {
		if sp, ok := b.Props[id]; ok {
			return sp
		}
	}
	f.t.Fatalf("no stored proposal %s", id)
	return StoredProposal{}
}

// vote records voter's vote, signing approvals with signer's key (normally
// the voter's own).
func (f *fixture) vote(voter, id string, approve bool, signer ...string) {
	f.t.Helper()
	sp := f.stored(id)
	if !approve {
		f.ballots[voter].Votes[id] = Vote{A: 0, X: sp.Tx.Expiration}
		return
	}
	digest, err := sp.Tx.Digest()
	if err != nil {
		f.t.Fatal(err)
	}
	keyName := voter
	if len(signer) > 0 {
		keyName = signer[0]
	}
	sig, err := steemtx.Sign(testKey(keyName), digest)
	if err != nil {
		f.t.Fatal(err)
	}
	f.ballots[voter].Votes[id] = Vote{A: 1, S: sig, X: sp.Tx.Expiration}
}

func (f *fixture) view() *View {
	return BuildView(f.now, f.props, f.bank, f.members, f.ballots, f.hist)
}

func (f *fixture) state() *SVMState {
	st := &SVMState{Height: f.svm.height, Timeout: f.svm.timeout, Requested: map[uint64]Withdrawal{}}
	ws, _ := f.svm.RequestedWithdrawals(context.Background())
	for _, w := range ws {
		st.Requested[w.ID] = w
	}
	return st
}

func (f *fixture) policy(self string) *Policy {
	return &Policy{Cfg: DefaultConfig(), SVM: f.svm, Self: self}
}

func find(t *testing.T, v *View, id string) *Proposal {
	t.Helper()
	for _, p := range v.Proposal {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("proposal %s not in view", id)
	return nil
}
