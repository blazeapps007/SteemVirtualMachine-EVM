package multisig

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"

	"steemvm/oracle/go/steemtx"
)

// Decision is the signer's verdict on a proposal.
type Decision int

const (
	Abstain Decision = iota // don't vote; a human reviews it (or retry next pass)
	Approve
	Deny
)

func (d Decision) String() string { return [...]string{"abstain", "approve", "deny"}[d] }

// Config holds the policy parameters. Every validator should run identical
// values for the rules that must agree across signers (threshold, tolerance,
// snapshot spacing and age).
type Config struct {
	BankAccount        string            // "svm.bank"
	ThresholdPercent   uint32            // 75
	ReweightTolerance  uint16            // 50 weight points = 0.5 percentage points
	PayoutMarginBlocks uint64            // never pay within this many SVM blocks of the automatic refund
	SnapshotEvery      int64             // validator-set snapshot spacing (SVM blocks)
	SnapshotMaxAge     int64             // reviewers reject snapshots older than this
	SnapshotLag        int64             // proposers snapshot this far behind the tip
	OwnerCooldown      time.Duration     // Steem allows one owner change per hour
	FallbackDelay      time.Duration     // per-rank wait before a non-designated member proposes
	MaxPerTxMilli      map[string]uint64 // optional per-transaction caps by symbol (unset = none)
}

// DefaultConfig returns the protocol defaults (oracle/PROTOCOL.md).
func DefaultConfig() Config {
	return Config{
		BankAccount:        "svm.bank",
		ThresholdPercent:   75,
		ReweightTolerance:  50,
		PayoutMarginBlocks: 14400,
		SnapshotEvery:      1000,
		SnapshotMaxAge:     3000,
		SnapshotLag:        10,
		OwnerCooldown:      time.Hour,
		FallbackDelay:      120 * time.Second,
	}
}

// SVMState is the SVM data read once per pass.
type SVMState struct {
	Height    int64
	Timeout   uint64
	Requested map[uint64]Withdrawal
}

// Intent is a transaction this signer wants svm.bank to make.
type Intent struct {
	Key   string
	Title string
	Ops   []steemtx.Operation
}

// Policy decides on proposals and produces intents.
type Policy struct {
	Cfg     Config
	SVM     SVM
	Self    string
	Journal *ApprovalJournal // this signer's own signed approvals (nil = none)
	cache   map[int64]steemtx.Authority
}

var snapshotRe = regexp.MustCompile(`@svm:(\d+)`)

// DesiredAt is the stake-proportional authority at an SVM snapshot height.
func (p *Policy) DesiredAt(ctx context.Context, height int64) (steemtx.Authority, error) {
	if p.cache == nil {
		p.cache = map[int64]steemtx.Authority{}
	}
	if a, ok := p.cache[height]; ok {
		return a, nil
	}
	stakes, err := p.SVM.ValidatorStakes(ctx, height)
	if err != nil {
		return steemtx.Authority{}, err
	}
	a, err := Apportion(stakes, p.Cfg.ThresholdPercent)
	if err != nil {
		return steemtx.Authority{}, err
	}
	if len(p.cache) > 16 {
		p.cache = map[int64]steemtx.Authority{}
	}
	p.cache[height] = a
	return a, nil
}

// Checkpoint is the snapshot height a proposer uses at the given SVM tip.
func (p *Policy) Checkpoint(tip int64) int64 {
	s := tip - p.Cfg.SnapshotLag
	if s <= 0 {
		return 0
	}
	return s - s%p.Cfg.SnapshotEvery
}

