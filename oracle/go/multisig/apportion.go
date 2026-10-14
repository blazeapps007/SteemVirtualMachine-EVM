// Package multisig is the automated svm.bank signer: it reads proposals and
// votes from validators' posting_json_metadata (the svm_ms protocol in
// oracle/multisigguide.md section 2), decides on each with a deterministic
// verify-independently policy, publishes its votes, executes proposals that
// reach the threshold, and proposes withdrawal payouts and validator-set
// syncs itself.
package multisig

import (
	"errors"
	"fmt"
	"math/big"
	"sort"

	"steemvm/oracle/go/steemtx"
)

// TotalWeight is the fixed sum of all validator weights on svm.bank's
// owner, active and posting authorities.
const TotalWeight = 10000

// MaxAccountAuths is Steem's limit on accounts in one authority.
const MaxAccountAuths = 40

// StakeEntry is one validator's input to the apportionment: its Steem account
// and its bonded SVM tokens (asteem, an arbitrary-precision integer).
type StakeEntry struct {
	Account string
	Tokens  *big.Int
}

// Apportion computes the desired svm.bank authority from validator stakes
// with exact integer arithmetic (oracle/PROTOCOL.md, "svm.bank multisig
// signer"). Every signer must produce a byte-identical result from the same
// input, so this deliberately avoids floating point:
//
//  1. drop zero stakes; sort by tokens desc then account asc; keep the top 40;
//  2. q_i = floor(10000*t_i/T), r_i = (10000*t_i) mod T;
//  3. give +1 to the (10000 - sum q_i) entries with the largest r_i, ties by
//     account asc (largest-remainder), so the weights sum to exactly 10000;
//  4. enforce a minimum weight of 1 by taking from the largest weight (ties by
//     account asc), keeping the sum at exactly 10000;
//  5. threshold = ceil(10000 * thresholdPercent / 100);
//  6. account_auths sorted by name; key_auths empty.
func Apportion(entries []StakeEntry, thresholdPercent uint32) (steemtx.Authority, error) {
	if thresholdPercent == 0 || thresholdPercent > 100 {
		return steemtx.Authority{}, fmt.Errorf("multisig: threshold percent %d out of range 1..100", thresholdPercent)
	}
	vals := make([]StakeEntry, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.Tokens == nil || e.Tokens.Sign() <= 0 {
			continue
		}
		if seen[e.Account] {
			return steemtx.Authority{}, fmt.Errorf("multisig: duplicate account %q in stake input", e.Account)
		}
		seen[e.Account] = true
		vals = append(vals, e)
	}
	if len(vals) == 0 {
		return steemtx.Authority{}, errors.New("multisig: no validators with stake")
	}
	sort.Slice(vals, func(i, j int) bool {
		if c := vals[i].Tokens.Cmp(vals[j].Tokens); c != 0 {
			return c > 0
		}
		return vals[i].Account < vals[j].Account
	})
	if len(vals) > MaxAccountAuths {
		vals = vals[:MaxAccountAuths]
	}

	total := new(big.Int)
	for _, v := range vals {
		total.Add(total, v.Tokens)
	}

	type row struct {
		account string
		weight  int64
		rem     *big.Int
	}
	rows := make([]row, len(vals))
	sumQ := int64(0)
	scale := big.NewInt(TotalWeight)
	for i, v := range vals {
		num := new(big.Int).Mul(scale, v.Tokens)
		q, r := new(big.Int).QuoRem(num, total, new(big.Int))
		rows[i] = row{account: v.Account, weight: q.Int64(), rem: r}
		sumQ += q.Int64()
	}

	deficit := TotalWeight - sumQ
	if deficit < 0 || deficit >= int64(len(rows)) {
		return steemtx.Authority{}, fmt.Errorf("multisig: impossible deficit %d for %d validators", deficit, len(rows))
	}
	order := make([]int, len(rows))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		ra, rb := rows[order[a]], rows[order[b]]
		if c := ra.rem.Cmp(rb.rem); c != 0 {
			return c > 0
		}
		return ra.account < rb.account
	})
	for _, idx := range order[:deficit] {
		rows[idx].weight++
	}

	// Minimum weight 1, processing zero-weight entries in account order and
	// taking each unit from the current largest weight (ties by account asc).
	byName := make([]int, len(rows))
	for i := range byName {
		byName[i] = i
	}
	sort.Slice(byName, func(a, b int) bool { return rows[byName[a]].account < rows[byName[b]].account })
	for _, zi := range byName {
		for rows[zi].weight == 0 {
			rows[zi].weight = 1
			donor := -1
			for _, di := range byName {
				if donor == -1 || rows[di].weight > rows[donor].weight {
					donor = di
				}
			}
			rows[donor].weight--
		}
	}

	auth := steemtx.Authority{
		WeightThreshold: uint32((TotalWeight*uint64(thresholdPercent) + 99) / 100),
		KeyAuths:        []steemtx.KeyAuth{},
	}
	var sum int64
	for _, r := range rows {
		if r.weight < 1 || r.weight > 65535 {
			return steemtx.Authority{}, fmt.Errorf("multisig: weight %d for %s out of range", r.weight, r.account)
		}
		sum += r.weight
		auth.AccountAuths = append(auth.AccountAuths, steemtx.AccountAuth{Name: r.account, Weight: uint16(r.weight)})
	}
	if sum != TotalWeight {
		return steemtx.Authority{}, fmt.Errorf("multisig: weights sum to %d, not %d", sum, TotalWeight)
	}
	return auth.Sorted(), nil
}

// ExceedsTolerance reports whether the desired authority differs from the
// current one enough to warrant a validator-set proposal: membership changed,
// the threshold changed, or some weight moved by more than tolerance points.
func ExceedsTolerance(current, desired steemtx.Authority, tolerance uint16) bool {
	if current.WeightThreshold != desired.WeightThreshold || len(current.AccountAuths) != len(desired.AccountAuths) {
		return true
	}
	for _, d := range desired.AccountAuths {
		c := current.WeightOf(d.Name)
		if c == 0 {
			return true // a new member
		}
		diff := int(c) - int(d.Weight)
		if diff < 0 {
			diff = -diff
		}
		if diff > int(tolerance) {
			return true
		}
	}
	return false
}
