package multisig

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"steemvm/oracle/go/steemtx"
)

// Account is the subset of a Steem account the signer uses.
type Account struct {
	Name                string            `json:"name"`
	Owner               steemtx.Authority `json:"owner"`
	Active              steemtx.Authority `json:"active"`
	Posting             steemtx.Authority `json:"posting"`
	MemoKey             string            `json:"memo_key"`
	JSONMetadata        string            `json:"json_metadata"`
	PostingJSONMetadata string            `json:"posting_json_metadata"`
	Balance             string            `json:"balance"`
	SBDBalance          string            `json:"sbd_balance"`
}

// GetAccounts fetches accounts by name, in the order the node returns them.
func GetAccounts(ctx context.Context, rpc *steemtx.RPC, names []string) ([]Account, error) {
	var out []Account
	if err := rpc.Call(ctx, "condenser_api.get_accounts", []any{names}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetDynamicProps fetches the head block reference and time.
func GetDynamicProps(ctx context.Context, rpc *steemtx.RPC) (steemtx.DynamicProps, error) {
	var p steemtx.DynamicProps
	err := rpc.Call(ctx, "condenser_api.get_dynamic_global_properties", []any{}, &p)
	return p, err
}

// Broadcast sends a signed transaction synchronously. A "Duplicate
// transaction" rejection means it is already on chain and is not an error.
func Broadcast(ctx context.Context, rpc *steemtx.RPC, tx steemtx.Transaction) (alreadyOnChain bool, err error) {
	var res json.RawMessage
	err = rpc.Call(ctx, "condenser_api.broadcast_transaction_synchronous", []any{tx}, &res)
	if err != nil && strings.Contains(err.Error(), "Duplicate transaction") {
		return true, nil
	}
	return false, err
}

// A withdrawal payout is one Steem transaction with two operations, in this
// order: the transfer, carrying the memo the user gave when bridging out, and a
// custom_json naming the withdrawal. Only svm.bank's active authority can sign
// that custom_json, so it can't be forged, and the memo means nothing to the
// bridge: a user can put anything there, including "svm-withdrawal 5", without
// affecting any withdrawal.

// PayoutLinkID is the custom_json id of a payout's link.
const PayoutLinkID = "svm-withdrawal"

var payoutLinkRe = regexp.MustCompile(`^\{"withdrawal_id":"(0|[1-9][0-9]*)"\}$`)

// PayoutLinkJSON is the exact custom_json body naming a withdrawal.
func PayoutLinkJSON(id uint64) string {
	return `{"withdrawal_id":"` + strconv.FormatUint(id, 10) + `"}`
}

// ParsePayoutLink returns the withdrawal a custom_json names, if it is exactly a
// payout link signed by the bank's active authority alone.
func ParsePayoutLink(c steemtx.CustomJSON, bank string) (uint64, bool) {
	if c.ID != PayoutLinkID || len(c.RequiredAuths) != 1 || c.RequiredAuths[0] != bank || len(c.RequiredPostingAuths) != 0 {
		return 0, false
	}
	m := payoutLinkRe.FindStringSubmatch(c.JSON)
	if m == nil {
		return 0, false
	}
	id, err := strconv.ParseUint(m[1], 10, 64)
	return id, err == nil
}

// PayoutOps builds the two operations of a withdrawal payout.
func PayoutOps(bank, to, amount, memo string, id uint64) []steemtx.Operation {
	return []steemtx.Operation{
		{Type: "transfer", Value: steemtx.Transfer{From: bank, To: to, Amount: amount, Memo: memo}},
		{Type: "custom_json", Value: steemtx.CustomJSON{
			RequiredAuths: []string{bank}, RequiredPostingAuths: []string{}, ID: PayoutLinkID, JSON: PayoutLinkJSON(id),
		}},
	}
}

// PayoutOf returns a transaction's transfer and the withdrawal it pays, if the
// transaction has exactly the shape of a payout.
func PayoutOf(tx steemtx.Transaction, bank string) (steemtx.Transfer, uint64, bool) {
	if len(tx.Operations) != 2 {
		return steemtx.Transfer{}, 0, false
	}
	t, ok := tx.Operations[0].Value.(steemtx.Transfer)
	if !ok {
		return steemtx.Transfer{}, 0, false
	}
	c, ok := tx.Operations[1].Value.(steemtx.CustomJSON)
	if !ok {
		return steemtx.Transfer{}, 0, false
	}
	id, ok := ParsePayoutLink(c, bank)
	return t, id, ok
}

// ---- svm.bank history index ----

// HistoryIndex is a persisted, rebuildable index of svm.bank's account
// history: which withdrawals were already paid, which transaction IDs were
// executed, and when the owner authority last changed. It is a cache of public
// chain data — deleting the file just triggers a full rescan.
type HistoryIndex struct {
	NextSeq         uint64            `json:"next_seq"`
	Paid            map[string]string `json:"paid"`     // withdrawal id -> payout trx_id
	Executed        map[string]string `json:"executed"` // trx_id -> timestamp (recent only)
	LastOwnerChange string            `json:"last_owner_change"`
}

const historyFile = "svm_bank_history.json"

// keepExecuted bounds the executed-trx set: proposals live at most an hour.
const keepExecuted = 96 * time.Hour

const zeroTxID = "0000000000000000000000000000000000000000"

// LoadHistoryIndex reads the index from dir (empty if absent).
func LoadHistoryIndex(dir string) (*HistoryIndex, error) {
	h := &HistoryIndex{Paid: map[string]string{}, Executed: map[string]string{}}
	b, err := os.ReadFile(filepath.Join(dir, historyFile))
	if os.IsNotExist(err) {
		return h, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, h); err != nil {
		// A corrupt cache is rebuilt from scratch rather than trusted.
		return &HistoryIndex{Paid: map[string]string{}, Executed: map[string]string{}}, nil
	}
	if h.Paid == nil {
		h.Paid = map[string]string{}
	}
	if h.Executed == nil {
		h.Executed = map[string]string{}
	}
	return h, nil
}

// Save writes the index atomically.
func (h *HistoryIndex) Save(dir string) error {
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, historyFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, historyFile))
}

