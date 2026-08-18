package relayer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cosmossdk.io/math"
)

// CompositePriceSource implements PriceSource by dispatching each whitelisted
// pair to the source that actually prices it: CoinMarketCap for
// STEEM/USD_External and SBD/USD_External (Steem has no native USD market),
// and the SAME Steem RPC node used for bridge scanning for
// STEEM/SBD_Internal (internal market) and Price_Feed (Steem's own
// witness-median feed price — deliberately not pair-shaped, since it isn't a
// tradeable market rate) — see oracle/PROTOCOL.md §7 for the exact mapping.
//
// FetchPrices never returns an error: a source being nil (not configured) or
// failing for a given pair just omits that pair from the result, matching
// Feeder.Step's existing "partial/empty map is fine" contract — one flaky
// upstream (say, CoinMarketCap rate-limiting) should never block the
// Steem-sourced pairs, and a whole-cycle miss is already meaningful on its
// own (the unified slashing engine counts it as a price-duty miss) without
// needing a distinct error path.
type CompositePriceSource struct {
	CMC   *CMCClient   // nil disables STEEM/USD_External and SBD/USD_External
	Steem *SteemClient // nil disables STEEM/SBD_Internal and Price_Feed
}

func (s CompositePriceSource) FetchPrices(pairs []string) (map[string]math.LegacyDec, error) {
	want := make(map[string]bool, len(pairs))
	for _, p := range pairs {
		want[p] = true
	}
	out := make(map[string]math.LegacyDec, len(pairs))

	if s.CMC != nil && (want["STEEM/USD_External"] || want["SBD/USD_External"]) {
		var symbols []string
		if want["STEEM/USD_External"] {
			symbols = append(symbols, "STEEM")
		}
		if want["SBD/USD_External"] {
			symbols = append(symbols, "SBD")
		}
		if prices, err := s.CMC.FetchUSDPrices(symbols); err == nil {
			if p, ok := prices["STEEM"]; ok && want["STEEM/USD_External"] {
				out["STEEM/USD_External"] = p
			}
			if p, ok := prices["SBD"]; ok && want["SBD/USD_External"] {
				out["SBD/USD_External"] = p
			}
		}
	}

	if s.Steem != nil && want["STEEM/SBD_Internal"] {
		if p, err := s.Steem.GetTicker(); err == nil {
			out["STEEM/SBD_Internal"] = p
		}
	}
	if s.Steem != nil && want["Price_Feed"] {
		if p, err := s.Steem.GetFeedHistory(); err == nil {
			out["Price_Feed"] = p
		}
	}

	return out, nil
}

// CMCClient is a minimal CoinMarketCap client, used only to price
// STEEM/USD_External and SBD/USD_External. Configured via
// ORACLE_CMC_API_KEY / ORACLE_CMC_BASE_URL — see
// oracle/.env.example. A nil/empty apiKey means "not configured"; callers
// should not construct a CMCClient in that case (see main.go).
type CMCClient struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// NewCMCClient builds a client. baseURL defaults to CoinMarketCap's
// production API; override for a sandbox key during testing.
func NewCMCClient(apiKey, baseURL string) *CMCClient {
	if baseURL == "" {
		baseURL = "https://pro-api.coinmarketcap.com"
	}
	return &CMCClient{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// FetchUSDPrices returns the latest USD price for each of the given
// CoinMarketCap symbols (e.g. "STEEM", "SBD"), batched into a single API
// call. Symbols CoinMarketCap doesn't return (unknown symbol, no USD quote)
// are simply absent from the result.
func (c *CMCClient) FetchUSDPrices(symbols []string) (map[string]math.LegacyDec, error) {
	if len(symbols) == 0 {
		return nil, nil
	}
	url := fmt.Sprintf("%s/v2/cryptocurrency/quotes/latest?symbol=%s&convert=USD",
		c.baseURL, strings.Join(symbols, ","))

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-CMC_PRO_API_KEY", c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("coinmarketcap: unexpected status %d", resp.StatusCode)
	}

	// json.Number preserves CoinMarketCap's exact textual price representation,
	// avoiding a float64 round-trip before it becomes a math.LegacyDec.
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	var parsed struct {
		Data map[string][]struct {
			Symbol string `json:"symbol"`
			Quote  map[string]struct {
				Price json.Number `json:"price"`
			} `json:"quote"`
		} `json:"data"`
	}
	if err := dec.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("coinmarketcap: invalid response: %w", err)
	}

	out := make(map[string]math.LegacyDec, len(symbols))
	for _, sym := range symbols {
		entries, ok := parsed.Data[sym]
		if !ok || len(entries) == 0 {
			continue
		}
		usd, ok := entries[0].Quote["USD"]
		if !ok {
			continue
		}
		price, err := math.LegacyNewDecFromStr(usd.Price.String())
		if err != nil {
			continue // malformed price from upstream: skip rather than fail the batch
		}
		out[sym] = price
	}
	return out, nil
}
