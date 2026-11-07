package multisig

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"

	"steemvm/oracle/go/steemtx"
	msvectors "steemvm/oracle/multisig"
)

// Logger is the logging surface the signer needs (cosmossdk.io/log fits).
type Logger interface {
	Info(msg string, keyVals ...any)
	Error(msg string, keyVals ...any)
}

// SignerConfig configures one validator's signer.
type SignerConfig struct {
	Policy    Config
	ActiveWIF string
	Nodes     []string
	Interval  time.Duration
	StateDir  string
	Propose   bool
	Execute   bool
	DryRun    bool
	RankDelay time.Duration // stagger between members executing the same ready proposal
}

// ForbiddenKeyVars are environment variables the signer refuses to start
// with: only the validator's ACTIVE key belongs on the server. svm.bank's
// owner/active/posting authorities resolve through validators' active keys,
// so an owner key, posting key or master password is never needed.
var ForbiddenKeyVars = []string{
	"ORACLE_STEEM_OWNER_WIF", "ORACLE_STEEM_OWNER", "ORACLE_STEEM_POSTING_WIF",
	"ORACLE_STEEM_MASTER_PASSWORD", "ORACLE_STEEM_PASSWORD", "OWNER", "POSTING", "MASTER_PASSWORD",
}

// CheckForbiddenKeyVars returns an error naming any forbidden variable that is set.
func CheckForbiddenKeyVars() error {
	for _, k := range ForbiddenKeyVars {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			return fmt.Errorf("refusing to start: %s is set — only ORACLE_STEEM_ACTIVE_WIF may be configured (an owner key, posting key or master password is never needed and must not be on the server)", k)
		}
	}
	return nil
}

// Signer is one validator's automated svm.bank signer.
type Signer struct {
	cfg        SignerConfig
	log        Logger
	rpc        *steemtx.RPC
	key        *secp256k1.PrivateKey
	pub        string
	svm        SVM
	self       string
	policy     *Policy
	hist       *HistoryIndex
	journal    *ApprovalJournal
	firstSeen  map[string]time.Time
	readySince map[string]time.Time
	logged     map[string]Decision // last logged decision per proposal, so repeats stay quiet
	unlock     func()
}

// NewSigner validates configuration and runs the conformance selftests.
func NewSigner(cfg SignerConfig, svm SVM, log Logger) (*Signer, error) {
	if err := CheckForbiddenKeyVars(); err != nil {
		return nil, err
	}
	if err := steemtx.Selftest(msvectors.JSON); err != nil {
		return nil, fmt.Errorf("refusing to start: %w", err)
	}
	if err := SelftestApportion(msvectors.JSON); err != nil {
		return nil, fmt.Errorf("refusing to start: %w", err)
	}
	if cfg.ActiveWIF == "" {
		return nil, errors.New("ORACLE_STEEM_ACTIVE_WIF is required when the multisig signer is enabled")
	}
	key, err := steemtx.DecodeWIF(cfg.ActiveWIF)
	if err != nil {
		return nil, fmt.Errorf("ORACLE_STEEM_ACTIVE_WIF: %w", err)
	}
	if len(cfg.Nodes) == 0 {
		return nil, errors.New("no Steem RPC nodes configured (ORACLE_STEEM_RPCS)")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Second
	}
	if cfg.RankDelay <= 0 {
		cfg.RankDelay = 4 * time.Second
	}
	return &Signer{
		cfg: cfg, log: log, rpc: steemtx.NewRPC(cfg.Nodes...), key: key,
		pub: steemtx.EncodePublicKey(key.PubKey()), svm: svm,
		firstSeen: map[string]time.Time{}, readySince: map[string]time.Time{}, logged: map[string]Decision{},
	}, nil
}

