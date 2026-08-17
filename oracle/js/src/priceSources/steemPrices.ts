// priceSources/steemPrices.ts — prices STEEM/SBD (internal market) and
// STEEM/FEED (witness feed) from the same Steem RPC node used for bridge
// scanning. Mirrors the Steem-sourced half of oracle/go/relayer/pricesource.go's
// CompositePriceSource dispatch (see oracle/PROTOCOL.md §7's price source →
// pair mapping table).

import type { SteemClient } from "../steemClient";

export class SteemPriceSource {
  constructor(private readonly steem: SteemClient) {}

  /** Prices whichever of STEEM/SBD / STEEM/FEED are in `pairs`. Never
   * throws: a fetch failure for one pair just omits it from the result,
   * matching the "partial/empty map is fine" contract every PriceSource
   * shares (see priceFeeder.ts). */
  async fetchPrices(pairs: string[]): Promise<Record<string, string>> {
    const want = new Set(pairs);
    const out: Record<string, string> = {};

    if (want.has("STEEM/SBD")) {
      try {
        out["STEEM/SBD"] = await this.steem.getTicker();
      } catch {
        // omit — see doc comment above
      }
    }
    if (want.has("STEEM/FEED")) {
      try {
        out["STEEM/FEED"] = await this.steem.getFeedHistory();
      } catch {
        // omit — see doc comment above
      }
    }
    return out;
  }
}
