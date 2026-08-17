// priceFeeder.ts — the commit-reveal price feed state machine, mirroring
// oracle/go/relayer/pricefeeder.go field-for-field. See oracle/PROTOCOL.md §7.

import { randomBytes } from "node:crypto";
import { sha256 } from "@noble/hashes/sha256";
import { bytesToHex } from "@noble/hashes/utils";

import { toLegacyDecString } from "./decimalFmt";
import type { FeederState } from "./state";
import {
  TYPE_URL_MSG_AGGREGATE_EXCHANGE_RATE_PREVOTE,
  TYPE_URL_MSG_AGGREGATE_EXCHANGE_RATE_VOTE,
  type EncodeObject,
} from "./broadcast";
import { CMCClient } from "./priceSources/cmc";
import { SteemPriceSource } from "./priceSources/steemPrices";

/** A price source prices whichever whitelisted pairs it can; pairs it
 * cannot price are simply omitted from the result (never throws for a
 * partial/individual failure) — an empty map means "nothing to vote this
 * period", which the unified slashing engine counts as a price-duty miss. */
export interface PriceSource {
  fetchPrices(pairs: string[]): Promise<Record<string, string>>;
}

/** Dispatches each whitelisted pair to the source that actually prices it:
 * CoinMarketCap for STEEM/USD and SBD/USD, the Steem RPC node for STEEM/SBD
 * and STEEM/FEED — see oracle/PROTOCOL.md §7's price source → pair mapping.
 * Mirrors oracle/go/relayer/pricesource.go's CompositePriceSource. */
export class CompositePriceSource implements PriceSource {
  constructor(
    private readonly cmc: CMCClient | undefined,
    private readonly steem: SteemPriceSource | undefined,
  ) {}

  async fetchPrices(pairs: string[]): Promise<Record<string, string>> {
    const want = new Set(pairs);
    const out: Record<string, string> = {};

    if (this.cmc && (want.has("STEEM/USD") || want.has("SBD/USD"))) {
      const symbols: string[] = [];
      if (want.has("STEEM/USD")) symbols.push("STEEM");
      if (want.has("SBD/USD")) symbols.push("SBD");
      try {
        const prices = await this.cmc.fetchUsdPrices(symbols);
        if (want.has("STEEM/USD") && prices.STEEM !== undefined) out["STEEM/USD"] = prices.STEEM;
        if (want.has("SBD/USD") && prices.SBD !== undefined) out["SBD/USD"] = prices.SBD;
      } catch {
        // omit both — one flaky upstream should never block Steem-sourced pairs
      }
    }

    if (this.steem) {
      const steemPrices = await this.steem.fetchPrices([...want].filter((p) => p === "STEEM/SBD" || p === "STEEM/FEED"));
      Object.assign(out, steemPrices);
    }

    return out;
  }
}

/** Deterministically derives the commit hash a validator puts in its
 * prevote: sha256(salt:exchangeRates:validator), hex-encoded and truncated
 * to the first 40 characters (20 bytes) — matches
 * x/oracle/data/types/vote.go's GetAggregateVoteHash exactly. */
export function getAggregateVoteHash(salt: string, exchangeRates: string, validator: string): string {
  const source = `${salt}:${exchangeRates}:${validator}`;
  const digest = sha256(new TextEncoder().encode(source));
  return bytesToHex(digest).slice(0, 40);
}

/** Renders a rate map as "PAIR:rate,PAIR:rate" with pairs sorted
 * lexicographically ascending, so two honest feeders that fetched the same
 * prices produce a BYTE-IDENTICAL string. Each rate is re-rendered through
 * toLegacyDecString to guarantee the canonical 18-fixed-decimal form
 * regardless of what precision the upstream source string carried. */
export function buildExchangeRatesString(rates: Record<string, string>): string {
  return Object.keys(rates)
    .sort()
    .map((pair) => `${pair}:${toLegacyDecString(rates[pair])}`)
    .join(",");
}

/** Builds the commit message for a period: carries only the truncated hash
 * of (salt, exchangeRates, validator). */
