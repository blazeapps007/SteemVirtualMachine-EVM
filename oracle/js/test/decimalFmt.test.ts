// decimalFmt.test.ts — unit tests for the LegacyDec.String()-compatible
// formatter against oracle/PROTOCOL.md §7's worked examples plus edge cases
// ported from cosmos-sdk's own LegacyNewDecFromStr/LegacyDec.String() tests.
// This is the single highest-risk piece of the whole protocol (see
// decimalFmt.ts's module doc comment) so it gets its own thorough suite,
// independent of any live chain.

import { test } from "node:test";
import assert from "node:assert/strict";

import { parseLegacyDec, formatLegacyDec, toLegacyDecString, LEGACY_PRECISION } from "../src/decimalFmt";

test("LEGACY_PRECISION is 18", () => {
  assert.equal(LEGACY_PRECISION, 18);
});

test("toLegacyDecString matches PROTOCOL.md §7 worked examples", () => {
  assert.equal(toLegacyDecString("1.23"), "1.230000000000000000");
  assert.equal(toLegacyDecString("0.5"), "0.500000000000000000");
});

test("toLegacyDecString: whole numbers get a full 18-decimal tail", () => {
  assert.equal(toLegacyDecString("0"), "0.000000000000000000");
  assert.equal(toLegacyDecString("42"), "42.000000000000000000");
  assert.equal(toLegacyDecString("1"), "1.000000000000000000");
});

test("toLegacyDecString: negative values", () => {
  assert.equal(toLegacyDecString("-1.23"), "-1.230000000000000000");
  assert.equal(toLegacyDecString("-0.5"), "-0.500000000000000000");
});

test("toLegacyDecString: already-18-decimal input is a no-op", () => {
  assert.equal(toLegacyDecString("1.230000000000000000"), "1.230000000000000000");
});

test("toLegacyDecString: fewer than 18 decimals gets zero-padded, not trimmed", () => {
  assert.equal(toLegacyDecString("0.1"), "0.100000000000000000");
  assert.equal(toLegacyDecString("0.123456"), "0.123456000000000000");
});

test("toLegacyDecString: exactly 18 fractional digits is accepted", () => {
  assert.equal(toLegacyDecString("0.123456789012345678"), "0.123456789012345678");
});

test("parseLegacyDec: more than 18 fractional digits throws (not rounds)", () => {
  assert.throws(() => parseLegacyDec("0.1234567890123456789"), /too many fractional digits/);
});

test("parseLegacyDec: empty string throws", () => {
  assert.throws(() => parseLegacyDec(""), /empty decimal string/);
});

test("parseLegacyDec: bare minus sign throws", () => {
  assert.throws(() => parseLegacyDec("-"), /empty decimal string/);
});

test("parseLegacyDec: leading-dot and trailing-dot are rejected (Go SplitN parity)", () => {
  assert.throws(() => parseLegacyDec(".5"), /invalid decimal length/);
  assert.throws(() => parseLegacyDec("5."), /invalid decimal length/);
});

test("parseLegacyDec: non-digit characters are rejected", () => {
  assert.throws(() => parseLegacyDec("1.2a"));
  assert.throws(() => parseLegacyDec("1a.2"));
  assert.throws(() => parseLegacyDec("1e5")); // no scientific notation support
});

test("parseLegacyDec: double decimal point is rejected", () => {
  assert.throws(() => parseLegacyDec("1.2.3"));
});

test("formatLegacyDec: large integer part splices the decimal point correctly", () => {
  // 123456789012345678901n has 21 digits; last 18 are fractional.
  assert.equal(formatLegacyDec(123456789012345678901n), "123.456789012345678901");
});

test("formatLegacyDec: zero", () => {
  assert.equal(formatLegacyDec(0n), "0.000000000000000000");
});

test("formatLegacyDec: smallest possible unit (1) and its negative", () => {
  assert.equal(formatLegacyDec(1n), "0.000000000000000001");
  assert.equal(formatLegacyDec(-1n), "-0.000000000000000001");
});

test("parseLegacyDec/formatLegacyDec round-trip for a range of values", () => {
  const cases = ["0", "1", "1.23", "0.5", "0.000000000000000001", "123456.789", "99999999999.999999999999999999"];
  for (const c of cases) {
    assert.equal(formatLegacyDec(parseLegacyDec(c)), toLegacyDecString(c));
  }
});

test("buildExchangeRatesString-style rendering: rate re-formatting is idempotent", () => {
  // A price source might hand back a string with fewer decimals (e.g. a
  // CoinMarketCap price "0.523"); re-running toLegacyDecString on the
  // ALREADY-canonical output must be a no-op (idempotent), since
  // priceFeeder.ts's Feeder.step re-renders filtered rates before hashing.
  const once = toLegacyDecString("0.523");
  const twice = toLegacyDecString(once);
  assert.equal(once, twice);
});