// Init resolves this validator's Steem account and verifies the key. It is
// retried by Run until it succeeds (the SVM node may still be syncing).
func (s *Signer) Init(ctx context.Context) error {
	if s.unlock == nil {
		unlock, err := lockStateDir(s.cfg.StateDir)
		if err != nil {
			return fmt.Errorf("one signer per validator: %w", err)
		}
		s.unlock = unlock
	}
	self, err := s.svm.SelfSteemAccount(ctx)
	if err != nil {
		return err
	}
	accts, err := GetAccounts(ctx, s.rpc, []string{self})
	if err != nil {
		return err
	}
	if len(accts) != 1 {
		return fmt.Errorf("steem account %s not found", self)
	}
	for _, k := range accts[0].Owner.KeyAuths {
		if k.Key == s.pub {
			return fmt.Errorf("refusing to run: ORACLE_STEEM_ACTIVE_WIF is an OWNER key of @%s — use the active key", self)
		}
	}
	if !keySatisfies(accts[0].Active, s.pub) {
		return fmt.Errorf("ORACLE_STEEM_ACTIVE_WIF (%s) does not satisfy @%s's active authority", s.pub, self)
	}
	hist, err := LoadHistoryIndex(s.cfg.StateDir)
	if err != nil {
		return err
	}
	journal, err := LoadJournal(s.cfg.StateDir)
	if err != nil {
		return err
	}
	s.self, s.hist, s.journal = self, hist, journal
	s.policy = &Policy{Cfg: s.cfg.Policy, SVM: s.svm, Self: self, Journal: journal}
	s.log.Info("svm.bank signer ready", "validator", self, "key", s.pub, "dry_run", s.cfg.DryRun,
		"propose", s.cfg.Propose, "execute", s.cfg.Execute)
	return nil
}

