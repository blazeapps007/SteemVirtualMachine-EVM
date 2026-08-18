// priceSources/steemPrices.ts — prices STEEM/SBD_Internal (internal market)
// and Price_Feed (Steem's own witness-median feed price) from the same
// Steem RPC node used for bridge scanning. Mirrors the Steem-sourced half of
// oracle/go/relayer/pricesource.go's CompositePriceSource dispatch (see
// oracle/PROTOCOL.md §7's price source → pair mapping table). Price_Feed is
// deliberately NOT pair-shaped — it's a single blockchain-native value, not
// a tradeable market rate.

import type { SteemClient } from "../steemClient";

export class SteemPriceSource {
  constructor(private readonly steem: SteemClient) {}

  /** Prices whichever of STEEM/SBD_Internal / Price_Feed are in `pairs`.
   * Never throws: a fetch failure for one pair just omits it from the
   * result, matching the "partial/empty map is fine" contract every
   * PriceSource shares (see priceFeeder.ts). */
  async fetchPrices(pairs: string[]): Promise<Record<string, string>> {
    const want = new Set(pairs);
    const out: Record<string, string> = {};

    if (want.has("STEEM/SBD_Internal")) {
      try {
        out["STEEM/SBD_Internal"] = await this.steem.getTicker();
      } catch {
        // omit — see doc comment above
      }
    }
    if (want.has("Price_Feed")) {
      try {
        out["Price_Feed"] = await this.steem.getFeedHistory();
      } catch {
        // omit — see doc comment above
      }
    }
    return out;
  }
}
