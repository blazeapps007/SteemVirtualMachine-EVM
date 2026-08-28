import { test } from "node:test";
import assert from "node:assert/strict";

import { CoinGeckoClient } from "../src/priceSources/coingecko";

function stubFetch(body: string, captureHeaders?: (h: Headers) => void) {
  const original = globalThis.fetch;
  globalThis.fetch = (async (_url: string, init?: RequestInit) => {
    if (captureHeaders) captureHeaders(new Headers(init?.headers));
    return new Response(body, { status: 200, headers: { "Content-Type": "application/json" } });
  }) as typeof fetch;
  return () => {
    globalThis.fetch = original;
  };
}

test("fetchUsdPrices preserves precision", async () => {
  // A price with many significant digits — regression test for the
  // float round-trip precision loss the regex-quote trick avoids.
  const restore = stubFetch('{"steem":{"usd":0.123456789012345678},"steem-dollars":{"usd":1.0}}');
  try {
    const c = new CoinGeckoClient("");
    const prices = await c.fetchUsdPrices(["STEEM", "SBD"]);
    assert.equal(prices.STEEM, "0.123456789012345678");
    assert.equal(prices.SBD, "1.000000000000000000");
  } finally {
    restore();
  }
});

test("missing id is skipped, not fatal", async () => {
  const restore = stubFetch('{"steem":{"usd":0.15}}');
  try {
    const c = new CoinGeckoClient("");
    const prices = await c.fetchUsdPrices(["STEEM", "SBD"]);
    assert.equal(prices.STEEM, "0.150000000000000000");
    assert.equal(prices.SBD, undefined);
  } finally {
    restore();
  }
});

test("demo key on the default host sends x-cg-demo-api-key", async () => {
  let seen: Headers | undefined;
  const restore = stubFetch("{}", (h) => (seen = h));
  try {
    const c = new CoinGeckoClient("k1");
    await c.fetchUsdPrices(["STEEM"]);
    assert.equal(seen?.get("x-cg-demo-api-key"), "k1");
    assert.equal(seen?.get("x-cg-pro-api-key"), null);
  } finally {
    restore();
  }
});

test("key on the pro host sends x-cg-pro-api-key", async () => {
  let seen: Headers | undefined;
  const restore = stubFetch("{}", (h) => (seen = h));
  try {
    const c = new CoinGeckoClient("k1", "https://pro-api.coingecko.com");
    await c.fetchUsdPrices(["STEEM"]);
    assert.equal(seen?.get("x-cg-pro-api-key"), "k1");
    assert.equal(seen?.get("x-cg-demo-api-key"), null);
  } finally {
    restore();
  }
});

test("no key sends no auth header", async () => {
  let seen: Headers | undefined;
  const restore = stubFetch("{}", (h) => (seen = h));
  try {
    const c = new CoinGeckoClient("");
    await c.fetchUsdPrices(["STEEM"]);
    assert.equal(seen?.get("x-cg-demo-api-key"), null);
    assert.equal(seen?.get("x-cg-pro-api-key"), null);
  } finally {
    restore();
  }
});