// IsPaid reports whether a payout for the withdrawal is on chain.
func (h *HistoryIndex) IsPaid(id uint64) bool {
	_, ok := h.Paid[strconv.FormatUint(id, 10)]
	return ok
}

type historyEntry struct {
	TrxID     string             `json:"trx_id"`
	Timestamp string             `json:"timestamp"`
	Op        [2]json.RawMessage `json:"op"`
}

type historyItem struct {
	seq   uint64
	entry historyEntry
}

// historyPage fetches sequences [start-100, start]. The node asserts
// start >= limit and clamps a start beyond the newest entry to the newest.
func historyPage(ctx context.Context, rpc *steemtx.RPC, account string, start uint64) ([]historyItem, error) {
	var page [][2]json.RawMessage
	if err := rpc.Call(ctx, "condenser_api.get_account_history", []any{account, start, 100}, &page); err != nil {
		return nil, err
	}
	out := make([]historyItem, 0, len(page))
	for _, item := range page {
		var it historyItem
		if err := json.Unmarshal(item[0], &it.seq); err != nil {
			return nil, fmt.Errorf("multisig: history seq: %w", err)
		}
		if err := json.Unmarshal(item[1], &it.entry); err != nil {
			return nil, fmt.Errorf("multisig: history entry %d: %w", it.seq, err)
		}
		out = append(out, it)
	}
	return out, nil
}

// applyPage applies the entries at or after NextSeq and reports progress.
func (h *HistoryIndex) applyPage(account string, page []historyItem) bool {
	advanced := false
	for _, it := range page {
		if it.seq < h.NextSeq {
			continue
		}
		h.apply(account, it.entry)
		advanced = true
		if it.seq+1 > h.NextSeq {
			h.NextSeq = it.seq + 1
		}
	}
	return advanced
}

// Sync brings the index up to svm.bank's newest history entry. The newest
// window is requested with the head block number as start: the node clamps it
// to the newest entry, and a start that changes every block can't be answered
// from a stale cache (multisigguide.md §4.1). A gap behind that window (first
// run, long downtime) is paged forward with start = NextSeq+100, which reads
// [NextSeq, NextSeq+100] — immutable history, safe to cache.
func (h *HistoryIndex) Sync(ctx context.Context, rpc *steemtx.RPC, account string, headBlock uint32, now time.Time) error {
	newest, err := historyPage(ctx, rpc, account, max(uint64(headBlock), h.NextSeq+100))
	if err != nil {
		return err
	}
	if len(newest) > 0 {
		oldest := newest[0].seq
		for _, it := range newest {
			oldest = min(oldest, it.seq)
		}
		for h.NextSeq < oldest {
			page, err := historyPage(ctx, rpc, account, h.NextSeq+100)
			if err != nil {
				return err
			}
			if !h.applyPage(account, page) {
				break
			}
		}
		h.applyPage(account, newest)
	}
	h.pruneExecuted(now)
	return nil
}

func (h *HistoryIndex) apply(account string, e historyEntry) {
	if e.TrxID != "" && e.TrxID != zeroTxID {
		h.Executed[e.TrxID] = e.Timestamp
	}
	var opType string
	if json.Unmarshal(e.Op[0], &opType) != nil {
		return
	}
	switch opType {
	case "custom_json":
		var c steemtx.CustomJSON
		if json.Unmarshal(e.Op[1], &c) == nil {
			if id, ok := ParsePayoutLink(c, account); ok {
				h.Paid[strconv.FormatUint(id, 10)] = e.TrxID
			}
		}
	case "account_update", "account_update2":
		var u struct {
			Account string          `json:"account"`
			Owner   json.RawMessage `json:"owner"`
		}
		if json.Unmarshal(e.Op[1], &u) == nil && u.Account == account && len(u.Owner) > 0 && string(u.Owner) != "null" {
			h.LastOwnerChange = e.Timestamp
		}
	}
}

func (h *HistoryIndex) pruneExecuted(now time.Time) {
	cut := now.Add(-keepExecuted)
	for id, ts := range h.Executed {
		if t, err := time.Parse(steemtx.TimeLayout, ts); err == nil && t.Before(cut) {
			delete(h.Executed, id)
		}
	}
}
