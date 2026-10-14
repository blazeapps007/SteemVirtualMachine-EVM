package multisig

import (
	"sort"
	"time"

	"steemvm/oracle/go/steemtx"
)

// Status of a proposal, derived identically by every client (guide section 2.5).
type Status int

const (
	Open Status = iota
	Ready
	Rejected
	Expired
	Executed
)

func (s Status) String() string {
	return [...]string{"open", "ready", "rejected", "expired", "executed"}[s]
}

// VoteInfo is one validator's latest vote on a proposal.
type VoteInfo struct {
	Approve bool
	Sig     string
	Valid   bool // approvals: signature recovers to a key that satisfies the voter's active authority
}

// Proposal is a verified proposal (its ID matches its transaction).
type Proposal struct {
	ID         string
	Creator    string
	Title      string
	Created    string
	Tx         steemtx.Transaction
	Digest     [32]byte
	Expiration time.Time
	Owner      bool // needs svm.bank's owner authority (else active)
	Votes      map[string]VoteInfo
	Approve    uint32
	Deny       uint32
	Status     Status
}

// Live reports whether the proposal can still be executed.
func (p *Proposal) Live() bool { return p.Status == Open || p.Status == Ready }

// View is everything the signer derives from the chain in one pass.
type View struct {
	Now      time.Time // Steem head block time
	Props    steemtx.DynamicProps
	Bank     Account
	Members  map[string]Account // current svm.bank authority members
	History  *HistoryIndex
	Proposal []*Proposal // sorted by (Created, ID)
}

// Authority returns the svm.bank authority a proposal is checked against.
func (v *View) Authority(p *Proposal) steemtx.Authority {
	if p.Owner {
		return v.Bank.Owner
	}
	return v.Bank.Active
}

// IsMember reports whether name is in svm.bank's owner authority.
func (v *View) IsMember(name string) bool { return v.Bank.Owner.WeightOf(name) > 0 }

// needsOwner reports whether a transaction needs svm.bank's owner authority.
func needsOwner(tx steemtx.Transaction) bool {
	for _, op := range tx.Operations {
		switch o := op.Value.(type) {
		case steemtx.AccountUpdate:
			if o.Owner != nil {
				return true
			}
		case steemtx.AccountUpdate2:
			if o.Owner != nil {
				return true
			}
		case steemtx.ChangeRecoveryAccount:
			return true
		}
	}
	return false
}

// BuildView verifies and tallies every proposal published by current members.
// ballots maps member name -> parsed ballot (members with unreadable
// metadata are simply absent).
func BuildView(now time.Time, props steemtx.DynamicProps, bank Account, members map[string]Account, ballots map[string]Ballot, hist *HistoryIndex) *View {
	v := &View{Now: now, Props: props, Bank: bank, Members: members, History: hist}
	byID := map[string]*Proposal{}

	creators := sortedKeys(ballots)
	for _, creator := range creators {
		if !v.IsMember(creator) {
			continue // only proposals from svm.bank's current owner authority
		}
		for id, sp := range ballots[creator].Props {
			gotID, err := sp.Tx.ID()
			if err != nil || gotID != id {
				continue // never trust a title or ID that doesn't match the transaction
			}
			if !targetsBank(sp.Tx, bank.Name) {
				continue
			}
			digest, err := sp.Tx.Digest()
			if err != nil {
				continue
			}
			exp, err := time.Parse(steemtx.TimeLayout, sp.Tx.Expiration)
			if err != nil {
				continue
			}
			p := &Proposal{ID: id, Creator: creator, Title: sp.T, Created: sp.C, Tx: sp.Tx, Digest: digest,
				Expiration: exp, Owner: needsOwner(sp.Tx), Votes: map[string]VoteInfo{}}
			if prev, ok := byID[id]; ok && (prev.Created < p.Created || prev.Created == p.Created && prev.Creator < p.Creator) {
				continue // same transaction published twice: keep the earliest
			}
			byID[id] = p
		}
	}

	for _, p := range byID {
		auth := v.Authority(p)
		for _, voter := range creators {
			w := uint32(auth.WeightOf(voter))
			if w == 0 {
				continue
			}
			vote, ok := ballots[voter].Votes[p.ID]
			if !ok {
				continue
			}
			vi := VoteInfo{Approve: vote.A == 1, Sig: vote.S}
			if vi.Approve {
				if pub, err := steemtx.RecoverSigner(p.Digest, vote.S); err == nil && keySatisfies(members[voter].Active, pub) {
					vi.Valid = true
					p.Approve += w
				}
			} else {
				vi.Valid = true
				p.Deny += w
			}
			p.Votes[voter] = vi
		}
		total := auth.Total()
		switch {
		case hist != nil && hist.Executed[p.ID] != "":
			p.Status = Executed
		case !now.Before(p.Expiration):
			p.Status = Expired
		case total >= auth.WeightThreshold && p.Deny > total-auth.WeightThreshold:
			p.Status = Rejected
		case p.Approve >= auth.WeightThreshold:
			p.Status = Ready
		default:
			p.Status = Open
		}
		v.Proposal = append(v.Proposal, p)
	}
	sort.Slice(v.Proposal, func(i, j int) bool {
		if v.Proposal[i].Created != v.Proposal[j].Created {
			return v.Proposal[i].Created < v.Proposal[j].Created
		}
		return v.Proposal[i].ID < v.Proposal[j].ID
	})
	return v
}

// keySatisfies reports whether a single key meets an active authority.
func keySatisfies(active steemtx.Authority, pub string) bool {
	for _, k := range active.KeyAuths {
		if k.Key == pub && uint32(k.Weight) >= active.WeightThreshold {
			return true
		}
	}
	return false
}

// targetsBank checks every operation acts on the bank account.
func targetsBank(tx steemtx.Transaction, bank string) bool {
	if len(tx.Operations) == 0 {
		return false
	}
	for _, op := range tx.Operations {
		var acct string
		switch o := op.Value.(type) {
		case steemtx.Transfer:
			acct = o.From
		case steemtx.TransferToVesting:
			acct = o.From
		case steemtx.WithdrawVesting:
			acct = o.Account
		case steemtx.AccountUpdate:
			acct = o.Account
		case steemtx.AccountUpdate2:
			acct = o.Account
		case steemtx.ChangeRecoveryAccount:
			acct = o.AccountToRecover
		}
		if acct != bank {
			return false
		}
	}
	return true
}

// MinimalSignatures walks the authority in chain (sorted) order and collects
// valid approval signatures until the threshold is reached. Steem rejects
// transactions with unnecessary signatures, so it stops exactly there.
func (v *View) MinimalSignatures(p *Proposal) []string {
	auth := v.Authority(p).Sorted()
	var sigs []string
	var w uint32
	for _, a := range auth.AccountAuths {
		if w >= auth.WeightThreshold {
			break
		}
		if vi, ok := p.Votes[a.Name]; ok && vi.Approve && vi.Valid {
			sigs = append(sigs, vi.Sig)
			w += uint32(a.Weight)
		}
	}
	if w < auth.WeightThreshold {
		return nil
	}
	return sigs
}

// SortedMembers returns svm.bank's owner members in chain order.
func (v *View) SortedMembers() []string {
	auth := v.Bank.Owner.Sorted()
	out := make([]string, len(auth.AccountAuths))
	for i, a := range auth.AccountAuths {
		out[i] = a.Name
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
