# Building a Go signer for the svm.bank multisig

This guide is for Go developers and validator operators who want to run an automated signer for **@svm.bank**, the SVM Gateway Bank on Steem. A signer is a long-running Go program that each validator runs with their own keys. It:

- watches for proposals from any validator,
- decides with its own rules whether to approve or deny each one,
- publishes its signature or denial on-chain,
- executes a proposal once enough validators have approved it,
- creates proposals of its own: sending STEEM or SBD, and adding, removing or reweighting signatories.

The signer uses the same on-chain protocol as the existing web dashboard and command-line tools in this repository, so validators can mix them freely. One validator can run the Go signer while another approves in the browser with Steem Keychain. Nothing in the protocol depends on a central server.

Everything byte-level in this guide (operation IDs, encodings, signature rules, limits) was checked against working code that has executed transactions on mainnet and against the Steem source. Section 4.8 has test vectors from real executed transactions so you can verify your implementation before it touches funds.

**A ready-made signer ships in all three oracle clients**: `oracle/go/multisig`, `oracle/python/src/steemvm_oracle/multisig/` and `oracle/js/src/multisig/`. Enable it with `ORACLE_MULTISIG_ENABLED=true` (section 7). It implements this guide, with the exact rules in `oracle/PROTOCOL.md` §11. Where this guide leaves a choice open, such as the payout memo, the weight arithmetic, the snapshot rule and who proposes, §11 fixes it so every validator's signer agrees. All three clients are held to the shared vectors in `oracle/multisig/vectors.json`.

## Contents

