// priceSources/cmc.ts — a minimal CoinMarketCap client, used only to price
// STEEM/USD and SBD/USD. Mirrors oracle/go/relayer/pricesource.go's
// CMCClient. Configured via ORACLE_CMC_API_KEY / ORACLE_CMC_BASE_URL.

import { toLegacyDecString } from "../decimalFmt";

export class CMCClient {
  private readonly apiKey: string;
  private readonly baseUrl: string;

  constructor(apiKey: string, baseUrl?: string) {
    this.apiKey = apiKey;
    this.baseUrl = (baseUrl || "https://pro-api.coinmarketcap.com").replace(/\/+$/, "");
  }

  /** Returns the latest USD price (as a LegacyDec.String()-formatted
   * string) for each of the given CoinMarketCap symbols, batched into a
   * single API call. Symbols CoinMarketCap doesn't return are simply absent
   * from the result. */
  async fetchUsdPrices(symbols: string[]): Promise<Record<string, string>> {
    if (symbols.length === 0) {
      return {};
    }
    const url = `${this.baseUrl}/v2/cryptocurrency/quotes/latest?symbol=${encodeURIComponent(symbols.join(","))}&convert=USD`;
    const resp = await fetch(url, {
      headers: { "X-CMC_PRO_API_KEY": this.apiKey, Accept: "application/json" },
    });
    if (!resp.ok) {
      throw new Error(`coinmarketcap: unexpected status ${resp.status}`);
    }

    // CoinMarketCap emits "price" as a raw JSON number literal. A plain
    // JSON.parse would round-trip it through float64 before we ever see it,
    // which can lose precision for prices with many significant digits —
    // the same failure mode toLegacyDecString's own doc comment warns
    // against. So the raw response text is patched to quote every "price"
    // number literal (preserving its exact source digits) BEFORE parsing;
    // json.Number in the Go client's encoding/json.Decoder achieves the
    // same "keep the original text" effect (see pricesource.go).
    const raw = await resp.text();
    const patched = raw.replace(
      /"price"\s*:\s*(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)/g,
      (_m, num: string) => `"price":${JSON.stringify(num)}`,
    );
    const parsed = JSON.parse(patched) as {
      data?: Record<string, Array<{ symbol: string; quote: Record<string, { price: string }> }>>;
    };

    const out: Record<string, string> = {};
    for (const sym of symbols) {
      const entries = parsed.data?.[sym];
      if (!entries || entries.length === 0) continue;
      const usd = entries[0].quote?.USD;
      if (!usd) continue;
      try {
        out[sym] = toLegacyDecString(usd.price);
      } catch {
        continue; // malformed price from upstream (e.g. exponent notation): skip rather than fail the batch
      }
    }
    return out;
  }
}
