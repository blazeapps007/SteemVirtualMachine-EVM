package multisig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"steemvm/oracle/go/steemtx"
)

// ApprovalJournal is this signer's local, write-ahead record of every approval
// it has signed. It is saved before an approval is published and consulted by
// every review, so "at most one live approval per withdrawal" holds even when a
// lagging Steem node serves stale metadata that hides an earlier approval.
type ApprovalJournal struct {
	Approvals map[string]JournalEntry `json:"approvals"` // proposal ID -> entry
}

// JournalEntry is one signed approval.
type JournalEntry struct {
	Subject    string `json:"subject"`    // what the transaction pays or changes (SubjectOf)
	Expiration string `json:"expiration"` // the transaction's expiration
}

const journalFile = "svm_bank_approvals.json"

// SubjectOf names what a single-operation proposal acts on: two proposals with
// the same subject must never both be approved while either can still execute.
// It is "" for anything the policy never approves.
func SubjectOf(tx steemtx.Transaction) string {
	if len(tx.Operations) != 1 {
		return ""
	}
	switch o := tx.Operations[0].Value.(type) {
	case steemtx.Transfer:
		return "transfer:" + o.Memo
	case steemtx.AccountUpdate:
		return "account_update:" + o.Account
	}
	return ""
}

// LoadJournal reads the journal from dir (empty if absent). Unlike the history
// index it is not rebuildable from the chain, so a corrupt file is an error.
func LoadJournal(dir string) (*ApprovalJournal, error) {
	j := &ApprovalJournal{Approvals: map[string]JournalEntry{}}
	b, err := os.ReadFile(filepath.Join(dir, journalFile))
	if os.IsNotExist(err) {
		return j, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, j); err != nil {
		return nil, fmt.Errorf("multisig: %s is corrupt (it is safe to delete once the signer has been stopped for over an hour): %w",
			filepath.Join(dir, journalFile), err)
	}
	if j.Approvals == nil {
		j.Approvals = map[string]JournalEntry{}
	}
	return j, nil
}

// Save writes the journal atomically.
func (j *ApprovalJournal) Save(dir string) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, journalFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, journalFile))
}

// Record notes an approval of proposal id.
func (j *ApprovalJournal) Record(id string, tx steemtx.Transaction) {
	j.Approvals[id] = JournalEntry{Subject: SubjectOf(tx), Expiration: tx.Expiration}
}

// Conflicts reports whether a different proposal with the same subject was
// approved and can still execute at Steem head time now. An unparseable
// expiration counts as a conflict.
func (j *ApprovalJournal) Conflicts(subject, id string, now time.Time) bool {
	if j == nil || subject == "" {
		return false
	}
	for pid, e := range j.Approvals {
		if pid == id || e.Subject != subject {
			continue
		}
		exp, err := time.Parse(steemtx.TimeLayout, e.Expiration)
		if err != nil || now.Before(exp) {
			return true
		}
	}
	return false
}

// Prune drops entries that expired more than PruneAfter before now.
func (j *ApprovalJournal) Prune(now time.Time) {
	cut := now.Add(-PruneAfter)
	for id, e := range j.Approvals {
		if exp, err := time.Parse(steemtx.TimeLayout, e.Expiration); err == nil && exp.Before(cut) {
			delete(j.Approvals, id)
		}
	}
}
