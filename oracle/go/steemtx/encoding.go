package steemtx

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TimeLayout is Steem's UTC timestamp format.
const TimeLayout = "2006-01-02T15:04:05"

// Writer builds Steem's compact little-endian binary encoding.
type Writer struct{ bytes.Buffer }

func (w *Writer) U8(v uint8) { w.WriteByte(v) }
func (w *Writer) U16(v uint16) {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], v)
	w.Write(b[:])
}
func (w *Writer) U32(v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	w.Write(b[:])
}
func (w *Writer) I64(v int64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(v))
	w.Write(b[:])
}

// Varint writes an unsigned LEB128 integer.
func (w *Writer) Varint(v uint32) {
	for v >= 0x80 {
		w.WriteByte(byte(v) | 0x80)
		v >>= 7
	}
	w.WriteByte(byte(v))
}

// Str writes a varint length followed by the UTF-8 bytes.
func (w *Writer) Str(s string) {
	w.Varint(uint32(len(s)))
	w.WriteString(s)
}

// Time writes a Steem timestamp as uint32 Unix seconds (UTC).
func (w *Writer) Time(s string) error {
	t, err := time.Parse(TimeLayout, s)
	if err != nil {
		return err
	}
	w.U32(uint32(t.Unix()))
	return nil
}

// ---- assets ----

// Asset is an amount in smallest units: "1.000 STEEM" is {1000, 3, "STEEM"}.
type Asset struct {
	Amount    int64
	Precision uint8
	Symbol    string
}

var precisions = map[string]uint8{"STEEM": 3, "SBD": 3, "VESTS": 6}

// ParseAsset parses "12.500 SBD" exactly, without floating point. It requires
// exactly the symbol's number of decimals and a non-negative amount.
func ParseAsset(s string) (Asset, error) {
	f := strings.Fields(s)
	if len(f) != 2 {
		return Asset{}, fmt.Errorf("bad asset %q", s)
	}
	prec, ok := precisions[f[1]]
	if !ok {
		return Asset{}, fmt.Errorf("unknown symbol in %q", s)
	}
	whole, frac, found := strings.Cut(f[0], ".")
	if !found || len(frac) != int(prec) || whole == "" || strings.HasPrefix(whole, "-") || strings.HasPrefix(whole, "+") {
		return Asset{}, fmt.Errorf("%q needs a non-negative amount with exactly %d decimals", s, prec)
	}
	for _, ch := range whole + frac {
		if ch < '0' || ch > '9' {
			return Asset{}, fmt.Errorf("bad digits in %q", s)
		}
	}
	n, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil {
		return Asset{}, err
	}
	return Asset{Amount: n, Precision: prec, Symbol: f[1]}, nil
}

// String formats the asset exactly, e.g. "12.500 SBD".
func (a Asset) String() string {
	p := int64(1)
	for i := uint8(0); i < a.Precision; i++ {
		p *= 10
	}
	return fmt.Sprintf("%d.%0*d %s", a.Amount/p, int(a.Precision), a.Amount%p, a.Symbol)
}

// FormatMilli formats a millisteem/millisbd integer as a 3-decimal Steem asset
// string, e.g. FormatMilli(12500, "SBD") == "12.500 SBD".
func FormatMilli(milli uint64, symbol string) string {
	return fmt.Sprintf("%d.%03d %s", milli/1000, milli%1000, symbol)
}

func (w *Writer) Asset(a Asset) {
	w.I64(a.Amount)
	w.U8(a.Precision)
	var sym [7]byte
	copy(sym[:], a.Symbol)
	w.Write(sym[:])
}

// ---- authorities ----

// AccountAuth is [name, weight] in JSON.
type AccountAuth struct {
	Name   string
	Weight uint16
}

func (a AccountAuth) MarshalJSON() ([]byte, error) { return json.Marshal([]any{a.Name, a.Weight}) }
func (a *AccountAuth) UnmarshalJSON(b []byte) error {
	var raw [2]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if err := json.Unmarshal(raw[0], &a.Name); err != nil {
		return err
	}
	return json.Unmarshal(raw[1], &a.Weight)
}

