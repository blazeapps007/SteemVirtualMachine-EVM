package multisig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"steemvm/oracle/go/steemtx"
)

// MetadataKey is the key inside posting_json_metadata that holds a
// validator's proposals and votes.
const MetadataKey = "svm_ms"

// PruneAfter is how long after expiry proposals and votes are kept.
const PruneAfter = 72 * time.Hour

// maxBallotBytes keeps the serialized svm_ms well inside Steem's 64 KiB
// transaction limit (the rest of posting_json_metadata rides along too).
const maxBallotBytes = 40 * 1024

// Ballot is the svm_ms object (oracle/multisigguide.md section 2.1).
type Ballot struct {
	Props map[string]StoredProposal `json:"props"`
	Votes map[string]Vote           `json:"votes"`
}

// StoredProposal is one proposal this validator created, keyed by its ID.
type StoredProposal struct {
	T  string              `json:"t"`
	Tx steemtx.Transaction `json:"tx"`
	C  string              `json:"c"`
}

// Vote is this validator's vote on a proposal, keyed by the proposal ID.
type Vote struct {
	A int    `json:"a"`           // 1 approve, 0 deny
	S string `json:"s,omitempty"` // signature over the tx digest, approvals only
	X string `json:"x"`           // the tx expiration, used for pruning
}

// ErrInvalidMetadata means posting_json_metadata is not a JSON object. A
// signer must never overwrite it in that case.
var ErrInvalidMetadata = errors.New("multisig: posting_json_metadata is not a valid JSON object")

// ParseMetadata splits posting_json_metadata into the untouched raw entries
// and the parsed svm_ms ballot. An empty string is an empty object.
func ParseMetadata(posting string) (map[string]json.RawMessage, Ballot, error) {
	raw := map[string]json.RawMessage{}
	if s := strings.TrimSpace(posting); s != "" {
		if err := json.Unmarshal([]byte(s), &raw); err != nil {
			return nil, Ballot{}, fmt.Errorf("%w: %v", ErrInvalidMetadata, err)
		}
		if raw == nil {
			raw = map[string]json.RawMessage{}
		}
	}
	b := Ballot{Props: map[string]StoredProposal{}, Votes: map[string]Vote{}}
	if ms, ok := raw[MetadataKey]; ok && len(ms) > 0 && string(ms) != "null" {
		// A malformed svm_ms of another validator is just ignored by readers;
		// for our own account the caller treats it as invalid metadata.
		var parsed struct {
			Props map[string]json.RawMessage `json:"props"`
			Votes map[string]Vote            `json:"votes"`
		}
		if err := json.Unmarshal(ms, &parsed); err != nil {
			return raw, b, fmt.Errorf("multisig: svm_ms is malformed: %w", err)
		}
		for id, p := range parsed.Props {
			var sp StoredProposal
			if err := json.Unmarshal(p, &sp); err != nil {
				continue // unsupported operations or bad shape: unverifiable, skipped
			}
			b.Props[id] = sp
		}
		for id, v := range parsed.Votes {
			b.Votes[id] = v
		}
	}
	return raw, b, nil
}

// EncodeMetadata replaces only svm_ms in the raw metadata and returns the new
// posting_json_metadata string. Other keys keep their exact bytes (no HTML
// escaping, no reformatting).
func EncodeMetadata(raw map[string]json.RawMessage, b Ballot) (string, error) {
	ballot, err := marshalNoEscape(b)
	if err != nil {
		return "", err
	}
	out := make(map[string]json.RawMessage, len(raw)+1)
	for k, v := range raw {
		out[k] = v
	}
	out[MetadataKey] = ballot

	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := marshalNoEscape(k)
		if err != nil {
			return "", err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(bytes.TrimSpace(out[k]))
	}
	buf.WriteByte('}')
	return buf.String(), nil
}

func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Prune drops proposals whose tx expired and votes whose x expired more than
// PruneAfter before now, then (if the ballot is still too large) the oldest
// remaining entries until it fits.
func (b *Ballot) Prune(now time.Time) {
	cut := now.Add(-PruneAfter)
	for id, p := range b.Props {
		if t, err := time.Parse(steemtx.TimeLayout, p.Tx.Expiration); err == nil && t.Before(cut) {
			delete(b.Props, id)
		}
	}
	for id, v := range b.Votes {
		if t, err := time.Parse(steemtx.TimeLayout, v.X); err == nil && t.Before(cut) {
			delete(b.Votes, id)
		}
	}
	for {
		enc, err := marshalNoEscape(b)
		if err != nil || len(enc) <= maxBallotBytes {
			return
		}
		if !b.dropOldest(now) {
			return
		}
	}
}

// dropOldest removes the entry with the earliest expiration that is already
// expired. Live entries are never dropped.
func (b *Ballot) dropOldest(now time.Time) bool {
	oldestID, oldestIsProp := "", false
	var oldest time.Time
	consider := func(id, exp string, isProp bool) {
		t, err := time.Parse(steemtx.TimeLayout, exp)
		if err != nil || !t.Before(now) {
			return
		}
		if oldestID == "" || t.Before(oldest) {
			oldestID, oldest, oldestIsProp = id, t, isProp
		}
	}
	for id, p := range b.Props {
		consider(id, p.Tx.Expiration, true)
	}
	for id, v := range b.Votes {
		consider(id, v.X, false)
	}
	if oldestID == "" {
		return false
	}
	if oldestIsProp {
		delete(b.Props, oldestID)
	} else {
		delete(b.Votes, oldestID)
	}
	return true
}
