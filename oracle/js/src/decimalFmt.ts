// decimalFmt.ts — a byte-exact reimplementation of Cosmos SDK's
// `cosmossdk.io/math.LegacyDec` decimal string parsing/formatting
// (`LegacyNewDecFromStr` / `LegacyDec.String()`), ported directly from the
// Go source (`cosmossdk.io/math/dec.go`). See oracle/PROTOCOL.md §7: this is
// the single highest-risk piece of the whole protocol — the on-chain
// `ParseExchangeRateTuples` round-trips through this EXACT format (18 fixed
// decimal places, trailing zeros never trimmed), and a mismatched string
// either fails to parse outright or — worse — silently hashes to a different
// commit than what's later revealed, since `BuildExchangeRatesString`
// requires the string used for the prevote hash and the string in the reveal
// to be byte-identical.
//
// A plain JS "format as decimal" call (Number.toString, toFixed, etc.) does
// NOT reproduce this: floating point can't exactly represent most decimal
// fractions, and none of the standard formatters keep 18 fixed places
// without trimming. Everything here works on strings/bigint only — the raw
// decimal text is parsed digit-by-digit into a bigint scaled by 10^18
// (never routed through `Number`), exactly mirroring the Go implementation's
// string-splice-based approach.

/** LegacyDec's fixed decimal precision: 18 digits after the point. */
export const LEGACY_PRECISION = 18;

const TEN_POW_PRECISION = 10n ** BigInt(LEGACY_PRECISION);

/**
 * Parses a plain decimal string (e.g. "1.23", "-0.5", "42") into the bigint
 * that `math.LegacyDec` stores internally — the decimal value scaled by
 * 10^18. Mirrors `LegacyNewDecFromStr` field-for-field, including its
 * rejection rules (this is a strict parser, not a lenient one):
 *
 *   - empty string, or bare "-": invalid.
 *   - a decimal point with an empty integer part (".5") or empty
 *     fractional part ("5."): invalid — Go's SplitN-based parse requires
 *     digits on both sides of "." whenever "." is present at all.
 *   - more than 18 fractional digits: invalid (Go errors here rather than
 *     rounding — same here, so a >18-decimal upstream price source is
 *     skipped exactly like the Go client skips it via a caught error).
 *   - any non-digit character in the integer or fractional part: invalid.
 *   - scientific notation ("1e-5"): invalid (Go's parser has no exponent
 *     support either, so this deliberately does not add any).
 *
 * Throws on any invalid input — callers (price sources) should catch and
 * skip the pair, exactly like the Go client's `if err != nil { continue }`
 * pattern in pricesource.go / steem.go.
 */
export function parseLegacyDec(input: string): bigint {
  if (input.length === 0) {
    throw new Error("decimalFmt: empty decimal string");
  }

  let neg = false;
  let str = input;
  if (str[0] === "-") {
    neg = true;
    str = str.slice(1);
  }
  if (str.length === 0) {
    throw new Error("decimalFmt: empty decimal string");
  }

  const dotIndex = str.indexOf(".");
  let intPart: string;
  let fracPart: string;
  if (dotIndex === -1) {
    intPart = str;
    fracPart = "";
  } else {
    intPart = str.slice(0, dotIndex);
    fracPart = str.slice(dotIndex + 1);
    // A second "." would show up as a non-digit character in fracPart's
    // digit check below and get rejected there — no special-case needed.
    if (fracPart.length === 0 || intPart.length === 0) {
      throw new Error(`decimalFmt: invalid decimal length in ${JSON.stringify(input)}`);
    }
  }

  if (fracPart.length > LEGACY_PRECISION) {
    throw new Error(
      `decimalFmt: too many fractional digits in ${JSON.stringify(input)} (max ${LEGACY_PRECISION})`,
    );
  }

  if (!/^[0-9]+$/.test(intPart)) {
    throw new Error(`decimalFmt: invalid integer part in ${JSON.stringify(input)}`);
  }
  if (fracPart.length > 0 && !/^[0-9]+$/.test(fracPart)) {
    throw new Error(`decimalFmt: invalid fractional part in ${JSON.stringify(input)}`);
  }

  const paddedFrac = fracPart + "0".repeat(LEGACY_PRECISION - fracPart.length);
  const combinedStr = intPart + paddedFrac;

  // combinedStr is now the value scaled by 10^18. Guard against a bare "0"*N
  // (e.g. input "0") which is fine — BigInt("0") === 0n.
  let combined = BigInt(combinedStr);
  if (neg) {
    combined = -combined;
  }
  return combined;
}

/**
 * Formats a bigint (scaled by 10^18, the internal `math.LegacyDec`
 * representation) into its canonical decimal string — 18 fixed decimal
 * places, trailing zeros never trimmed. Mirrors `LegacyDec.String()`
 * exactly:
 *
 *   1230000000000000000n → "1.230000000000000000"
 *    500000000000000000n → "0.500000000000000000"
 *                      0n → "0.000000000000000000"
 *                    -1n  → "-0.000000000000000001"
 *
 * Values whose magnitude is smaller than 10^18 (i.e. the whole value is a
 * fraction less than 1) get zero-padded on the left of the fractional part;
 * larger values get the decimal point spliced in at the right position —
 * exactly the two branches in the Go source (`inputSize <= LegacyPrecision`
 * vs. the else branch).
 */
export function formatLegacyDec(raw: bigint): string {
  const isNeg = raw < 0n;
  const abs = isNeg ? -raw : raw;

  const digits = abs.toString(); // no leading zeros (BigInt.toString), "0" for zero
  let out: string;
  if (digits.length <= LEGACY_PRECISION) {
    out = "0." + "0".repeat(LEGACY_PRECISION - digits.length) + digits;
  } else {
    const decPointPlace = digits.length - LEGACY_PRECISION;
    out = digits.slice(0, decPointPlace) + "." + digits.slice(decPointPlace);
  }
  return isNeg ? "-" + out : out;
}

/**
 * Convenience wrapper: parse a plain decimal string/number and immediately
 * re-render it in `LegacyDec.String()` canonical form. This is what price
 * sources should call on every raw rate before it goes into
 * `buildExchangeRatesString` — see priceFeeder.ts.
 */
export function toLegacyDecString(input: string | number): string {
  const str = typeof input === "number" ? formatPlainNumber(input) : input;
  return formatLegacyDec(parseLegacyDec(str));
}

// formatPlainNumber renders a JS number as a plain (non-exponential) decimal
// string suitable for parseLegacyDec, for the rare caller that has a number
// rather than the exact upstream text. Prefer passing the ORIGINAL string
// from an API response (e.g. CoinMarketCap's JSON number as text) instead of
// routing through a JS number at all, since float64 can't exactly represent
// most decimal fractions — this helper exists only as a fallback.
function formatPlainNumber(n: number): string {
  if (!Number.isFinite(n)) {
    throw new Error(`decimalFmt: not a finite number: ${n}`);
  }
  // toFixed(18) avoids exponential notation for the ranges prices realistically
  // fall in; trailing digits beyond float64's real precision are noise but
  // harmless since parseLegacyDec only keeps what LegacyDec itself would.
  return n.toFixed(18);
}