// KeyAuth is [STM-key, weight] in JSON.
type KeyAuth struct {
	Key    string
	Weight uint16
}

func (k KeyAuth) MarshalJSON() ([]byte, error) { return json.Marshal([]any{k.Key, k.Weight}) }
func (k *KeyAuth) UnmarshalJSON(b []byte) error {
	var raw [2]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if err := json.Unmarshal(raw[0], &k.Key); err != nil {
		return err
	}
	return json.Unmarshal(raw[1], &k.Weight)
}

// Authority is a Steem owner/active/posting authority.
type Authority struct {
	WeightThreshold uint32        `json:"weight_threshold"`
	AccountAuths    []AccountAuth `json:"account_auths"`
	KeyAuths        []KeyAuth     `json:"key_auths"`
}

// Sorted returns a copy with account_auths in chain order (byte order of the
// name) and a non-nil key_auths. Always sort before serializing or storing an
// authority you built: the chain verifies signatures over the sorted encoding.
func (a Authority) Sorted() Authority {
	out := a
	out.AccountAuths = append([]AccountAuth(nil), a.AccountAuths...)
	sort.Slice(out.AccountAuths, func(i, j int) bool { return out.AccountAuths[i].Name < out.AccountAuths[j].Name })
	// key_auths order is left as given: the chain orders them by binary public
	// key, not by the STM string, and the svm.bank signer always keeps them
	// empty anyway.
	out.KeyAuths = append([]KeyAuth(nil), a.KeyAuths...)
	if out.AccountAuths == nil {
		out.AccountAuths = []AccountAuth{}
	}
	if out.KeyAuths == nil {
		out.KeyAuths = []KeyAuth{}
	}
	return out
}

// Total is the sum of all weights.
func (a Authority) Total() (sum uint32) {
	for _, x := range a.AccountAuths {
		sum += uint32(x.Weight)
	}
	for _, x := range a.KeyAuths {
		sum += uint32(x.Weight)
	}
	return sum
}

// WeightOf returns the account_auths weight of name (0 if absent).
func (a Authority) WeightOf(name string) uint16 {
	for _, x := range a.AccountAuths {
		if x.Name == name {
			return x.Weight
		}
	}
	return 0
}

// Equal reports whether two authorities serialize identically.
func (a Authority) Equal(b Authority) bool {
	var wa, wb Writer
	if wa.Authority(a) != nil || wb.Authority(b) != nil {
		return false
	}
	return bytes.Equal(wa.Bytes(), wb.Bytes())
}

func (w *Writer) Authority(a Authority) error {
	w.U32(a.WeightThreshold)
	w.Varint(uint32(len(a.AccountAuths)))
	for _, x := range a.AccountAuths {
		w.Str(x.Name)
		w.U16(x.Weight)
	}
	w.Varint(uint32(len(a.KeyAuths)))
	for _, x := range a.KeyAuths {
		pk, err := DecodePublicKey(x.Key)
		if err != nil {
			return err
		}
		w.Write(pk)
		w.U16(x.Weight)
	}
	return nil
}

func (w *Writer) optAuthority(a *Authority) error {
	if a == nil {
		w.U8(0)
		return nil
	}
	w.U8(1)
	return w.Authority(*a)
}

// ---- operations ----

type Transfer struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Amount string `json:"amount"`
	Memo   string `json:"memo"`
}

type TransferToVesting struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Amount string `json:"amount"`
}

type WithdrawVesting struct {
	Account       string `json:"account"`
	VestingShares string `json:"vesting_shares"`
}

type AccountUpdate struct {
	Account      string     `json:"account"`
	Owner        *Authority `json:"owner,omitempty"`
	Active       *Authority `json:"active,omitempty"`
	Posting      *Authority `json:"posting,omitempty"`
	MemoKey      string     `json:"memo_key"`
	JSONMetadata string     `json:"json_metadata"`
}

type ChangeRecoveryAccount struct {
	AccountToRecover   string `json:"account_to_recover"`
	NewRecoveryAccount string `json:"new_recovery_account"`
	Extensions         []any  `json:"extensions"`
}

