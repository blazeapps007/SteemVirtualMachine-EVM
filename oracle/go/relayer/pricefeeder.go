package relayer

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"strings"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	oracledatatypes "steemvm/x/oracle/data/types"
)

// PriceSource fetches current reference prices for the whitelisted pairs. The
// CONCRETE sources (exchange APIs / the Steem-witness internal feed) are
// DEFERRED for v0.0.3 (plan §5: ".env.example gains source endpoints
// (deferred)"). Until one is wired, a feeder is constructed with a nil Source
// and simply idles — so shipping the machinery now is safe and self-activating:
// once a Source is configured on a v0.0.3-live chain, the feeder starts voting.
type PriceSource interface {
	// FetchPrices returns a rate per pair it can price. Pairs it cannot price are
	// omitted; an empty map means "nothing to vote this period" and the feeder
	// skips its prevote (a no-vote period, which the unified slashing engine
	// counts as a price-duty miss — see plan §2/§2b).
	FetchPrices(pairs []string) (map[string]math.LegacyDec, error)
}

// FeederState is the cross-cycle memory a feeder needs to REVEAL what it
// previously COMMITTED. The on-chain flow is commit-reveal: a prevote in period
// N commits sha256(salt:exchangeRates:validator); the matching vote in period
// N+1 reveals the salt + exact exchange-rates string, and the chain recomputes
// the hash to verify. So the salt and the byte-exact string must survive from
// the prevote to the following period's reveal.
type FeederState struct {
	PrevotePeriod uint64 `json:"prevote_period"`
	Salt          string `json:"salt"`
	ExchangeRates string `json:"exchange_rates"`
}

// Feeder builds a validator's price prevote/reveal messages. It holds no chain
// or network handles — the caller supplies the current vote period and
// whitelist (read from the node) and broadcasts the returned messages, exactly
// as the deposit relayer's BuildMsg is broadcast by the loop.
type Feeder struct {
	// Validator is the operator address string the messages are signed for.
	Validator string
	// Source prices the pairs each period; nil idles the feeder (sources deferred).
	Source PriceSource
}

// Step advances the feeder by one vote period. It returns the messages to
// broadcast now and the state to persist for next period:
//   - a REVEAL (vote) of the previous period's commit, but only when that commit
//     was made in the immediately preceding period (the on-chain reveal window);
//     a stale commit is silently dropped rather than revealed late.
//   - a fresh PREVOTE committing this period's prices, when a Source is
//     configured and returns at least one whitelisted pair. The returned
//     FeederState carries that commit's salt + string so next period can reveal.
//
// When no Source is set (or it returns no prices) only the reveal, if any, is
// emitted and the returned state is empty — nothing to reveal next period.
func (f Feeder) Step(period uint64, whitelist []string, prev FeederState) ([]sdk.Msg, FeederState, error) {
	var msgs []sdk.Msg

	// Reveal the previous commit — only in the period right after it (the reveal
	// window). prev.PrevotePeriod+1 == period; a wider gap means we missed the
	// window (node downtime, etc.) and the commit is abandoned, not revealed.
	if prev.ExchangeRates != "" && prev.PrevotePeriod+1 == period {
		msgs = append(msgs, BuildVoteMsg(f.Validator, prev.Salt, prev.ExchangeRates))
	}

	if f.Source == nil {
		return msgs, FeederState{}, nil
	}
	rates, err := f.Source.FetchPrices(whitelist)
	if err != nil {
		return msgs, FeederState{}, err
	}

	// Keep only whitelisted pairs — the chain rejects a vote carrying a pair
	// outside the gov whitelist, so an over-eager source can't invalidate the vote.
	allowed := make(map[string]bool, len(whitelist))
	for _, p := range whitelist {
		allowed[p] = true
	}
	filtered := make(map[string]math.LegacyDec, len(rates))
	for pair, rate := range rates {
		if allowed[pair] && !rate.IsNil() && !rate.IsNegative() {
			filtered[pair] = rate
		}
	}
	if len(filtered) == 0 {
		return msgs, FeederState{}, nil
	}

	salt, err := NewSalt()
	if err != nil {
		return msgs, FeederState{}, err
	}
	exchangeRates := BuildExchangeRatesString(filtered)
	msgs = append(msgs, BuildPrevoteMsg(f.Validator, exchangeRates, salt))
	return msgs, FeederState{PrevotePeriod: period, Salt: salt, ExchangeRates: exchangeRates}, nil
}

// BuildExchangeRatesString renders a rate map as "PAIR:rate,PAIR:rate" with
// pairs sorted, so two honest feeders that fetched the same prices produce a
// BYTE-IDENTICAL string — and therefore the same commit hash and a vote the
// chain's ParseExchangeRateTuples accepts. Each rate uses LegacyDec.String()
// (the canonical decimal form the on-chain parser round-trips).
func BuildExchangeRatesString(rates map[string]math.LegacyDec) string {
	pairs := make([]string, 0, len(rates))
	for pair := range rates {
		pairs = append(pairs, pair)
	}
	sort.Strings(pairs)

	parts := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		parts = append(parts, pair+":"+rates[pair].String())
	}
	return strings.Join(parts, ",")
}

// BuildPrevoteMsg builds the commit message for a period: it carries only the
// truncated hash of (salt, exchangeRates, validator), computed by the SAME
// on-chain helper the reveal is verified against.
func BuildPrevoteMsg(validator, exchangeRates, salt string) *oracledatatypes.MsgAggregateExchangeRatePrevote {
	return &oracledatatypes.MsgAggregateExchangeRatePrevote{
		Validator: validator,
		Hash:      oracledatatypes.GetAggregateVoteHash(salt, exchangeRates, validator),
	}
}

// BuildVoteMsg builds the reveal message: the salt and exact exchange-rates
// string committed to in the previous period's prevote.
func BuildVoteMsg(validator, salt, exchangeRates string) *oracledatatypes.MsgAggregateExchangeRateVote {
	return &oracledatatypes.MsgAggregateExchangeRateVote{
		Validator:     validator,
		Salt:          salt,
		ExchangeRates: exchangeRates,
	}
}

// NewSalt returns a fresh random 32-hex-character salt for a prevote commit.
// The salt hides the committed prices until reveal, so a validator cannot copy
// another's vote from the mempool.
func NewSalt() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