// Run initializes and then runs a pass every Interval until ctx ends.
func (s *Signer) Run(ctx context.Context) error {
	defer func() {
		if s.unlock != nil {
			s.unlock()
		}
	}()
	for {
		err := s.Init(ctx)
		if err == nil {
			break
		}
		s.log.Error("svm.bank signer: init failed, retrying", "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(30 * time.Second):
		}
	}
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	for {
		if err := s.Pass(ctx); err != nil {
			s.log.Error("svm.bank signer: pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Pass loads the chain view, votes on live proposals, proposes due intents,
// publishes all of it in one metadata write, then executes ready proposals.
func (s *Signer) Pass(ctx context.Context) error {
	props, err := GetDynamicProps(ctx, s.rpc)
	if err != nil {
		return fmt.Errorf("dynamic props: %w", err)
	}
	now, err := time.Parse(steemtx.TimeLayout, props.Time)
	if err != nil {
		return err
	}
	bank := s.cfg.Policy.BankAccount
	if err := s.hist.Sync(ctx, s.rpc, bank, props.HeadBlockNumber, now); err != nil {
		return fmt.Errorf("history: %w", err)
	}
	if err := s.hist.Save(s.cfg.StateDir); err != nil {
		s.log.Error("svm.bank signer: saving history index", "err", err)
	}

	accts, err := GetAccounts(ctx, s.rpc, []string{bank})
	if err != nil || len(accts) != 1 {
		return fmt.Errorf("loading %s: %v", bank, err)
	}
	bankAcct := accts[0]
	names := bankAcct.Owner.Sorted().AccountAuths
	memberNames := make([]string, 0, len(names))
	for _, a := range names {
		memberNames = append(memberNames, a.Name)
	}
	members := map[string]Account{}
	ballots := map[string]Ballot{}
	if len(memberNames) > 0 {
		macc, err := GetAccounts(ctx, s.rpc, memberNames)
		if err != nil {
			return fmt.Errorf("loading members: %w", err)
		}
		for _, a := range macc {
			members[a.Name] = a
			if _, b, err := ParseMetadata(a.PostingJSONMetadata); err == nil {
				ballots[a.Name] = b
			}
		}
	}
	view := BuildView(now, props, bankAcct, members, ballots, s.hist)

	height, err := s.svm.Height(ctx)
	if err != nil {
		return fmt.Errorf("svm height: %w", err)
	}
	timeout, err := s.svm.WithdrawalTimeoutBlocks(ctx)
	if err != nil {
		return fmt.Errorf("svm params: %w", err)
	}
	reqs, err := s.svm.RequestedWithdrawals(ctx)
	if err != nil {
		return fmt.Errorf("svm withdrawals: %w", err)
	}
	st := &SVMState{Height: height, Timeout: timeout, Requested: map[uint64]Withdrawal{}}
	for _, w := range reqs {
		st.Requested[w.ID] = w
	}

	if view.IsMember(s.self) {
		if err := s.voteAndPropose(ctx, view, st, props, now, ballots[s.self]); err != nil {
			s.log.Error("svm.bank signer: vote/propose", "err", err)
		}
	}
	if s.cfg.Execute {
		s.execute(ctx, view, st, ballots[s.self])
	}
	return nil
}

func (s *Signer) voteAndPropose(ctx context.Context, view *View, st *SVMState, props steemtx.DynamicProps, now time.Time, mine Ballot) error {
	myVotes := mine.Votes
	if myVotes == nil {
		myVotes = map[string]Vote{}
	}
	newVotes := map[string]Vote{}
	newProps := map[string]StoredProposal{}
	// A working copy so approvals cast earlier in this pass count for the
	// "one live approval per withdrawal" rule later in the same pass.
	working := map[string]Vote{}
	for k, v := range myVotes {
		working[k] = v
	}

	for _, p := range view.Proposal {
		if !p.Live() {
			continue
		}
		if mv, ok := myVotes[p.ID]; ok && (mv.A == 0 || p.Votes[s.self].Valid) {
			continue // already voted (re-vote only if our approval is invalid)
		}
		d, reason := s.policy.Review(ctx, p, view, st, working)
		s.logDecision(p.ID, d, "title", p.Title, "reason", reason)
		switch d {
		case Approve:
			sig, err := steemtx.Sign(s.key, p.Digest)
			if err != nil {
				return err
			}
			v := Vote{A: 1, S: sig, X: p.Tx.Expiration}
			newVotes[p.ID], working[p.ID] = v, v
			if !s.cfg.DryRun {
				s.journal.Record(p.ID, p.Tx)
			}
		case Deny:
			v := Vote{A: 0, X: p.Tx.Expiration}
			newVotes[p.ID], working[p.ID] = v, v
		}
	}

	if s.cfg.Propose {
		intents, err := s.policy.Intents(ctx, view, st, s.firstSeen, time.Now())
		if err != nil {
			s.log.Error("svm.bank: computing intents", "err", err)
		}
		for _, in := range intents {
			tx, err := steemtx.NewTransaction(props, in.Ops, 58*time.Minute)
			if err != nil {
				return err
			}
			id, err := tx.ID()
			if err != nil {
				return err
			}
			digest, _ := tx.Digest()
			sig, err := steemtx.Sign(s.key, digest)
			if err != nil {
				return err
			}
			newProps[id] = StoredProposal{T: in.Title, Tx: tx, C: props.Time}
			v := Vote{A: 1, S: sig, X: tx.Expiration}
			newVotes[id], working[id] = v, v
			if s.cfg.DryRun {
				// Each pass builds a fresh transaction (new ID), so log by intent.
				s.logDecision("intent "+in.Key, Approve, "title", in.Title, "dry_run", true)
				continue
			}
			s.journal.Record(id, tx)
			s.log.Info("svm.bank: propose", "id", id, "title", in.Title)
		}
	}
	s.pruneLocal(view, now)

	if len(newVotes) == 0 && len(newProps) == 0 {
		return nil
	}
	if s.cfg.DryRun {
		return nil
	}
	// Write-ahead: the journal must hold every approval before any signature
	// leaves this process. If it can't be saved, nothing is published.
	if err := s.journal.Save(s.cfg.StateDir); err != nil {
		return fmt.Errorf("saving the approval journal (nothing published): %w", err)
	}
	return s.publish(ctx, props, now, newVotes, newProps)
}

// publish does the read-modify-write of our own svm_ms in one account_update2,
// signed with the active key (active authority satisfies posting).
func (s *Signer) publish(ctx context.Context, props steemtx.DynamicProps, now time.Time, votes map[string]Vote, propsNew map[string]StoredProposal) error {
	accts, err := GetAccounts(ctx, s.rpc, []string{s.self})
	if err != nil || len(accts) != 1 {
		return fmt.Errorf("reloading @%s: %v", s.self, err)
	}
	raw, ballot, err := ParseMetadata(accts[0].PostingJSONMetadata)
	if err != nil {
		return fmt.Errorf("refusing to overwrite @%s's posting_json_metadata: %w", s.self, err)
	}
	for id, v := range votes {
		ballot.Votes[id] = v
	}
	for id, p := range propsNew {
		ballot.Props[id] = p
	}
	ballot.Prune(now)
	posting, err := EncodeMetadata(raw, ballot)
	if err != nil {
		return err
	}
	tx, err := steemtx.NewTransaction(props, []steemtx.Operation{{Type: "account_update2", Value: steemtx.AccountUpdate2{
		Account: s.self, JSONMetadata: "", PostingJSONMetadata: posting, Extensions: []any{},
	}}}, 5*time.Minute)
	if err != nil {
		return err
	}
	digest, err := tx.Digest()
	if err != nil {
		return err
	}
	sig, err := steemtx.Sign(s.key, digest)
	if err != nil {
		return err
	}
	tx.Signatures = []string{sig}
	if _, err := Broadcast(ctx, s.rpc, tx); err != nil {
		return fmt.Errorf("publishing votes: %w", err)
	}
	s.log.Info("svm.bank: published", "votes", len(votes), "proposals", len(propsNew))
	return nil
}

// execute broadcasts ready proposals this signer's own policy still approves,
// with only the minimal signature set. The creator goes first; others wait by
// rank so the same proposal is rarely broadcast twice (a duplicate is harmless).
func (s *Signer) execute(ctx context.Context, view *View, st *SVMState, mine Ballot) {
	members := view.SortedMembers()
	for _, p := range view.Proposal {
		if p.Status != Ready {
			delete(s.readySince, p.ID)
			continue
		}
		if _, ok := s.readySince[p.ID]; !ok {
			s.readySince[p.ID] = time.Now()
		}
		if time.Until(p.Expiration.Add(-30*time.Second)) <= 0 {
			continue
		}
		delay := time.Duration(0)
		if p.Creator != s.self {
			rank := rankOf(members, s.self, 0)
			if rank < 0 {
				rank = len(members)
			}
			delay = s.cfg.RankDelay * time.Duration(rank+1)
		}
		if time.Since(s.readySince[p.ID]) < delay {
			continue
		}
		// Execute only what our own policy approves right now (e.g. a payout
		// whose withdrawal was paid by another proposal in the meantime is not
		// re-broadcast). Our own approval doesn't count as a competing one here.
		votes := map[string]Vote{}
		for k, v := range mine.Votes {
			if k != p.ID {
				votes[k] = v
			}
		}
		if d, reason := s.policy.Review(ctx, p, view, st, votes); d != Approve {
			s.logDecision("exec "+p.ID, d, "title", p.Title, "reason", "not executing ready proposal: "+reason)
			continue
		}
		sigs := view.MinimalSignatures(p)
		if sigs == nil {
			continue
		}
		tx := p.Tx
		tx.Signatures = sigs
		if s.cfg.DryRun {
			s.logDecision("exec "+p.ID, Approve, "title", p.Title, "would_execute_with_signatures", len(sigs))
			continue
		}
		dup, err := Broadcast(ctx, s.rpc, tx)
		switch {
		case err != nil:
			s.log.Error("svm.bank: execute failed", "id", p.ID, "title", p.Title, "err", err)
		case dup:
			s.log.Info("svm.bank: already executed", "id", p.ID)
		default:
			s.log.Info("svm.bank: executed", "id", p.ID, "title", p.Title, "signatures", len(sigs))
		}
	}
}

// logDecision logs a decision about key only when it differs from the last one
// logged, so a proposal re-reviewed every pass (abstain, dry run) logs once.
func (s *Signer) logDecision(key string, d Decision, keyVals ...any) {
	if prev, ok := s.logged[key]; ok && prev == d {
		return
	}
	s.logged[key] = d
	s.log.Info("svm.bank: "+d.String(), append([]any{"key", key, "dry_run", s.cfg.DryRun}, keyVals...)...)
}

func (s *Signer) pruneLocal(view *View, now time.Time) {
	if len(s.logged) > 2000 {
		s.logged = map[string]Decision{}
	}
	if len(s.firstSeen) > 1000 {
		s.firstSeen = map[string]time.Time{}
	}
	s.journal.Prune(now)
}
