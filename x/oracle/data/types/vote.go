package types

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
)

// aggregateVoteHashLen is the hex length of the truncated commit hash (20 bytes),
// matching the Terra-style aggregate vote hash.
const aggregateVoteHashLen = 40

// GetAggregateVoteHash deterministically derives the commit hash a validator
// puts in its prevote. The reveal (salt + exchange_rates + validator) must
// reproduce this exact string. Keep this in sync with the off-chain feeder.
func GetAggregateVoteHash(salt, exchangeRates, validator string) string {
	sourceStr := fmt.Sprintf("%s:%s:%s", salt, exchangeRates, validator)
	h := sha256.Sum256([]byte(sourceStr))
	return hex.EncodeToString(h[:])[:aggregateVoteHashLen]
}

// ParseExchangeRateTuples parses a "PAIR:rate,PAIR:rate" string into tuples.
// A pair itself may contain a "/" (e.g. "STEEM/USD_External") but never a ":", so each
// entry is split on its final ":" — everything before is the pair, everything
// after is the decimal rate. An empty input yields an empty slice.
func ParseExchangeRateTuples(exchangeRates string) ([]ExchangeRateTuple, error) {
	exchangeRates = strings.TrimSpace(exchangeRates)
	if exchangeRates == "" {
		return []ExchangeRateTuple{}, nil
	}

	entries := strings.Split(exchangeRates, ",")
	tuples := make([]ExchangeRateTuple, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		idx := strings.LastIndex(entry, ":")
		if idx <= 0 || idx == len(entry)-1 {
			return nil, errorsmod.Wrapf(ErrInvalidExchangeRate, "malformed entry %q", entry)
		}
		pair := strings.TrimSpace(entry[:idx])
		rateStr := strings.TrimSpace(entry[idx+1:])
		if pair == "" {
			return nil, errorsmod.Wrapf(ErrInvalidExchangeRate, "empty pair in entry %q", entry)
		}
		if seen[pair] {
			return nil, errorsmod.Wrapf(ErrInvalidExchangeRate, "duplicate pair %q", pair)
		}
		rate, err := math.LegacyNewDecFromStr(rateStr)
		if err != nil {
			return nil, errorsmod.Wrapf(ErrInvalidExchangeRate, "invalid rate %q for pair %q: %s", rateStr, pair, err)
		}
		if rate.IsNegative() {
			return nil, errorsmod.Wrapf(ErrInvalidExchangeRate, "negative rate for pair %q", pair)
		}
		seen[pair] = true
		tuples = append(tuples, ExchangeRateTuple{Pair: pair, Rate: rate})
	}
	return tuples, nil
}
