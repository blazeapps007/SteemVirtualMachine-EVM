package relayer

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"
)

func TestCoinGeckoClient_FetchUSDPrices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v3/simple/price", r.URL.Path)
		require.Equal(t, "steem,steem-dollars", r.URL.Query().Get("ids"))
		require.Equal(t, "usd", r.URL.Query().Get("vs_currencies"))
		// A price with many significant digits — regression test for the
		// float64-round-trip precision loss json.Number/UseNumber avoids.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"steem":{"usd":0.123456789012345678},"steem-dollars":{"usd":1.0}}`))
	}))
	defer srv.Close()

	c := NewCoinGeckoClient("", srv.URL)
	prices, err := c.FetchUSDPrices([]string{"STEEM", "SBD"})
	require.NoError(t, err)
	require.True(t, dec("0.123456789012345678").Equal(prices["STEEM"]))
	require.True(t, dec("1.0").Equal(prices["SBD"]))
}

func TestCoinGeckoClient_MissingIDSkipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"steem":{"usd":0.15}}`)) // no steem-dollars entry
	}))
	defer srv.Close()

	c := NewCoinGeckoClient("", srv.URL)
	prices, err := c.FetchUSDPrices([]string{"STEEM", "SBD"})
	require.NoError(t, err)
	require.Contains(t, prices, "STEEM")
	require.NotContains(t, prices, "SBD", "a missing id must be skipped, not fatal to the batch")
}

func TestCoinGeckoClient_AuthHeaderSelection(t *testing.T) {
	var gotHeader, gotValue string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := r.Header.Get("x-cg-demo-api-key"); v != "" {
			gotHeader, gotValue = "x-cg-demo-api-key", v
		}
		if v := r.Header.Get("x-cg-pro-api-key"); v != "" {
			gotHeader, gotValue = "x-cg-pro-api-key", v
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	// Default (non-pro) base URL + a key -> demo header.
	_, err := NewCoinGeckoClient("k1", srv.URL).FetchUSDPrices([]string{"STEEM"})
	require.NoError(t, err)
	require.Equal(t, "x-cg-demo-api-key", gotHeader)
	require.Equal(t, "k1", gotValue)
}

// stubExternalPriceClient lets a CompositePriceSource dispatch test stand in
// for either CMCClient or CoinGeckoClient, confirming the field rename from
// CMC to External didn't break routing.
type stubExternalPriceClient struct {
	prices map[string]math.LegacyDec
}

func (s stubExternalPriceClient) FetchUSDPrices(symbols []string) (map[string]math.LegacyDec, error) {
	out := make(map[string]math.LegacyDec)
	for _, sym := range symbols {
		if p, ok := s.prices[sym]; ok {
			out[sym] = p
		}
	}
	return out, nil
}

func TestCompositePriceSource_ExternalDispatch(t *testing.T) {
	src := CompositePriceSource{
		External: stubExternalPriceClient{prices: map[string]math.LegacyDec{
			"STEEM": dec("0.25"),
			"SBD":   dec("1.0"),
		}},
	}
	out, err := src.FetchPrices([]string{"STEEM/USD_External", "SBD/USD_External"})
	require.NoError(t, err)
	require.True(t, dec("0.25").Equal(out["STEEM/USD_External"]))
	require.True(t, dec("1.0").Equal(out["SBD/USD_External"]))
}
