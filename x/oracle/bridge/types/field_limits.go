package types

import (
	"unicode/utf8"

	errortypes "github.com/cosmos/cosmos-sdk/types/errors"
)

// Stateless upper bounds on the free-form string fields a client can put into
// a bridge message. Without these, a bonded validator's fee-exempt
// attestations (and any user's bridge-out) could write arbitrarily large
// values into permanent chain state at no cost. The limits are generous
// relative to what the Steem chain itself can produce, so they never reject an
// honest submission:
//
//   - MaxMemoBytes mirrors Steem's own 2048-byte transfer-memo cap, so any
//     memo that actually rode in on a Steem transfer fits. It also bounds the
//     bridge-out memo, which is relayed back to Steem as a transfer memo.
//   - MaxSteemTimestampBytes covers Steem's ISO-8601 block timestamp
//     ("2006-01-02T15:04:05", 19 chars) with ample slack.
//   - MaxSteemTxidBytes/op-index are already pinned by the txid regex and the
//     uint32 type; no separate limit is needed for them.
const (
	MaxMemoBytes           = 2048
	MaxSteemTimestampBytes = 64
)

// ValidateMemo bounds a free-form memo field and requires valid UTF-8 (the
// field is persisted and re-emitted in events and, for bridge-out, relayed to
// Steem). An empty memo is allowed.
func ValidateMemo(memo string) error {
	if len(memo) > MaxMemoBytes {
		return errortypes.ErrInvalidRequest.Wrapf("memo is %d bytes, over the %d-byte limit", len(memo), MaxMemoBytes)
	}
	if !utf8.ValidString(memo) {
		return errortypes.ErrInvalidRequest.Wrap("memo is not valid UTF-8")
	}
	return nil
}

// ValidateSteemTimestamp bounds the observed Steem block timestamp string. The
// bridge treats it as an opaque observed fact (it is part of the attested
// fact-set), so it only needs a length/UTF-8 guard, not full date parsing.
func ValidateSteemTimestamp(ts string) error {
	if len(ts) > MaxSteemTimestampBytes {
		return errortypes.ErrInvalidRequest.Wrapf("steem timestamp is %d bytes, over the %d-byte limit", len(ts), MaxSteemTimestampBytes)
	}
	if !utf8.ValidString(ts) {
		return errortypes.ErrInvalidRequest.Wrap("steem timestamp is not valid UTF-8")
	}
	return nil
}