1. [How the multisig works](#1-how-the-multisig-works)
2. [The proposal protocol](#2-the-proposal-protocol)
3. [Transaction details](#3-transaction-details)
4. [Steem essentials in Go](#4-steem-essentials-in-go)
5. [Signer architecture](#5-signer-architecture)
6. [Decentralized participation](#6-decentralized-participation)
7. [Running the signer](#7-running-the-signer)
8. [Safety checklist](#8-safety-checklist)
9. [Reference](#9-reference)

---

## 1. How the multisig works

### 1.1 The account

@svm.bank has no keys of its own. Its owner, active and posting authorities each contain only **account authorities**: a list of validator accounts with a weight, plus a threshold. A transaction from svm.bank is valid when it carries signatures from validators whose weights add up to at least the threshold.

On 2026-10-14 the authority was:

| Validator | Weight | Share |
|---|---:|---:|
| blazed007 | 6,980 | 69.78% |
| svm-deployer | 312 | 3.12% |
| blaze.apps | 309 | 3.09% |
| cur8 | 309 | 3.09% |
| dhaka.witness | 309 | 3.09% |
| xpilar.witness | 306 | 3.06% |
| yasu | 306 | 3.06% |
| happycapital | 303 | 3.03% |
| steem-agora | 303 | 3.03% |
| steem-space | 300 | 3.00% |
| upex | 266 | 2.66% |
| **Threshold** | **7,503 of 10,003** | **75%** |

Always read the live values from the chain; never hard-code them. The weights follow each validator's stake in the SVM network (section 5.6).

The weights above add up to 10,003, not 10,000, because each weight was rounded separately. The automated signers apportion with exact integer arithmetic (section 5.6), so their first validator-set sync moves the authority to exactly 10,000 with threshold 7,500. That first sync still needs approvals reaching the current threshold of 7,503: blazed007 plus at least two others.

### 1.2 Who signs

An account authority is satisfied by that account's **active** authority. So a validator approves an svm.bank transaction by signing it with **their own active key**. This holds for transfers (which need svm.bank's active authority) and for authority changes (which need svm.bank's owner authority): Steem resolves both through the validators' active keys.

Two consequences:

- A validator can rotate their own keys at any time. svm.bank refers to validators by account name, so nothing on svm.bank changes.
- The signer never needs an owner key. Do not give it one.

### 1.3 The flow

```
 proposer's signer            other validators' signers             anyone
 ─────────────────            ─────────────────────────             ──────
 build unsigned tx
 publish it in own      ──►   read it from the proposer's
 profile metadata             metadata, check it with own rules
 (optionally + own             │
  signature)                   ├─ approve: sign tx with active key,
                               │  publish signature in own metadata
                               └─ deny: publish a denial
                                         │
                        approvals ≥ threshold ──────────────►  combine signatures,
                                                               broadcast the tx
```

Proposals and votes are stored in each validator's own `posting_json_metadata`. Every participant reads the same public data and applies the same rules (section 2), so they all reach the same view of every proposal without trusting each other or a server.

---

## 2. The proposal protocol

This section is the contract. Any client that follows it interoperates with the web dashboard, the command-line tools and other Go signers.

### 2.1 Where data lives

Each validator keeps a key named `svm_ms` inside the JSON object stored in their account's `posting_json_metadata`. Everything else in that object (profile, avatar and so on) must be preserved.

```json
{
  "profile": { "name": "…", "profile_image": "…" },
  "svm_ms": {
    "props": {
      "4b3ae1add30535891552adb33e130fa71deadfdf": {
        "t": "Set recovery account to @null",
        "tx": {
          "ref_block_num": 35715,
          "ref_block_prefix": 3778233551,
          "expiration": "2026-10-11T01:48:45",
          "operations": [["change_recovery_account", {"account_to_recover": "svm.bank", "new_recovery_account": "null", "extensions": []}]],
          "extensions": []
        },
        "c": "2026-10-11T00:50:46"
      }
    },
    "votes": {
      "4b3ae1add30535891552adb33e130fa71deadfdf": { "a": 1, "s": "1f…(130 hex chars)", "x": "2026-10-11T01:48:45" },
      "<another trx_id>": { "a": 0, "x": "2026-10-11T03:12:00" }
    }
  }
}
```

| Field | Meaning |
|---|---|
| `props` | Proposals **this validator created**, keyed by proposal ID |
| `props[id].t` | Title shown to validators |
| `props[id].tx` | The exact unsigned transaction validators sign |
| `props[id].c` | Creation time, UTC, `YYYY-MM-DDTHH:MM:SS` |
| `votes` | This validator's votes on any proposal, keyed by proposal ID |
| `votes[id].a` | `1` = approve, `0` = deny |
| `votes[id].s` | Approvals only: this validator's signature over `tx`, hex |
| `votes[id].x` | The transaction's expiration, used for pruning |

### 2.2 Proposal IDs

A proposal's ID is the Steem transaction ID of its unsigned transaction: the first 20 bytes of `sha256(serialized transaction)`, as lowercase hex, without the chain ID (section 4.4).

When you read a proposal, recompute the ID from `tx` and **ignore the proposal if it doesn't match the key**. That stops anyone from showing a title or ID that doesn't belong to the transaction.

### 2.3 Proposal transactions

- Every operation must act on svm.bank: `from` / `account` / `account_to_recover` must be `svm.bank`.
- Set `expiration` to the head block time plus **58 minutes**. Steem rejects anything more than 60 minutes ahead, and 58 leaves room for clock skew.
- Take `ref_block_num` and `ref_block_prefix` from the head block at creation time (section 4.3).
- `extensions` is always `[]`. The stored `tx` has no `signatures` field.

Because of the one-hour limit, a proposal must collect enough approvals and be executed within 58 minutes of being created. After that it can only be proposed again as a new transaction with a new ID. Section 5.5 covers renewing automatically.

### 2.4 Votes

To **approve**, sign the proposal's transaction digest with your active key (section 4.6) and publish `{"a": 1, "s": "<sig>", "x": "<tx expiration>"}` under the proposal ID in your own `svm_ms.votes`.

To **deny**, publish `{"a": 0, "x": "<tx expiration>"}`.

To change your mind, overwrite your entry. Your latest published vote counts.

When counting votes, a signer must:

1. Only count votes from validators in svm.bank's **current** authority.
2. For approvals, recover the public key from the signature and the digest (section 4.7). Count it only if that key is in the validator's current active `key_auths` with enough weight to meet their active threshold. Otherwise treat the vote as an **invalid signature**: it doesn't count, and you can show it so the validator re-approves.

### 2.5 Status

Every client derives the status of each proposal from chain data with these rules, applied in order:

| Status | Rule |
|---|---|
| `executed` | The proposal ID appears as a `trx_id` in svm.bank's account history |
| `expired` | Now ≥ `tx.expiration` and not executed |
| `rejected` | Deny weight > total weight − threshold (the threshold can no longer be reached) |
| `ready` | Approve weight ≥ threshold |
| `open` | Anything else |

Weights come from svm.bank's authority **at the time you evaluate**. Steem checks signatures against the authority in force when a transaction is broadcast, so if the validator set changes while a proposal is open, recompute with the new set. For an already executed proposal, the set that applied is the last authority change in history before the execution.

### 2.6 Execution

Anyone may execute a `ready` proposal; it doesn't need a key.

1. Walk svm.bank's `account_auths` **in the chain's order (sorted by account name)** and collect valid approval signatures until their weights reach the threshold. Stop there.
2. Broadcast `tx` plus `"signatures": [those signatures]` with `condenser_api.broadcast_transaction_synchronous`.

Include only the signatures you need. Steem stops counting as soon as the threshold is met and **rejects transactions with unnecessary signatures**. Walking in sorted order means every included signature is one the chain actually counts.

If the broadcast fails with `Duplicate transaction check failed`, the transaction is already on-chain: someone else executed it. Treat that as success.

### 2.7 Publishing to your metadata

`account_update2` replaces the whole `posting_json_metadata` string, so every write is a read-modify-write:

1. Read your account fresh with `get_accounts`.
2. Parse `posting_json_metadata`. If it isn't valid JSON, **stop and alert**; never overwrite it.
3. Change only `svm_ms`, then prune: remove `props` whose `tx.expiration` and `votes` whose `x` are more than **3 days** in the past.
4. Broadcast `account_update2` with `json_metadata: ""` (empty means unchanged) and the new `posting_json_metadata`, signed with your **posting** key. Your active key also works, since it satisfies posting authority.

Run **one writer per validator account**. Two processes writing the same account will overwrite each other's votes. Each write costs Resource Credits (RC) from the validator's Steem Power.

### 2.8 Which proposals to trust

- Only read proposals from accounts in svm.bank's current owner authority. Ignore everyone else.
- Always check the ID (section 2.2), the target account (section 2.3) and the operation content with your own policy (section 5.3). The proposer's title is a label, not a statement of fact.

---

## 3. Transaction details

These are the operations a signer builds or reviews. JSON is the form used in stored proposals and in `broadcast_transaction_synchronous`. Section 4.2 gives the binary encoding.

### 3.1 Send STEEM or SBD

```json
["transfer", {
  "from": "svm.bank",
  "to": "recipient-account",
  "amount": "12.500 SBD",
  "memo": "svm-withdrawal 42"
}]
```

- `amount` always has exactly 3 decimals and the symbol `STEEM` or `SBD`.
- The memo is public and at most 2,048 bytes. For an SVM withdrawal payout it must be **exactly** `svm-withdrawal <id>`: the SVM withdrawal ID in decimal, with no leading zeros and no other text. That is the format the bridge relays parse. They attest the payout on SVM (`MsgAttestWithdrawalPayout`), which marks the withdrawal PROCESSED. A payout with any other memo is never attested, so SVM automatically refunds the withdrawal after `withdrawal_timeout_blocks` and the user is paid twice. The memo is also how every validator checks the payout and how nobody pays the same withdrawal twice (section 5.3).
- A **burn** is a transfer to `null`.

### 3.2 Add, remove or reweight signatories

One `account_update` sets the new validator set and threshold on all three authorities:

```json
["account_update", {
  "account": "svm.bank",
  "owner":   { "weight_threshold": 7503, "account_auths": [["blaze.apps", 309], ["blazed007", 6980], …], "key_auths": [] },
  "active":  { …same as owner… },
  "posting": { …same as owner… },
  "memo_key": "STM8LWVtBAjP9Hq8vXQfw15nfnnaNMSiZmvEpLEy6G3jjPFPxerAS",
  "json_metadata": ""
}]
```

| Change | How |
|---|---|
| Add a signatory | Add `[name, weight]` to `account_auths` |
| Remove a signatory | Leave them out |
| Update a weight | Change their number |
| Change the threshold | Change `weight_threshold` |

Rules, all enforced by the chain or required for signatures to verify:

- **Sort `account_auths` by account name** (byte order, which is Go's `<` on strings). The chain stores them sorted and verifies signatures over the sorted encoding. An unsorted list produces a different digest, so every signature would fail.
- Each account appears once and must exist. Weights are whole numbers from 1 to 65,535. At most 40 accounts.
- `weight_threshold` must not exceed the sum of the weights, or the account is locked forever.
- Keep `key_auths` empty. Any key in it can sign without the validators.
- Copy `memo_key` from the current account unchanged.
- `json_metadata: ""` leaves the profile untouched.
- This changes the owner authority, so it needs approvals reaching the **current** owner threshold. Steem allows **one owner change per hour**: execution fails if the last change was less than 60 minutes ago.

### 3.3 Other operations the dashboard can propose

A Go signer should at least recognize these so it can review them:

```json
["transfer_to_vesting", {"from": "svm.bank", "to": "svm.bank", "amount": "100.000 STEEM"}]
["withdraw_vesting", {"account": "svm.bank", "vesting_shares": "161245.628500 VESTS"}]
["withdraw_vesting", {"account": "svm.bank", "vesting_shares": "0.000000 VESTS"}]
["change_recovery_account", {"account_to_recover": "svm.bank", "new_recovery_account": "null", "extensions": []}]
```

- `withdraw_vesting` starts a power-down; `0.000000 VESTS` stops it. Payouts come in 13 weekly installments. To convert Steem Power to VESTS, multiply by `total_vesting_shares / total_vesting_fund_steem` from the dynamic global properties.
- A recovery change applies 30 days after execution. If a change is pending, proposing the current recovery account cancels it.

### 3.4 Publishing votes and proposals

```json
["account_update2", {
  "account": "your-validator-account",
  "json_metadata": "",
  "posting_json_metadata": "{\"profile\":{…},\"svm_ms\":{…}}",
  "extensions": []
}]
```

Leave out `owner`, `active`, `posting` and `memo_key`. That keeps the operation at posting level, so it can be signed with the posting key.

---

## 4. Steem essentials in Go

The code below uses only these modules:

```
github.com/decred/dcrd/dcrec/secp256k1/v4   // keys, signing, recovery
github.com/btcsuite/btcd/btcutil            // base58
golang.org/x/crypto                         // ripemd160
```

It's written for clarity. Add your own error wrapping and tests. Keep everything in one package (here `steem`) or split it as you like.

### 4.1 JSON-RPC

```go
package steem

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"
)

// RPC calls Steem nodes in order, moving to the next one only on transport errors.
type RPC struct {
	Nodes []string
	HTTP  *http.Client
}

type ChainError struct{ Message string }

func (e *ChainError) Error() string { return e.Message }

var transient = regexp.MustCompile(`(?i)upstream|circuit breaker|timeout|unavailable`)

func (c *RPC) Call(ctx context.Context, method string, params, out any) error {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	var lastErr error
	for _, node := range c.Nodes {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, node, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		var r struct {
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		err = json.NewDecoder(resp.Body).Decode(&r)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if r.Error != nil {
			if transient.MatchString(r.Error.Message) {
				lastErr = fmt.Errorf("%s: %s", node, r.Error.Message)
				continue
			}
			return &ChainError{r.Error.Message} // the chain rejected it; don't retry elsewhere
		}
		return json.Unmarshal(r.Result, out)
	}
	return lastErr
}

func NewRPC(nodes ...string) *RPC {
	return &RPC{Nodes: nodes, HTTP: &http.Client{Timeout: 20 * time.Second}}
}
```

| Method | Params | Used for |
|---|---|---|
| `condenser_api.get_accounts` | `[["svm.bank", "blaze.apps", …]]` | Authorities, keys, balances, `posting_json_metadata` |
| `condenser_api.get_dynamic_global_properties` | `[]` | Head block (for new transactions), VESTS rate |
| `condenser_api.get_account_history` | `["svm.bank", <head_block_number>, 100]` | Executed transactions (`trx_id`) and authority changes |
| `condenser_api.broadcast_transaction_synchronous` | `[<signed tx>]` | Executing and publishing |
| `database_api.find_change_recovery_account_requests` | `{"accounts": ["svm.bank"]}` | Pending recovery change |
| `condenser_api.get_current_median_history_price` | `[]` | STEEM price in SBD (optional) |

Two quirks seen on public nodes:

- `get_account_history` accepts a **limit of at most 100**.
- Requesting the newest history with a start of `-1` can return a stale cached answer. Pass the current `head_block_number` as the start instead: the node clamps it to the newest entry, and the request changes every block, so it can't be served from a cache.

Use several nodes, ideally including your own. `https://api.steemit.com` and `https://api.justyy.com` both work as public fallbacks.

### 4.2 Binary serialization

Steem signs a compact little-endian binary encoding of the transaction:

| Type | Encoding |
|---|---|
| `uint8` / `uint16` / `uint32` / `int64` | Little-endian, fixed size |
| varint | Unsigned LEB128 (7 bits per byte, high bit = more) |
| string | varint length + UTF-8 bytes |
| time | `uint32` Unix seconds (UTC) |
| asset | `int64` amount in smallest units + `uint8` precision + symbol padded with zeros to 7 bytes |
| public key | 33 bytes, compressed secp256k1 |
| optional X | `uint8` 0 (absent) or 1 followed by X |
| array / map | varint count + items |
| authority | `uint32` threshold + map(string → `uint16`) account_auths + map(pubkey → `uint16`) key_auths |
| operation | varint operation ID + fields in the order below |

| Operation | ID | Fields in order |
|---|---:|---|
| `transfer` | 2 | from string, to string, amount asset, memo string |
| `transfer_to_vesting` | 3 | from string, to string, amount asset |
| `withdraw_vesting` | 4 | account string, vesting_shares asset |
| `account_update` | 10 | account string, owner opt authority, active opt authority, posting opt authority, memo_key pubkey, json_metadata string |
| `change_recovery_account` | 26 | account_to_recover string, new_recovery_account string, extensions varint(0) |
| `account_update2` | 43 | account string, owner opt authority, active opt authority, posting opt authority, memo_key opt pubkey, json_metadata string, posting_json_metadata string, extensions varint(0) |

Transaction: `ref_block_num uint16`, `ref_block_prefix uint32`, `expiration time`, `operations array`, `extensions varint(0)`.

Assets: `STEEM` and `SBD` use precision 3; `VESTS` uses 6.

```go
package steem

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

const TimeLayout = "2006-01-02T15:04:05"

type Writer struct{ bytes.Buffer }

func (w *Writer) U8(v uint8)   { w.WriteByte(v) }
func (w *Writer) U16(v uint16) { var b [2]byte; binary.LittleEndian.PutUint16(b[:], v); w.Write(b[:]) }
func (w *Writer) U32(v uint32) { var b [4]byte; binary.LittleEndian.PutUint32(b[:], v); w.Write(b[:]) }
func (w *Writer) I64(v int64)  { var b [8]byte; binary.LittleEndian.PutUint64(b[:], uint64(v)); w.Write(b[:]) }
func (w *Writer) Str(s string) { w.Varint(uint32(len(s))); w.WriteString(s) }
func (w *Writer) Varint(v uint32) {
	for v >= 0x80 {
		w.WriteByte(byte(v) | 0x80)
		v >>= 7
	}
	w.WriteByte(byte(v))
}
func (w *Writer) Time(s string) error {
	t, err := time.Parse(TimeLayout, s)
	if err != nil {
		return err
	}
	w.U32(uint32(t.Unix()))
	return nil
}

// ---- assets ----

type Asset struct {
	Amount    int64 // smallest units: "1.000 STEEM" is 1000
	Precision uint8
	Symbol    string
}

var precisions = map[string]uint8{"STEEM": 3, "SBD": 3, "VESTS": 6}

// ParseAsset parses "12.500 SBD" exactly, without floating point.
func ParseAsset(s string) (Asset, error) {
	f := strings.Fields(s)
	if len(f) != 2 {
		return Asset{}, fmt.Errorf("bad asset %q", s)
	}
	prec, ok := precisions[f[1]]
	if !ok {
		return Asset{}, fmt.Errorf("unknown symbol in %q", s)
	}
	whole, frac, _ := strings.Cut(f[0], ".")
	if len(frac) != int(prec) || strings.HasPrefix(whole, "-") {
		return Asset{}, fmt.Errorf("%q needs a positive amount with exactly %d decimals", s, prec)
	}
	n, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil {
		return Asset{}, err
	}
	return Asset{Amount: n, Precision: prec, Symbol: f[1]}, nil
}

func (a Asset) String() string {
	p := int64(1)
	for i := uint8(0); i < a.Precision; i++ {
		p *= 10
	}
	return fmt.Sprintf("%d.%0*d %s", a.Amount/p, int(a.Precision), a.Amount%p, a.Symbol)
}

func (w *Writer) Asset(a Asset) {
	w.I64(a.Amount)
	w.U8(a.Precision)
	var sym [7]byte
	copy(sym[:], a.Symbol)
	w.Write(sym[:])
}

// ---- authorities ----

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

type Authority struct {
	WeightThreshold uint32        `json:"weight_threshold"`
	AccountAuths    []AccountAuth `json:"account_auths"`
	KeyAuths        []KeyAuth     `json:"key_auths"`
}

// Sorted returns a copy with account_auths in chain order. Always sort before
// serializing or storing an authority you built.
func (a Authority) Sorted() Authority {
	out := a
	out.AccountAuths = append([]AccountAuth(nil), a.AccountAuths...)
	sort.Slice(out.AccountAuths, func(i, j int) bool { return out.AccountAuths[i].Name < out.AccountAuths[j].Name })
	if out.KeyAuths == nil {
		out.KeyAuths = []KeyAuth{}
	}
	return out
}

func (a Authority) Total() (sum uint32) {
	for _, x := range a.AccountAuths {
		sum += uint32(x.Weight)
	}
	for _, x := range a.KeyAuths {
		sum += uint32(x.Weight)
	}
	return sum
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

// Operation is ["type", {fields}] in JSON.
type Operation struct {
	Type  string
	Value any
}

func (op Operation) MarshalJSON() ([]byte, error) { return json.Marshal([]any{op.Type, op.Value}) }

func (op *Operation) UnmarshalJSON(b []byte) error {
	var raw [2]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if err := json.Unmarshal(raw[0], &op.Type); err != nil {
		return err
	}
	var v any
	switch op.Type {
	case "transfer":
		v = &Transfer{}
	case "transfer_to_vesting":
		v = &TransferToVesting{}
	case "withdraw_vesting":
		v = &WithdrawVesting{}
	case "account_update":
		v = &AccountUpdate{}
	case "change_recovery_account":
		v = &ChangeRecoveryAccount{}
	case "account_update2":
		v = &AccountUpdate2{}
	default:
		return fmt.Errorf("unsupported operation %q", op.Type) // treat as "deny"
	}
	if err := json.Unmarshal(raw[1], v); err != nil {
		return err
	}
	op.Value = derefOp(v)
	return nil
}

func derefOp(v any) any {
	switch x := v.(type) {
	case *Transfer:
		return *x
	case *TransferToVesting:
		return *x
	case *WithdrawVesting:
		return *x
	case *AccountUpdate:
		return *x
	case *ChangeRecoveryAccount:
		return *x
	case *AccountUpdate2:
		return *x
	}
	return v
}

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
		return errors.New("unsupported operation " + op.Type)
	}
	return nil
}
```

### 4.3 Transactions

```go
package steem

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"time"
)

type Transaction struct {
	RefBlockNum    uint16      `json:"ref_block_num"`
	RefBlockPrefix uint32      `json:"ref_block_prefix"`
	Expiration     string      `json:"expiration"`
	Operations     []Operation `json:"operations"`
	Extensions     []any       `json:"extensions"`
	Signatures     []string    `json:"signatures,omitempty"`
}

type DynamicProps struct {
	HeadBlockNumber       uint32 `json:"head_block_number"`
	HeadBlockID           string `json:"head_block_id"`
	Time                  string `json:"time"`
	TotalVestingShares    string `json:"total_vesting_shares"`
	TotalVestingFundSteem string `json:"total_vesting_fund_steem"`
}

// NewTransaction references the head block and expires ttl after the head
// block time. Use 58*time.Minute for proposals and a few minutes for votes.
func NewTransaction(p DynamicProps, ops []Operation, ttl time.Duration) (Transaction, error) {
	id, err := hex.DecodeString(p.HeadBlockID)
	if err != nil || len(id) < 8 {
		return Transaction{}, err
	}
	head, err := time.Parse(TimeLayout, p.Time)
	if err != nil {
		return Transaction{}, err
	}
	return Transaction{
		RefBlockNum:    uint16(p.HeadBlockNumber & 0xffff),
		RefBlockPrefix: binary.LittleEndian.Uint32(id[4:8]),
		Expiration:     head.Add(ttl).UTC().Format(TimeLayout),
		Operations:     ops,
		Extensions:     []any{},
	}, nil
}

// Serialize encodes the transaction without signatures.
func (tx Transaction) Serialize() ([]byte, error) {
	var w Writer
	w.U16(tx.RefBlockNum)
	w.U32(tx.RefBlockPrefix)
	if err := w.Time(tx.Expiration); err != nil {
		return nil, err
	}
	w.Varint(uint32(len(tx.Operations)))
	for _, op := range tx.Operations {
		if err := w.Operation(op); err != nil {
			return nil, err
		}
	}
	w.Varint(0) // extensions
	return w.Bytes(), nil
}
```

### 4.4 Digest and transaction ID

The Steem mainnet chain ID is 32 zero bytes.

```go
var ChainID = make([]byte, 32)

// Digest is what validators sign: sha256(chain_id || serialized tx).
func (tx Transaction) Digest() ([32]byte, error) {
	b, err := tx.Serialize()
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(append(append([]byte{}, ChainID...), b...)), nil
}

// ID is the Steem trx_id and the proposal ID: sha256(serialized tx)[:20].
func (tx Transaction) ID() (string, error) {
	b, err := tx.Serialize()
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:20]), nil
}
```

### 4.5 Keys

| Format | Encoding |
|---|---|
| Private key (WIF) | base58(`0x80` ‖ 32-byte key ‖ first 4 bytes of sha256(sha256(`0x80` ‖ key))) |
| Public key | `STM` + base58(33-byte compressed key ‖ first 4 bytes of ripemd160(key)) |
| Key from a master password | private key = sha256(account ‖ role ‖ password), role = `active`, `posting`, `owner` or `memo` |

```go
package steem

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"strings"

	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"golang.org/x/crypto/ripemd160"
)

func DecodeWIF(wif string) (*secp256k1.PrivateKey, error) {
	raw := base58.Decode(wif)
	if len(raw) != 37 || raw[0] != 0x80 {
		return nil, errors.New("not a WIF private key")
	}
	sum := sha256.Sum256(raw[:33])
	sum = sha256.Sum256(sum[:])
	if !bytes.Equal(sum[:4], raw[33:]) {
		return nil, errors.New("bad WIF checksum")
	}
	return secp256k1.PrivKeyFromBytes(raw[1:33]), nil
}

// KeyFromPassword derives a key the way Steem wallets do from a master password.
func KeyFromPassword(account, role, password string) *secp256k1.PrivateKey {
	seed := sha256.Sum256([]byte(account + role + password))
	return secp256k1.PrivKeyFromBytes(seed[:])
}

func DecodePublicKey(s string) ([]byte, error) {
	if !strings.HasPrefix(s, "STM") {
		return nil, errors.New("public key must start with STM")
	}
	raw := base58.Decode(s[3:])
	if len(raw) != 37 {
		return nil, errors.New("bad public key length")
	}
	h := ripemd160.New()
	h.Write(raw[:33])
	if !bytes.Equal(h.Sum(nil)[:4], raw[33:]) {
		return nil, errors.New("bad public key checksum")
	}
	return raw[:33], nil
}

func EncodePublicKey(pub *secp256k1.PublicKey) string {
	key := pub.SerializeCompressed()
	h := ripemd160.New()
	h.Write(key)
	return "STM" + base58.Encode(append(key, h.Sum(nil)[:4]...))
}
```

At startup, check that `EncodePublicKey(key.PubKey())` appears in your validator account's active `key_auths` (and posting, for the posting key). Refuse to start if it doesn't.

### 4.6 Signing

Steem verifies compact recoverable secp256k1 signatures and rejects non-canonical ones. A signature is accepted when:

- `s` is in the lower half of the curve order ("low-S"), and
- `r` and `s` each pass the byte check below: no high bit set in the first byte, and no leading `0x00` unless the next byte has its high bit set.

The deterministic signers in common Go libraries can't retry with a different nonce, and about half of all signatures fail the byte check. So build the signature from the library's primitives and retry with the next RFC 6979 nonce (`extraIterations`) until it passes. The existing JavaScript stack does the same.

The output is 65 bytes, hex encoded: `27 + 4 + recoveryID`, then `r`, then `s`. The first byte is always `31` or `32`, so the hex starts with `1f` or `20`.

```go
package steem

import (
	"encoding/hex"
	"errors"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// Sign returns a Steem-compatible signature of digest, as hex.
func Sign(key *secp256k1.PrivateKey, digest [32]byte) (string, error) {
	keyBytes := key.Key.Bytes()
	for attempt := uint32(0); attempt < 256; attempt++ {
		k := secp256k1.NonceRFC6979(keyBytes[:], digest[:], nil, nil, attempt)
		sig, recovery, ok := signWithNonce(&key.Key, k, digest[:])
		k.Zero()
		if !ok {
			continue
		}
		r, s := sig.R(), sig.S()
		var out [65]byte
		out[0] = 27 + 4 + recovery // 4 = compressed public key
		r.PutBytesUnchecked(out[1:33])
		s.PutBytesUnchecked(out[33:65])
		if isCanonical(out[1:]) {
			return hex.EncodeToString(out[:]), nil
		}
	}
	return "", errors.New("no canonical signature found")
}

// signWithNonce mirrors the ECDSA signing step in dcrd's ecdsa package,
// including low-S normalization, but takes the nonce as a parameter.
func signWithNonce(priv, k *secp256k1.ModNScalar, hash []byte) (*ecdsa.Signature, byte, bool) {
	var kG secp256k1.JacobianPoint
	secp256k1.ScalarBaseMultNonConst(k, &kG)
	kG.ToAffine()

	var xBytes [32]byte
	kG.X.PutBytes(&xBytes)
	var r secp256k1.ModNScalar
	overflow := r.SetBytes(&xBytes)
	if r.IsZero() {
		return nil, 0, false
	}
	recovery := byte(overflow<<1) | byte(kG.Y.IsOddBit())

	var e secp256k1.ModNScalar
	e.SetByteSlice(hash)
	kinv := new(secp256k1.ModNScalar).InverseValNonConst(k)
	s := new(secp256k1.ModNScalar).Mul2(priv, &r).Add(&e).Mul(kinv)
	if s.IsZero() {
		return nil, 0, false
	}
	if s.IsOverHalfOrder() {
		s.Negate()
		recovery ^= 0x01
	}
	return ecdsa.NewSignature(&r, s), recovery, true
}

// isCanonical is Steem's byte rule on r (rs[0:32]) and s (rs[32:64]).
func isCanonical(rs []byte) bool {
	return rs[0]&0x80 == 0 && !(rs[0] == 0 && rs[1]&0x80 == 0) &&
		rs[32]&0x80 == 0 && !(rs[32] == 0 && rs[33]&0x80 == 0)
}
```

### 4.7 Verifying a signature

```go
// RecoverSigner returns the public key (STM…) that produced sigHex over digest.
func RecoverSigner(digest [32]byte, sigHex string) (string, error) {
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != 65 {
		return "", errors.New("signature must be 65 bytes of hex")
	}
	if !isCanonical(sig[1:]) {
		return "", errors.New("signature is not canonical")
	}
	pub, _, err := ecdsa.RecoverCompact(sig, digest[:])
	if err != nil {
		return "", err
	}
	return EncodePublicKey(pub), nil
}
```

An approval counts when the recovered key is in the validator's current active `key_auths` with weight ≥ their active `weight_threshold` (section 2.4).

### 4.8 Test vectors

These are real svm.bank transactions that executed on mainnet. Your `Serialize`, `Digest`, `ID` and `RecoverSigner` must reproduce them exactly.

**Transfer** (executed 2026-09-27, approved by two validators):

```json
{"ref_block_num":28270,"ref_block_prefix":2711898870,"expiration":"2026-09-27T02:18:06",
 "operations":[["transfer",{"from":"svm.bank","to":"svm.bank","amount":"1.000 STEEM","memo":"multisig test"}]],
 "extensions":[]}
```

| | Value |
|---|---|
| Serialized | `6e6ef64aa4a1de7cb86a01020873766d2e62616e6b0873766d2e62616e6be80300000000000003535445454d00000d6d756c7469736967207465737400` |
| Digest | `426f69aabb998b3b5e9f79c879015ce523f2d34db048e4fea9341768ae25a4c7` |
| ID | `f4bc2c800eecf30226a37235886cc89a2e5b44e7` |
| Signature 1 | `1f1e0597e19740b2d55c182905d8bad43efa47282f4ee17b1105faf9bd88fab8fd3cd86c1f72937973de48fd820906c13f9a4b8dc8b8a250a372598b7e14a02927` |
| Recovers to | `STM5fvPvqwkwDTKXW1wsr7wPdeVXjetjvg1Wer8Zs7P24KFHht2Be` |
| Signature 2 | `1f5455c57294bc991743d3baa3584387d90010a8f8fc988e0b17d4a0e87db2d31c11ad211cd0581224a39b331b21862422ce39baaa0b8cad770dfda9bb0d959d99` |
| Recovers to | `STM4vpkJnwbFzFmFyEPpP6T8iMLn6rc9j8aa3PVo62yQnP9ck9sps` |

Reading the serialized bytes: `6e6e` ref_block_num · `f64aa4a1` ref_block_prefix · `de7cb86a` expiration · `01` one operation · `02` transfer · `08 73766d2e62616e6b` "svm.bank" twice · `e803000000000000` 1000 · `03` precision · `535445454d0000` "STEEM" · `0d …` memo · `00` extensions.

**Recovery change** (executed 2026-10-11, block 110,332,809):

```json
{"ref_block_num":35715,"ref_block_prefix":3778233551,"expiration":"2026-10-11T01:48:45",
 "operations":[["change_recovery_account",{"account_to_recover":"svm.bank","new_recovery_account":"null","extensions":[]}]],
 "extensions":[]}
```

| | Value |
|---|---|
| Serialized | `838bcf4433e1fdeaca6a011a0873766d2e62616e6b046e756c6c0000` |
| Digest | `4a5f4e05cf52dcebb03f2a8f4d065ea8276db6885f77d1272c2478b0d1df6578` |
| ID | `4b3ae1add30535891552adb33e130fa71deadfdf` |

**Validator change** (executed 2026-10-11, block 110,332,671): an `account_update` setting the 11-validator authority shown in section 1.1 on owner, active and posting, with memo key `STM8LWVtBAjP9Hq8vXQfw15nfnnaNMSiZmvEpLEy6G3jjPFPxerAS`, `json_metadata` `"{}"`, ref_block_num 35560, ref_block_prefix 4046415574, expiration `2026-10-11T01:40:57`.

| | Value |
|---|---|
| ID | `63ea81848ebc17e01d58ca41329c0dc9b48ea90a` |
| Digest | `b3dadf83c8a4c9cb1d8360569a793201b75b9478c847be17d66a2cba54e4c797` |

<details>
<summary>Serialized (490 bytes)</summary>

```
e88ad6662ff129e9ca6a010a0873766d2e62616e6b014f1d00000b0a626c617a652e61707073350109626c617a6564303037441b046375723835010d6468616b612e7769746e65737335010c68617070796361706974616c2f010b737465656d2d61676f72612f010b737465656d2d73706163652c010c73766d2d6465706c6f796572380104757065780a010e7870696c61722e7769746e65737332010479617375320100014f1d00000b0a626c617a652e61707073350109626c617a6564303037441b046375723835010d6468616b612e7769746e65737335010c68617070796361706974616c2f010b737465656d2d61676f72612f010b737465656d2d73706163652c010c73766d2d6465706c6f796572380104757065780a010e7870696c61722e7769746e65737332010479617375320100014f1d00000b0a626c617a652e61707073350109626c617a6564303037441b046375723835010d6468616b612e7769746e65737335010c68617070796361706974616c2f010b737465656d2d61676f72612f010b737465656d2d73706163652c010c73766d2d6465706c6f796572380104757065780a010e7870696c61722e7769746e6573733201047961737532010003c62b5716ac3b7c30be5710b1be73b5a6c8d41a4a8154149d3f3c128c3b525181027b7d00
```

</details>

Note `0a626c617a652e61707073` ("blaze.apps") comes before `09626c617a6564303037` ("blazed007"): `.` (0x2e) sorts before `d` (0x64).

**Keys** (a made-up account and password, safe to publish):

| | Value |
|---|---|
| Account, role, password | `svm.example`, `active`, `correct horse battery staple` |
| WIF | `5Hxy6PfPo8GPD2SHpMERWy2TBJ9WrKinYyBQLpPBoMBj2GxpXdg` |
| Public key | `STM897LiMytWqxuRnrNNMuAJRqjK9yWsVpVvoQzRM6n9wVBxcM3RJ` |
| Compressed key | `03ac492d0e65b7eb7f0a8499736a7e4192d967284904666881c11748d0ee227cc7` |

Also test that `Sign` followed by `RecoverSigner` returns the signing key's public key for many random digests, and that every output passes `isCanonical` and starts with `1f` or `20`.

---

## 5. Signer architecture

### 5.1 Components

```
cmd/svm-signer/        main: config, keys, the loop
steem/                 sections 4.1–4.7: RPC, encoding, keys, signing
multisig/
  state.go             load svm.bank, validators, history, metadata
  aggregate.go         proposals, votes, status (section 2)
  publish.go           read-modify-write of svm_ms (section 2.7)
  execute.go           minimal signature set + broadcast (section 2.6)
policy/                your rules: approve, deny or abstain; what to propose
store/                 optional local audit log (SQLite or files)
```

The signer is **stateless with respect to the protocol**. Everything it needs is re-derived from the chain on every pass, so restarting it is always safe. Local storage is only for audit and for your own business data, such as which SVM requests you've seen.

### 5.2 The main loop

Run a pass every 6 to 15 seconds. Steem produces a block every 3 seconds.

```go
func (s *Signer) Pass(ctx context.Context) error {
	view, err := multisig.Load(ctx, s.rpc, "svm.bank") // accounts, props, history, metadata
	if err != nil {
		return err // try again next pass
	}
	if !view.IsValidator(s.self) {
		return errors.New(s.self + " is no longer a validator of svm.bank")
	}

	// 1. Vote on every live proposal we haven't voted on yet (or whose vote is invalid).
	for _, p := range view.Proposals() {
		if p.Status != multisig.Open && p.Status != multisig.Ready {
			continue
		}
		if v := p.VoteOf(s.self); v != nil && v.Valid {
			continue
		}
		decision, reason := s.policy.Review(ctx, p, view)
		switch decision {
		case policy.Approve:
			sig, err := steem.Sign(s.activeKey, p.Digest)
			if err != nil {
				return err
			}
			err = s.publish(ctx, func(b *multisig.Ballot) { b.Votes[p.ID] = multisig.Vote{A: 1, S: sig, X: p.Tx.Expiration} })
			s.log.Info("approved", "id", p.ID, "reason", reason, "err", err)
		case policy.Deny:
			err := s.publish(ctx, func(b *multisig.Ballot) { b.Votes[p.ID] = multisig.Vote{A: 0, X: p.Tx.Expiration} })
			s.log.Info("denied", "id", p.ID, "reason", reason, "err", err)
		}
	}

	// 2. Execute proposals that have enough approvals (section 5.4).
	for _, p := range view.Ready() {
		s.maybeExecute(ctx, p, view)
	}

	// 3. Create or renew proposals this signer is responsible for (section 5.5).
	for _, intent := range s.policy.Intents(ctx, view) {
		s.propose(ctx, view, intent)
	}
	return nil
}
```

Batch the writes: if several votes are due in one pass, apply them all in **one** `account_update2`. That saves RC and avoids racing with yourself.

### 5.3 Policy: the signer's own logic

The policy decides what this validator approves. Every validator writes their own; they never have to agree on code, only on outcomes.

```go
package policy

type Decision int

const (
	Abstain Decision = iota // don't vote yet; review again next pass
	Approve
	Deny
)

type Policy interface {
	// Review decides on one proposal. It sees the full chain view.
	Review(ctx context.Context, p multisig.Proposal, v multisig.View) (Decision, string)
	// Intents lists transactions this signer wants svm.bank to make.
	Intents(ctx context.Context, v multisig.View) []Intent
}

type Intent struct {
	Key        string            // stable business key, e.g. the SVM withdrawal ID
	Title      string
	Operations []steem.Operation
}
```

A sensible baseline for a bridge:

| Operation | Approve when | Otherwise |
|---|---|---|
| `transfer` (payout) | The memo is exactly `svm-withdrawal <id>` for a withdrawal that is REQUESTED on SVM. Recipient and **net** amount match it exactly. It has not been paid yet: no svm.bank transfer with that memo exists. It is far enough from SVM's automatic refund (`oracle/PROTOCOL.md` §11.4) | Deny |
| `transfer` to `null` (burn) | The amount matches a burn your rules expect | Deny |
| `account_update` (signatories) | The new authority equals the set your own computation produces (section 5.6), byte for byte | Deny |
| `withdraw_vesting`, `transfer_to_vesting` | Only if your treasury rules call for it | Abstain or deny |
| `change_recovery_account` | Never automatically | Abstain (leave it to a human) |
| More than one operation, or anything unknown | Never | Deny |

The most important rule for a bridge is **verify independently**. Your signer approves a payout because it checked the SVM chain itself, not because another validator proposed it. If every validator's signer does this, a single compromised validator can propose anything but can't get it approved.

**Prevent double payouts.** Before approving or proposing a transfer, search svm.bank's history and all open proposals for the same reference. Approve at most one unexpired proposal per reference, and only the earliest correct one. If two proposals pay the same withdrawal, leave the later one unapproved. Don't deny it: a deny is final, and the later proposal may be the one to approve if the earlier one expires. Keep a local record of every approval you sign, written before you publish it. Then a lagging RPC node that hides your earlier approval can't trick you into signing a second one.

### 5.4 Executing

Anyone can execute, and several signers will notice the same `ready` proposal at once. Duplicates are harmless (the chain rejects the second with `Duplicate transaction check failed`), but you can avoid the noise:

```go
func (s *Signer) maybeExecute(ctx context.Context, p multisig.Proposal, v multisig.View) {
	// The proposer goes first; everyone else waits a little longer by rank.
	delay := time.Duration(v.Rank(s.self, p)) * 4 * time.Second
	if p.Creator == s.self {
		delay = 0
	}
	if time.Since(p.ReadyAt) < delay || time.Until(p.ExpiresAt) < 30*time.Second {
		return
	}
	tx := p.Tx
	tx.Signatures = p.MinimalSignatures(v.Authority) // sorted walk, stop at threshold
	var res struct {
		ID       string `json:"id"`
		BlockNum uint32 `json:"block_num"`
	}
	err := s.rpc.Call(ctx, "condenser_api.broadcast_transaction_synchronous", []any{tx}, &res)
	switch {
	case err == nil:
		s.log.Info("executed", "id", p.ID, "block", res.BlockNum)
	case strings.Contains(err.Error(), "Duplicate transaction"):
		s.log.Info("already executed", "id", p.ID)
	default:
		s.log.Warn("execute failed", "id", p.ID, "err", err) // retried next pass
	}
}
```

Execution can fail for reasons that need a person, for example insufficient balance or an owner change within the last hour. Alert on repeated failures instead of retrying forever; the proposal expires on its own.

### 5.5 Proposing and renewing

```go
func (s *Signer) propose(ctx context.Context, v multisig.View, in policy.Intent) {
	// Never duplicate: if a live proposal already has these exact operations, vote on it instead.
	if v.HasLiveProposalWithOps(in.Operations) || v.AlreadyExecuted(in.Key) {
		return
	}
	props := v.DynamicProps // fetched this pass
	tx, err := steem.NewTransaction(props, in.Operations, 58*time.Minute)
	if err != nil {
		return
	}
	id, _ := tx.ID()
	digest, _ := tx.Digest()
	sig, err := steem.Sign(s.activeKey, digest)
	if err != nil {
		return
	}
	// Publish the proposal and our approval in one write.
	err = s.publish(ctx, func(b *multisig.Ballot) {
		b.Props[id] = multisig.StoredProposal{T: in.Title, Tx: tx, C: time.Now().UTC().Format(steem.TimeLayout)}
		b.Votes[id] = multisig.Vote{A: 1, S: sig, X: tx.Expiration}
	})
	s.log.Info("proposed", "id", id, "title", in.Title, "err", err)
}
```

The stored `tx` must be **exactly** the transaction you signed. Other clients recompute its ID from the JSON, so don't reformat amounts or reorder `account_auths` after signing.

**Renewing.** If a proposal expires but its intent still stands (for example a withdrawal that's still unpaid), propose it again as a new transaction. To avoid several signers proposing the same thing, each intent has one designated proposer:

- for a payout, the member at index `withdrawal_id mod N` in name order;
- for a validator set, the member at index `(S / 1000) mod N`.

The member `k` places after the designated one may step in once the intent has been due for `k × 120 s`. The duplicate rules above keep it safe either way.

### 5.6 Keeping the validator set in sync

Validators are weighted by their stake in the SVM. To update signatories automatically and still be decentralized, every signer must compute the **same** authority from the same SVM data. Then any validator's signer can propose it, and every other signer approves only if its own computation matches exactly.

Use this deterministic procedure. It uses **integer arithmetic only**, so every signer gets byte-identical weights that add up to exactly 10,000.

Floating point can't do this. Stake is 18-decimal `asteem`, around 10²³, far beyond float64's 2⁵³ of exact integers. Rounding each weight on its own also drifts the sum: the live set was rounded that way and adds up to 10,003.

1. Take the SVM validators that are bonded and not jailed, and whose moniker is an ACTIVE Steem name link owned by the operator. Each gives `(account, tokens)` with tokens as a big integer.
2. Sort by tokens, highest first, then by account name. Keep the first 40.
3. For each validator, `q = (10000 × tokens) div total` and `r = (10000 × tokens) mod total`.
4. `deficit = 10000 − Σq`. Add 1 to the `deficit` validators with the largest `r`, breaking ties by account name. This is the largest-remainder method.
5. Minimum weight 1. In name order, set each zero weight to 1 and take that unit from the largest weight, breaking ties by name.
6. `threshold = ceil(10000 × thresholdPercent ÷ 100)`: 7,500 at 75%.
7. Sort `account_auths` by account name. Use the same authority for owner, active and posting.

The reference implementation is `Apportion` in `oracle/go/multisig/apportion.go`, with Python and JS twins. The `apportionment` cases in `oracle/multisig/vectors.json` pin it, including ties, the minimum weight and truncation to 40.

```go
func Apportion(entries []StakeEntry, thresholdPercent uint32) (steemtx.Authority, error) {
	// entries sorted by tokens desc, account asc; top 40; total = Σ tokens (math/big)
	for i, v := range vals {
		num := new(big.Int).Mul(big.NewInt(10000), v.Tokens)
		q, r := new(big.Int).QuoRem(num, total, new(big.Int))
		rows[i] = row{account: v.Account, weight: q.Int64(), rem: r}
	}
	// +1 to the (10000 − Σq) largest remainders (ties by name); then minimum weight 1
	// taken from the largest weight; threshold = (10000*P + 99) / 100; sort by name.
	…
}
```

**Snapshot rule.** Signers must compute from the same SVM state. The proposer uses the checkpoint `S = floor((H − 10) / 1000) × 1000` and titles the proposal `Validator set @svm:<S>`. Reviewers recompute at exactly height `S` using historical queries, and approve only if:

- `S` is a multiple of 1,000;
- `S` is at most 3,000 blocks old;
- the authorities match byte for byte.

The policy then:

- **Intents:** if the desired authority differs from the current one and no live proposal already carries it, propose it. To avoid churn from tiny stake movements, only propose when some weight changes by more than an agreed tolerance (for example 0.5 percentage points) or a validator joins or leaves.
- **Review:** approve an `account_update` only if its owner, active and posting are each equal to your desired authority, `key_auths` is empty, and `memo_key` is unchanged.

If two signers read different snapshots, they compute different sets and deny each other. Nothing changes until the next checkpoint, when they converge. That is the safe failure mode.

### 5.7 The ballot types

```go
package multisig

type Ballot struct {
	Props map[string]StoredProposal `json:"props"`
	Votes map[string]Vote           `json:"votes"`
}

type StoredProposal struct {
	T  string            `json:"t"`
	Tx steem.Transaction `json:"tx"`
	C  string            `json:"c"`
}

type Vote struct {
	A int    `json:"a"`           // 1 approve, 0 deny
	S string `json:"s,omitempty"` // signature, approvals only
	X string `json:"x"`           // tx expiration
}
```

Read and write `posting_json_metadata` as `map[string]json.RawMessage` and replace only the `svm_ms` entry. That way keys your program doesn't know about survive untouched.

---

## 6. Decentralized participation

### 6.1 No central party

| Concern | How the protocol handles it |
|---|---|
| Where proposals live | In each validator's own on-chain metadata. Nobody can delete another validator's proposal or vote. |
| Who decides | Each validator, with their own key and their own rules. Votes are signatures, so they can't be forged. |
| Who executes | Anyone. Execution needs no key and no permission. |
| Agreeing on status | Every client applies the same rules (section 2.5) to the same public chain data. |
| Servers | Optional. The dashboard's server only relays and archives. Go signers can talk to Steem nodes directly. |

### 6.2 Mixing clients

These all read and write the same `svm_ms` data and can be used together:

- the **Go signer** (this guide), for automated validators;
- the **web dashboard**, signing with Steem Keychain or a key held in the browser tab;
- the **command-line tools** in this repository (`proposals.js`, `propose-validators.js`).

A typical setup: most validators run the Go signer for routine payouts and validator-set syncs, while people use the dashboard to review anything the signers abstain on, such as recovery changes.

### 6.3 Liveness

A proposal must reach the threshold within 58 minutes. With automated signers that usually takes seconds. Without them, validators have to be online together. To keep things moving:

- Keep enough validators running signers that together they hold more than the threshold. With today's weights that means **blazed007 plus at least two others**, because the other ten together hold 30.2%.
- Alert when your signer abstains on a proposal, so a person can review it within the window.
- Monitor your RC. A signer without RC can't publish votes.

### 6.4 When the validator set changes

- After a validator change executes, the new authority governs everything that follows, including the next validator change.
- Approvals on still-open proposals are re-weighted under the new set. Approvals from removed validators no longer count, and newly added validators can approve.
- A newly added validator needs only Steem Keychain, the dashboard or their own signer with their own keys. Nothing has to be handed over.
- Never remove so many validators at once that the remaining active ones can't reach the threshold. Simulate the new authority against who is actually running signers before approving.

### 6.5 Recovery

svm.bank's recovery account is changing to `@null` on 2026-11-10. After that, nobody can recover the account if the validators' control is lost. Lost control can only happen if so many validators lose their keys that the rest can't reach the threshold. Validators can always rotate their own keys, so keep the set healthy with section 6.4.

---

## 7. Running the signer

### 7.1 Configuration

The signer runs inside each validator's oracle client, whichever of Go, Python or JS that is, and reads the same `oracle/.env`:

```sh
ORACLE_MULTISIG_ENABLED=true
ORACLE_STEEM_ACTIVE_WIF=5K…        # your validator account's ACTIVE key: the only Steem secret
ORACLE_STEEM_RPCS=https://your-own-steem-node.example,https://api.steemit.com,https://api.justyy.com
# ORACLE_MULTISIG_DRY_RUN=true     # log every decision, publish nothing (start here)
```

The signer votes as your validator's verified Steem account, which is its moniker as an ACTIVE name link. It refuses to start in any of these cases:

- an owner key, posting key or master password variable is set;
- the active key doesn't match that account;
- the active key is also one of the account's owner keys;
- the conformance vectors fail.

The other knobs are interval, threshold, tolerance, refund margin, propose/execute and optional per-payout caps. Their defaults are in `oracle/PROTOCOL.md` §11.7. Keep threshold, tolerance and margin identical across validators: they are rules every signer must agree on.

### 7.2 Running it

```sh
docker compose --profile go up -d        # or --profile python / --profile js: exactly one
docker compose logs -f oracle-go         # every decision is logged with the proposal ID and reason
```

There are no separate commands. Each pass votes, proposes and executes on its own. A person reviews anything the signer abstains on (treasury and recovery operations) in the dashboard, and can always override by voting there.

### 7.3 Deployment

- Run exactly **one** signer per validator account (section 2.7). The signer holds an exclusive lock in its state directory, and a second one on the same volume refuses to start.
- Keep `ORACLE_STATE_DIR` on a persistent volume (the compose file already does this). It holds the approval journal, the write-ahead record that keeps the signer from ever approving two payouts for one withdrawal.
- Start with `ORACLE_MULTISIG_DRY_RUN=true`, check the logged decisions against the dashboard, then turn it off.

---

## 8. Safety checklist

- [ ] `selftest` passes: serialization, IDs, digests and signature recovery match section 4.8.
- [ ] The signer refuses to start if its keys don't match the validator account's on-chain keys.
- [ ] No owner key anywhere in the deployment.
- [ ] Proposal ID recomputed and checked on every proposal read.
- [ ] Only proposals from svm.bank's current owner authority are considered.
- [ ] Unknown operations and multi-operation proposals are denied.
- [ ] Payouts are checked against the SVM chain independently, never inside the refund margin, with the exact `svm-withdrawal <id>` memo.
- [ ] Double payouts are impossible: at most one approved unexpired or executed transfer per SVM withdrawal, backed by a local write-ahead approval record.
- [ ] Validator changes are approved only when they equal the locally computed authority.
- [ ] `account_auths` are sorted before serializing, storing or signing.
- [ ] Metadata writes preserve all other keys and stop on invalid JSON.
- [ ] One writer per validator account.
- [ ] Execution includes only the minimal signature set, in sorted order.
- [ ] Alerts for: abstentions, repeated execution failures, low RC, RPC failures, and being removed from the validator set.

| Failure | What you'll see | Fix |
|---|---|---|
| Unsorted `account_auths` | `Missing Active/Owner Authority` though enough validators approved | Sort, re-propose |
| Too many signatures | `Unnecessary signature(s) detected` | Use the minimal set (section 2.6) |
| Non-canonical signature | `signature is not canonical` | Use `Sign` from section 4.6 |
| Expired | `transaction expiration exception` | Propose again (section 5.5) |
| Owner changed within the hour | `Owner authority can only be updated once an hour` | Execute after the hour passes, within the 58-minute window |
| Already executed | `Duplicate transaction check failed` | Treat as success |
| Stale history | An executed proposal still looks ready | Use `head_block_number` as the history start (section 4.1) |
| History paging | `start must be greater than limit` | `get_account_history` needs `start ≥ limit` and returns `[start − limit, start]`; scan forward with `start = next + limit` |

---

## 9. Reference

### Chain constants (Steem mainnet)

| Constant | Value |
|---|---|
| Chain ID | 32 zero bytes |
| Public key prefix | `STM` |
| Max time until expiration | 3,600 s (proposals use 3,480 s) |
| Max accounts or keys in one authority | 40 |
| Account authority nesting depth | 2 |
| Max transaction size | 65,536 bytes |
| Max memo size | 2,048 bytes |
| Owner update limit | once per 60 minutes |
| Recovery account change delay | 30 days |
| Power-down | 13 weekly installments |
| Account name length | 3–16 characters |
| Block interval | 3 s |

### Protocol constants (this multisig)

| Constant | Value |
|---|---|
| Account | `svm.bank` |
| Metadata key | `svm_ms` in `posting_json_metadata` |
| Proposal lifetime | 58 minutes |
| Pruning | 3 days after expiration |
| Proposal ID | `hex(sha256(serialized tx)[:20])` |

### Glossary

| Term | Meaning |
|---|---|
| Validator / signatory | An account in svm.bank's account authorities |
| Influence | A validator's weight divided by the total weight |
| Threshold | The weight approvals must reach |
| Proposal | An unsigned transaction published for validators to approve |
| Approval | A validator's signature over a proposal, published in their metadata |
| Execution | Broadcasting a proposal with enough signatures |
