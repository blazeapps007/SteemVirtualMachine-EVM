package relayer

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	oracledatatypes "steemvm/x/oracle/data/types"
)

// stubSource returns a fixed rate map (or an empty one to model "no prices").
type stubSource struct {
	rates map[string]math.LegacyDec
	err   error
}

func (s stubSource) FetchPrices([]string) (map[string]math.LegacyDec, error) {
	return s.rates, s.err
}

func dec(s string) math.LegacyDec { return math.LegacyMustNewDecFromStr(s) }

func TestBuildExchangeRatesString(t *testing.T) {
	// Pairs must come out sorted regardless of map order, so every honest feeder
	// produces the identical string (and thus commit hash). Price_Feed sorts
	// before the market pairs ('P' < 'S') — this test exercises that too.
	got := BuildExchangeRatesString(map[string]math.LegacyDec{
		"STEEM/USD_External": dec("0.25"),
		"SBD/USD_External":   dec("1.02"),
		"STEEM/SBD_Internal": dec("0.245098"),
		"Price_Feed":         dec("0.248"),
	})
	require.Equal(t, "Price_Feed:0.248000000000000000,SBD/USD_External:1.020000000000000000,STEEM/SBD_Internal:0.245098000000000000,STEEM/USD_External:0.250000000000000000", got)

	require.Equal(t, "", BuildExchangeRatesString(map[string]math.LegacyDec{}))
}

// TestFeederHashRoundTrip is the load-bearing test: the prevote's committed hash
// must be reproducible from the reveal, using the SAME derivation the chain uses
// (types.GetAggregateVoteHash) — otherwise every vote is rejected on-chain.
func TestFeederHashRoundTrip(t *testing.T) {
	const validator = "steemvaloper1abc"
	rates := map[string]math.LegacyDec{"STEEM/USD_External": dec("0.25"), "SBD/USD_External": dec("1.00")}
	exchangeRates := BuildExchangeRatesString(rates)
	salt, err := NewSalt()
	require.NoError(t, err)
	require.Len(t, salt, 32)

	prevote := BuildPrevoteMsg(validator, exchangeRates, salt)
	vote := BuildVoteMsg(validator, salt, exchangeRates)

	// The reveal reproduces the commit hash exactly.
	require.Equal(t, prevote.Hash, oracledatatypes.GetAggregateVoteHash(vote.Salt, vote.ExchangeRates, vote.Validator))
	require.Len(t, prevote.Hash, 40)

	// And the revealed string parses under the in-consensus parser.
	tuples, err := oracledatatypes.ParseExchangeRateTuples(vote.ExchangeRates)
	require.NoError(t, err)
	require.Len(t, tuples, 2)
}

func TestFeederStepCommitReveal(t *testing.T) {
	whitelist := []string{"STEEM/USD_External", "SBD/USD_External"}
	feeder := Feeder{
		Validator: "steemvaloper1abc",
		Source:    stubSource{rates: map[string]math.LegacyDec{"STEEM/USD_External": dec("0.25"), "SBD/USD_External": dec("1.00")}},
	}

	// Period 10: nothing to reveal (empty prior state), one fresh prevote.
	msgs, state, err := feeder.Step(10, whitelist, FeederState{})
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	_, isPrevote := msgs[0].(*oracledatatypes.MsgAggregateExchangeRatePrevote)
	require.True(t, isPrevote)
	require.Equal(t, uint64(10), state.PrevotePeriod)
	require.NotEmpty(t, state.Salt)

	// Period 11: reveal period 10's commit AND prevote period 11.
	msgs, state2, err := feeder.Step(11, whitelist, state)
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	vote, isVote := msgs[0].(*oracledatatypes.MsgAggregateExchangeRateVote)
	require.True(t, isVote, "reveal comes first")
	require.Equal(t, state.Salt, vote.Salt)
	require.Equal(t, state.ExchangeRates, vote.ExchangeRates)
	_, isPrevote = msgs[1].(*oracledatatypes.MsgAggregateExchangeRatePrevote)
	require.True(t, isPrevote)
	require.Equal(t, uint64(11), state2.PrevotePeriod)
}

func TestFeederStepStaleCommitNotRevealed(t *testing.T) {
	feeder := Feeder{Validator: "v", Source: stubSource{rates: map[string]math.LegacyDec{"STEEM/USD_External": dec("0.25")}}}
	// A commit from period 8 is stale at period 11 (missed the period-9 window):
	// only a fresh prevote is emitted, the stale commit is dropped.
	msgs, _, err := feeder.Step(11, []string{"STEEM/USD_External"}, FeederState{PrevotePeriod: 8, Salt: "x", ExchangeRates: "STEEM/USD_External:0.25"})
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	_, isPrevote := msgs[0].(*oracledatatypes.MsgAggregateExchangeRatePrevote)
	require.True(t, isPrevote)
}

func TestFeederStepIdleWithoutSource(t *testing.T) {
	// No source configured (the shipped default while sources are deferred): the
	// feeder still reveals a pending commit but never prevotes.
	feeder := Feeder{Validator: "v", Source: nil}
	prev := FeederState{PrevotePeriod: 4, Salt: "s", ExchangeRates: "STEEM/USD_External:0.25"}
	msgs, state, err := feeder.Step(5, []string{"STEEM/USD_External"}, prev)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	_, isVote := msgs[0].(*oracledatatypes.MsgAggregateExchangeRateVote)
	require.True(t, isVote)
	require.Zero(t, state.PrevotePeriod, "no new commit without a source")

	// A source that returns no prices also idles (a miss period).
	feeder.Source = stubSource{rates: map[string]math.LegacyDec{}}
	msgs, _, err = feeder.Step(10, []string{"STEEM/USD_External"}, FeederState{})
	require.NoError(t, err)
	require.Empty(t, msgs)
}
