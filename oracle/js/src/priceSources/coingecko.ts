// priceSources/coingecko.ts — a minimal CoinGecko client, used only to price
// STEEM/USD_External and SBD/USD_External — an alternative to CMCClient,
// selected via ORACLE_PRICE_SOURCE. Mirrors oracle/go/relayer/coingecko.go's
// CoinGeckoClient. Unlike CMC, apiKey is optional: CoinGecko's public
// /simple/price endpoint works keyless (at the public rate limit); a demo or
// pro key just raises it.

import { toLegacyDecString } from "../decimalFmt";

// The only place the STEEM/SBD -> CoinGecko coin-id translation happens, so
// callers only ever deal in "STEEM"/"SBD", exactly like CMCClient.
const COIN_IDS: Record<string, string> = { STEEM: "steem", SBD: "steem-dollars" };

export class CoinGeckoClient {
  private readonly apiKey: string;
  private readonly baseUrl: string;

  constructor(apiKey: string, baseUrl?: string) {
    this.apiKey = apiKey;
    this.baseUrl = (baseUrl || "https://api.coingecko.com").replace(/\/+$/, "");
  }

  /** Returns the latest USD price (as a LegacyDec.String()-formatted
   * string) for each of the given tickers, batched into a single API call.
   * Tickers this client doesn't have a CoinGecko id for, or that CoinGecko
   * doesn't return a USD price for, are simply absent from the result. */
  async fetchUsdPrices(symbols: string[]): Promise<Record<string, string>> {
    const ids = symbols.map((sym) => COIN_IDS[sym]).filter((id): id is string => Boolean(id));
    if (ids.length === 0) {
      return {};
    }
    const url = `${this.baseUrl}/api/v3/simple/price?ids=${encodeURIComponent(ids.join(","))}&vs_currencies=usd`;
    const headers: Record<string, string> = { Accept: "application/json" };
    if (this.apiKey) {
      const keyHeader = this.baseUrl.includes("pro-api.coingecko.com") ? "x-cg-pro-api-key" : "x-cg-demo-api-key";
      headers[keyHeader] = this.apiKey;
    }
    const resp = await fetch(url, { headers });
    if (!resp.ok) {
      throw new Error(`coingecko: unexpected status ${resp.status}`);
    }

    // CoinGecko emits "usd" as a raw JSON number literal — same
    // precision-loss risk as CMC's "price" field, same fix: quote the
    // literal before JSON.parse so toLegacyDecString sees the exact source
    // digits. See cmc.ts's identical trick.
    const raw = await resp.text();
    const patched = raw.replace(/"usd"\s*:\s*(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)/g, (_m, num: string) => `"usd":${JSON.stringify(num)}`);
    const parsed = JSON.parse(patched) as Record<string, { usd?: string } | undefined>;

    const out: Record<string, string> = {};
    for (const sym of symbols) {
      const id = COIN_IDS[sym];
      if (!id) continue;
      const usd = parsed[id]?.usd;
      if (usd === undefined) continue;
      try {
        out[sym] = toLegacyDecString(usd);
      } catch {
        continue; // malformed price from upstream: skip rather than fail the batch
      }
    }
    return out;
  }
}
