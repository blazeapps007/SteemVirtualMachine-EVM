package relayer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cosmossdk.io/math"
)

// coinGeckoIDs maps this chain's bridgeable-asset tickers to CoinGecko's own
// coin ids — the only place that translation happens, so callers (and
// CompositePriceSource) only ever deal in "STEEM"/"SBD", exactly like
// CMCClient.
var coinGeckoIDs = map[string]string{
	"STEEM": "steem",
	"SBD":   "steem-dollars",
}

// CoinGeckoClient is a minimal CoinGecko client, used only to price
// STEEM/USD_External and SBD/USD_External — an alternative to CMCClient,
// selected via ORACLE_PRICE_SOURCE (see oracle/.env.example). Unlike CMC,
// apiKey is optional: CoinGecko's public /simple/price endpoint works
// keyless (at the public rate limit); a demo or pro key just raises it.
type CoinGeckoClient struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// NewCoinGeckoClient builds a client. baseURL defaults to CoinGecko's public
// API; set it to https://pro-api.coingecko.com for a pro-tier key (this also
// switches which auth header FetchUSDPrices sends).
func NewCoinGeckoClient(apiKey, baseURL string) *CoinGeckoClient {
	if baseURL == "" {
		baseURL = "https://api.coingecko.com"
	}
	return &CoinGeckoClient{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// FetchUSDPrices returns the latest USD price for each of the given tickers
// (e.g. "STEEM", "SBD"), batched into a single API call. Tickers this client
// doesn't have a CoinGecko id for, or that CoinGecko doesn't return a USD
// price for, are simply absent from the result.
func (c *CoinGeckoClient) FetchUSDPrices(symbols []string) (map[string]math.LegacyDec, error) {
	var ids []string
	for _, sym := range symbols {
		if id, ok := coinGeckoIDs[sym]; ok {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	url := fmt.Sprintf("%s/api/v3/simple/price?ids=%s&vs_currencies=usd",
		c.baseURL, strings.Join(ids, ","))

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		if strings.Contains(c.baseURL, "pro-api.coingecko.com") {
			req.Header.Set("x-cg-pro-api-key", c.apiKey)
		} else {
			req.Header.Set("x-cg-demo-api-key", c.apiKey)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("coingecko: unexpected status %d", resp.StatusCode)
	}

	// json.Number preserves CoinGecko's exact textual price representation,
	// avoiding a float64 round-trip before it becomes a math.LegacyDec.
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	var parsed map[string]map[string]json.Number
	if err := dec.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("coingecko: invalid response: %w", err)
	}

	out := make(map[string]math.LegacyDec, len(symbols))
	for _, sym := range symbols {
		id, ok := coinGeckoIDs[sym]
		if !ok {
			continue
		}
		usd, ok := parsed[id]["usd"]
		if !ok {
			continue
		}
		price, err := math.LegacyNewDecFromStr(usd.String())
		if err != nil {
			continue // malformed price from upstream: skip rather than fail the batch
		}
		out[sym] = price
	}
	return out, nil
}