// Review decides on one live proposal. myVotes are this signer's own
// published votes (to enforce "at most one live approval per withdrawal").
func (p *Policy) Review(ctx context.Context, prop *Proposal, v *View, st *SVMState, myVotes map[string]Vote) (Decision, string) {
	if t, id, ok := PayoutOf(prop.Tx, p.Cfg.BankAccount); ok {
		return p.reviewTransfer(ctx, prop, t, id, v, st, myVotes)
	}
	if len(prop.Tx.Operations) != 1 {
		return Deny, "more than one operation, and not a withdrawal payout"
	}
	switch op := prop.Tx.Operations[0].Value.(type) {
	case steemtx.Transfer:
		return Deny, "a transfer must come with its withdrawal link"
	case steemtx.AccountUpdate:
		return p.reviewAccountUpdate(ctx, prop, op, v, st, myVotes)
	case steemtx.TransferToVesting, steemtx.WithdrawVesting, steemtx.ChangeRecoveryAccount:
		return Abstain, "treasury/recovery operation: needs a human"
	default:
		return Deny, "operation type not allowed: " + prop.Tx.Operations[0].Type
	}
}

// payoutFacts checks a transfer against the SVM withdrawal it pays. It returns
// Approve when the content is a correct payout, Deny when
// it is wrong and can never become right (a deny vote is final), and Abstain
// when it can't be judged yet (a read failed, or this signer's SVM node hasn't
// seen the withdrawal), so the next pass looks again.
func (p *Policy) payoutFacts(ctx context.Context, t steemtx.Transfer, id uint64, v *View, st *SVMState) (Decision, string) {
	if t.From != p.Cfg.BankAccount {
		return Deny, "not from " + p.Cfg.BankAccount
	}
	if v.History.IsPaid(id) {
		return Deny, "withdrawal already paid on Steem"
	}
	w, ok := st.Requested[id]
	if !ok {
		got, found, err := p.SVM.Withdrawal(ctx, id)
		switch {
		case err != nil:
			return Abstain, "cannot read withdrawal: " + err.Error()
		case !found:
			return Abstain, "SVM withdrawal not found (yet)"
		case got.Status != "REQUESTED":
			return Deny, "SVM withdrawal is " + got.Status
		}
		w = got
	}
	if t.To != w.Destination {
		return Deny, "recipient differs from the SVM withdrawal destination"
	}
	if t.Amount != steemtx.FormatMilli(w.AmountMilli, w.Symbol) {
		return Deny, "amount differs from the SVM withdrawal net amount"
	}
	if t.Memo != w.Memo {
		return Deny, "memo differs from the SVM withdrawal memo"
	}
	deadline := w.CreatedAt + st.Timeout
	if uint64(st.Height)+p.Cfg.PayoutMarginBlocks > deadline {
		return Deny, "too close to the automatic SVM refund"
	}
	return Approve, ""
}

func (p *Policy) reviewTransfer(ctx context.Context, prop *Proposal, t steemtx.Transfer, id uint64, v *View, st *SVMState, myVotes map[string]Vote) (Decision, string) {
	d, reason := p.payoutFacts(ctx, t, id, v, st)
	if d != Approve {
		return d, reason
	}
	if cap, ok := p.Cfg.MaxPerTxMilli[symbolOf(t.Amount)]; ok && cap > 0 {
		if a, err := steemtx.ParseAsset(t.Amount); err == nil && uint64(a.Amount) > cap {
			return Abstain, "over the configured per-transaction cap"
		}
	}
	// One payout per withdrawal: never sign a second proposal for the same
	// withdrawal while one this signer approved can still execute, and among
	// correct live proposals approve only the earliest, so at most one can ever
	// reach the threshold. These are Abstain, not Deny: they clear once the
	// other proposal expires, and a deny vote is final.
	if p.Journal.Conflicts(SubjectOf(prop.Tx), prop.ID, v.Now) {
		return Abstain, "already approved another unexpired proposal for this withdrawal"
	}
	earliest := ""
	for _, q := range v.Proposal { // sorted by (Created, ID)
		qt, qid, ok := PayoutOf(q.Tx, p.Cfg.BankAccount)
		if !ok || qid != id {
			continue
		}
		// Not just live ones: a "rejected" proposal can still execute if a
		// member flips a deny to an approval before it expires.
		if q.ID != prop.ID && q.Status != Expired {
			if mv, voted := myVotes[q.ID]; voted && mv.A == 1 {
				return Abstain, "already approved another unexpired proposal for this withdrawal"
			}
		}
		if earliest == "" && q.Live() {
			if qd, _ := p.payoutFacts(ctx, qt, qid, v, st); qd == Approve {
				earliest = q.ID
			}
		}
	}
	if earliest != prop.ID {
		return Abstain, fmt.Sprintf("an earlier correct proposal exists for withdrawal %d", id)
	}
	return Approve, fmt.Sprintf("pays SVM withdrawal %d exactly", id)
}