type AccountUpdate2 struct {
	Account             string     `json:"account"`
	Owner               *Authority `json:"owner,omitempty"`
	Active              *Authority `json:"active,omitempty"`
	Posting             *Authority `json:"posting,omitempty"`
	MemoKey             string     `json:"memo_key,omitempty"`
	JSONMetadata        string     `json:"json_metadata"`
	PostingJSONMetadata string     `json:"posting_json_metadata"`
	Extensions          []any      `json:"extensions"`
}

// Operation is ["type", {fields}] in JSON. Value holds one of the operation
// structs above (by value).
type Operation struct {
	Type  string
	Value any
}

func (op Operation) MarshalJSON() ([]byte, error) { return json.Marshal([]any{op.Type, op.Value}) }

// ErrUnsupportedOperation marks an operation type this signer cannot encode;
// a proposal containing one can never be verified and is never approved.
var ErrUnsupportedOperation = errors.New("unsupported operation")

func (op *Operation) UnmarshalJSON(b []byte) error {
	var raw [2]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if err := json.Unmarshal(raw[0], &op.Type); err != nil {
		return err
	}
	switch op.Type {
	case "transfer":
		var v Transfer
		err := json.Unmarshal(raw[1], &v)
		op.Value = v
		return err
	case "transfer_to_vesting":
		var v TransferToVesting
		err := json.Unmarshal(raw[1], &v)
		op.Value = v
		return err
	case "withdraw_vesting":
		var v WithdrawVesting
		err := json.Unmarshal(raw[1], &v)
		op.Value = v
		return err
	case "account_update":
		var v AccountUpdate
		err := json.Unmarshal(raw[1], &v)
		op.Value = v
		return err
	case "change_recovery_account":
		var v ChangeRecoveryAccount
		err := json.Unmarshal(raw[1], &v)
		if v.Extensions == nil {
			v.Extensions = []any{}
		}
		op.Value = v
		return err
	case "account_update2":
		var v AccountUpdate2
		err := json.Unmarshal(raw[1], &v)
		if v.Extensions == nil {
			v.Extensions = []any{}
		}
		op.Value = v
		return err
	default:
		return fmt.Errorf("%w %q", ErrUnsupportedOperation, op.Type)
	}
}

// Operation writes one operation (varint ID + fields in chain order).
func (w *Writer) Operation(op Operation) error {
	asset := func(s string) error {
		a, err := ParseAsset(s)
		if err == nil {
			w.Asset(a)
		}
		return err
	}
	switch v := op.Value.(type) {
	case Transfer:
		w.Varint(2)
		w.Str(v.From)
		w.Str(v.To)
		if err := asset(v.Amount); err != nil {
			return err
		}
		w.Str(v.Memo)
	case TransferToVesting:
		w.Varint(3)
		w.Str(v.From)
		w.Str(v.To)
		return asset(v.Amount)
	case WithdrawVesting:
		w.Varint(4)
		w.Str(v.Account)
		return asset(v.VestingShares)
	case AccountUpdate:
		w.Varint(10)
		w.Str(v.Account)
		for _, a := range []*Authority{v.Owner, v.Active, v.Posting} {
			if err := w.optAuthority(a); err != nil {
				return err
			}
		}
		pk, err := DecodePublicKey(v.MemoKey)
		if err != nil {
			return err
		}
		w.Write(pk)
		w.Str(v.JSONMetadata)
	case ChangeRecoveryAccount:
		w.Varint(26)
		w.Str(v.AccountToRecover)
		w.Str(v.NewRecoveryAccount)
		w.Varint(0)
	case AccountUpdate2:
		w.Varint(43)
		w.Str(v.Account)
		for _, a := range []*Authority{v.Owner, v.Active, v.Posting} {
			if err := w.optAuthority(a); err != nil {
				return err
			}
		}
		if v.MemoKey == "" {
			w.U8(0)
		} else {
			pk, err := DecodePublicKey(v.MemoKey)
			if err != nil {
				return err
			}
			w.U8(1)
			w.Write(pk)
		}
		w.Str(v.JSONMetadata)
		w.Str(v.PostingJSONMetadata)
		w.Varint(0)
	default:
		return fmt.Errorf("%w %q", ErrUnsupportedOperation, op.Type)
	}
	return nil
}
