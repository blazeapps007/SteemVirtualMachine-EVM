package multisig

import (
	"encoding/json"
	"sort"
	"testing"
	"time"

	"steemvm/oracle/go/steemtx"
	msvectors "steemvm/oracle/multisig"
)

// TestBallotVectors replays real svm_ms metadata from mainnet (the shared
// "ballots" vectors, also run by the Python and JS clients): every client must
// recompute the same proposal IDs and accept exactly the same approvals.
func TestBallotVectors(t *testing.T) {
	var v struct {
		Ballots []struct {
			Name          string            `json:"name"`
			Now           string            `json:"now"`
			BankAuthority steemtx.Authority `json:"bank_authority"`
			Members       []struct {
				Name                string `json:"name"`
				ActiveKey           string `json:"active_key"`
				PostingJSONMetadata string `json:"posting_json_metadata"`
			} `json:"members"`
			Expected []struct {
				ID             string   `json:"id"`
				Creator        string   `json:"creator"`
				Approve        uint32   `json:"approve"`
				Deny           uint32   `json:"deny"`
				Status         string   `json:"status"`
				ValidApprovals []string `json:"valid_approvals"`
			} `json:"expected"`
		} `json:"ballots"`
	}
	if err := json.Unmarshal(msvectors.JSON, &v); err != nil || len(v.Ballots) == 0 {
		t.Fatalf("vectors: %v (%d ballot cases)", err, len(v.Ballots))
	}
	for _, c := range v.Ballots {
		t.Run(c.Name, func(t *testing.T) {
			now, err := time.Parse(steemtx.TimeLayout, c.Now)
			if err != nil {
				t.Fatal(err)
			}
			bank := Account{Name: testBank, Owner: c.BankAuthority, Active: c.BankAuthority, Posting: c.BankAuthority}
			members, ballots := map[string]Account{}, map[string]Ballot{}
			for _, m := range c.Members {
				members[m.Name] = Account{Name: m.Name, Active: keyAuthority(m.ActiveKey)}
				_, b, err := ParseMetadata(m.PostingJSONMetadata)
				if err != nil {
					t.Fatalf("%s: %v", m.Name, err)
				}
				ballots[m.Name] = b
			}
			view := BuildView(now, steemtx.DynamicProps{}, bank, members, ballots, &HistoryIndex{Paid: map[string]string{}, Executed: map[string]string{}})
			if len(view.Proposal) != len(c.Expected) {
				t.Fatalf("%d proposals, want %d", len(view.Proposal), len(c.Expected))
			}
			for i, want := range c.Expected {
				p := find(t, view, want.ID)
				var valid []string
				for name, vi := range p.Votes {
					if vi.Approve && vi.Valid {
						valid = append(valid, name)
					}
				}
				sort.Strings(valid)
				if p.ID != want.ID || p.Creator != want.Creator || p.Approve != want.Approve || p.Deny != want.Deny ||
					p.Status.String() != want.Status || !equalStrings(valid, want.ValidApprovals) {
					t.Fatalf("proposal %d: id %s creator %s approve %d deny %d status %v approvals %v; want %+v",
						i, p.ID, p.Creator, p.Approve, p.Deny, p.Status, valid, want)
				}
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