func (p *Policy) reviewAccountUpdate(ctx context.Context, prop *Proposal, u steemtx.AccountUpdate, v *View, st *SVMState, myVotes map[string]Vote) (Decision, string) {
	if u.Account != p.Cfg.BankAccount {
		return Deny, "not " + p.Cfg.BankAccount
	}
	if u.Owner == nil || u.Active == nil || u.Posting == nil {
		return Deny, "must set owner, active and posting together"
	}
	m := snapshotRe.FindStringSubmatch(prop.Title)
	if m == nil {
		return Deny, "title has no @svm:<height> snapshot"
	}
	s, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || s <= 0 || s%p.Cfg.SnapshotEvery != 0 || st.Height-s > p.Cfg.SnapshotMaxAge {
		return Deny, "snapshot is not a recent checkpoint"
	}
	if s > st.Height {
		return Abstain, "this signer's SVM node has not reached the snapshot yet"
	}
	desired, err := p.DesiredAt(ctx, s)
	if err != nil {
		return Abstain, "cannot compute the stake-proportional set: " + err.Error()
	}
	if !u.Owner.Equal(desired) || !u.Active.Equal(desired) || !u.Posting.Equal(desired) {
		return Deny, fmt.Sprintf("authority differs from the stake-proportional set at @svm:%d", s)
	}
	if u.MemoKey != v.Bank.MemoKey {
		return Deny, "memo key changed"
	}
	if u.JSONMetadata != "" && u.JSONMetadata != v.Bank.JSONMetadata {
		return Deny, "json_metadata changed"
	}
	if !ExceedsTolerance(v.Bank.Owner, desired, p.Cfg.ReweightTolerance) {
		return Deny, "change is within the reweight tolerance"
	}
	if p.Journal.Conflicts(SubjectOf(prop.Tx), prop.ID, v.Now) {
		return Abstain, "already approved another unexpired validator-set proposal"
	}
	for _, q := range v.Proposal {
		if q.ID == prop.ID || q.Status == Expired || q.Status == Executed {
			continue
		}
		if _, isUpd := q.Tx.Operations[0].Value.(steemtx.AccountUpdate); isUpd {
			if mv, voted := myVotes[q.ID]; voted && mv.A == 1 {
				return Abstain, "already approved another unexpired validator-set proposal"
			}
		}
	}
	return Approve, fmt.Sprintf("equals the stake-proportional set at @svm:%d", s)
}

func symbolOf(amount string) string {
	if a, err := steemtx.ParseAsset(amount); err == nil {
		return a.Symbol
	}
	return ""
}

// rankOf is the self member's distance (in chain order) from the designated
// proposer index, or -1 when self is not a member.
func rankOf(members []string, self string, designated int) int {
	n := len(members)
	for i, m := range members {
		if m == self {
			return ((i-designated)%n + n) % n
		}
	}
	return -1
}