export function buildPrevoteMsg(validator: string, exchangeRates: string, salt: string): EncodeObject {
  return {
    typeUrl: TYPE_URL_MSG_AGGREGATE_EXCHANGE_RATE_PREVOTE,
    value: { validator, hash: getAggregateVoteHash(salt, exchangeRates, validator) },
  };
}

/** Builds the reveal message: the salt and exact exchange-rates string
 * committed to in the previous period's prevote. */
export function buildVoteMsg(validator: string, salt: string, exchangeRates: string): EncodeObject {
  return {
    typeUrl: TYPE_URL_MSG_AGGREGATE_EXCHANGE_RATE_VOTE,
    value: { validator, salt, exchangeRates },
  };
}

/** A fresh random 32-hex-character salt for a prevote commit. */
export function newSalt(): string {
  return bytesToHex(randomBytes(16));
}

const EMPTY_FEEDER_STATE: FeederState = { prevote_period: 0, salt: "", exchange_rates: "" };

/** Builds a validator's price prevote/reveal messages for one vote period.
 * Holds no chain/network handles — the caller supplies the current vote
 * period and whitelist (read from the node) and broadcasts the returned
 * messages. Mirrors oracle/go/relayer/pricefeeder.go's Feeder.Step. */
export class Feeder {
  constructor(
    /** The account bech32 address the messages are signed for (NOT the
     * valoper address — see PROTOCOL.md §1). */
    private readonly validator: string,
    /** Prices the pairs each period; undefined idles the feeder. */
    private readonly source: PriceSource | undefined,
  ) {}

  /**
   * Advances the feeder by one vote period. Returns the messages to
   * broadcast now and the state to persist for next period:
   *   - a REVEAL (vote) of the previous period's commit, but only when that
   *     commit was made in the immediately preceding period (the on-chain
   *     reveal window); a stale commit is silently dropped rather than
   *     revealed late.
   *   - a fresh PREVOTE committing this period's prices, when a source is
   *     configured and returns at least one whitelisted pair.
   *
   * When no source is set (or it returns no prices) only the reveal, if
   * any, is emitted and the returned state is empty.
   */
  async step(period: number, whitelist: string[], prev: FeederState): Promise<{ msgs: EncodeObject[]; state: FeederState }> {
    const msgs: EncodeObject[] = [];

    // Reveal the previous commit — only in the period right after it (the
    // reveal window). A wider gap means the window was missed (node
    // downtime, etc.) and the commit is abandoned, not revealed.
    if (prev.exchange_rates !== "" && prev.prevote_period + 1 === period) {
      msgs.push(buildVoteMsg(this.validator, prev.salt, prev.exchange_rates));
    }

    if (!this.source) {
      return { msgs, state: { ...EMPTY_FEEDER_STATE } };
    }

    let rates: Record<string, string>;
    try {
      rates = await this.source.fetchPrices(whitelist);
    } catch {
      // A price-fetch error still lets msgs (a pending reveal, if any)
      // through — only the fresh-prevote half of Step failed.
      return { msgs, state: { ...EMPTY_FEEDER_STATE } };
    }

    // Keep only whitelisted pairs — the chain rejects a vote carrying a
    // pair outside the gov whitelist.
    const allowed = new Set(whitelist);
    const filtered: Record<string, string> = {};
    for (const [pair, rate] of Object.entries(rates)) {
      if (!allowed.has(pair)) continue;
      try {
        // Reject negative/unparseable rates the same way the Go client's
        // !rate.IsNegative() check does.
        const scaled = toLegacyDecString(rate);
        if (scaled.startsWith("-")) continue;
        filtered[pair] = rate;
      } catch {
        continue;
      }
    }
    if (Object.keys(filtered).length === 0) {
      return { msgs, state: { ...EMPTY_FEEDER_STATE } };
    }

    const salt = newSalt();
    const exchangeRates = buildExchangeRatesString(filtered);
    msgs.push(buildPrevoteMsg(this.validator, exchangeRates, salt));
    return { msgs, state: { prevote_period: period, salt, exchange_rates: exchangeRates } };
  }
}