// Intents lists the payouts and validator-set change this signer should
// propose now. firstSeen tracks (locally) when each intent became due so
// non-designated members wait rank*FallbackDelay before stepping in.
func (p *Policy) Intents(ctx context.Context, v *View, st *SVMState, firstSeen map[string]time.Time, now time.Time) ([]Intent, error) {
	members := v.SortedMembers()
	n := len(members)
	if n == 0 || rankOf(members, p.Self, 0) < 0 {
		return nil, nil
	}
	due := func(key string, designated int) bool {
		if _, ok := firstSeen[key]; !ok {
			firstSeen[key] = now
		}
		rank := rankOf(members, p.Self, designated)
		return rank == 0 || now.Sub(firstSeen[key]) >= time.Duration(rank)*p.Cfg.FallbackDelay
	}
	live := map[uint64]bool{}
	liveUpdate := false
	for _, q := range v.Proposal {
		if !q.Live() {
			continue
		}
		if _, id, ok := PayoutOf(q.Tx, p.Cfg.BankAccount); ok {
			live[id] = true
			// Restart the fallback clock while a proposal is live, so if it
			// expires unexecuted the designated member re-proposes first
			// instead of every member at once.
			delete(firstSeen, "w:"+strconv.FormatUint(id, 10))
		}
		if _, ok := q.Tx.Operations[0].Value.(steemtx.AccountUpdate); ok && len(q.Tx.Operations) == 1 {
			liveUpdate = true
		}
	}

	balance := map[string]uint64{}
	for _, s := range []string{v.Bank.Balance, v.Bank.SBDBalance} {
		if a, err := steemtx.ParseAsset(s); err == nil {
			balance[a.Symbol] = uint64(a.Amount)
		}
	}

	var out []Intent
	ids := make([]uint64, 0, len(st.Requested))
	for id := range st.Requested {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		w := st.Requested[id]
		if live[id] || v.History.IsPaid(id) || p.Journal.Conflicts(PayoutSubject(id), "", v.Now) {
			continue
		}
		if uint64(st.Height)+p.Cfg.PayoutMarginBlocks > w.CreatedAt+st.Timeout {
			continue // the automatic SVM refund will handle it
		}
		if cap, ok := p.Cfg.MaxPerTxMilli[w.Symbol]; ok && cap > 0 && w.AmountMilli > cap {
			continue
		}
		if balance[w.Symbol] < w.AmountMilli {
			continue
		}
		key := "w:" + strconv.FormatUint(id, 10)
		if !due(key, int(id%uint64(n))) {
			continue
		}
		balance[w.Symbol] -= w.AmountMilli
		amount := steemtx.FormatMilli(w.AmountMilli, w.Symbol)
		out = append(out, Intent{
			Key:   key,
			Title: fmt.Sprintf("Withdrawal %d: %s to @%s", id, amount, w.Destination),
			Ops:   PayoutOps(p.Cfg.BankAccount, w.Destination, amount, w.Memo, id),
		})
	}

	if !liveUpdate && !p.Journal.Conflicts("account_update:"+p.Cfg.BankAccount, "", v.Now) {
		cooled := true
		if v.History.LastOwnerChange != "" {
			if t, err := time.Parse(steemtx.TimeLayout, v.History.LastOwnerChange); err == nil && v.Now.Sub(t) < p.Cfg.OwnerCooldown {
				cooled = false
			}
		}
		if s := p.Checkpoint(st.Height); cooled && s > 0 {
			desired, err := p.DesiredAt(ctx, s)
			if err != nil {
				return out, err
			}
			if ExceedsTolerance(v.Bank.Owner, desired, p.Cfg.ReweightTolerance) {
				key := "v:" + strconv.FormatInt(s, 10)
				if due(key, int((s/p.Cfg.SnapshotEvery)%int64(n))) {
					d := desired
					out = append(out, Intent{
						Key:   key,
						Title: fmt.Sprintf("Validator set @svm:%d", s),
						Ops: []steemtx.Operation{{Type: "account_update", Value: steemtx.AccountUpdate{
							Account: p.Cfg.BankAccount, Owner: &d, Active: &d, Posting: &d,
							MemoKey: v.Bank.MemoKey, JSONMetadata: "",
						}}},
					})
				}
			}
		}
	}
	return out, nil
}
