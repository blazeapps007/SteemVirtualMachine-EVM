"""Reproduces Cosmos SDK ``math.LegacyDec.String()``'s canonical decimal
format: fixed 18 decimal places, trailing zeros **never trimmed**.

E.g. ``1.23`` -> ``"1.230000000000000000"``, ``0.5`` -> ``"0.500000000000000000"``.

This is ``oracle/PROTOCOL.md`` SS7's explicitly flagged highest-risk
formatting detail in the whole protocol: a default "format as decimal" call
in Python (``Decimal.normalize()``/plain ``str()``) does NOT produce this --
neither trims to the shortest representation nor pads to a fixed width by
default the way this function does. A wrong string either fails
``ParseExchangeRateTuples`` outright (loud) or -- worse -- produces a
*different* string than was hashed into the prevote commit, which the chain
rejects via ``ErrHashVerification`` on reveal (loud, but only visible one
full vote period after the mistake was made).
"""

from __future__ import annotations

from decimal import ROUND_HALF_UP, Decimal, InvalidOperation

# Cosmos SDK math.LegacyDec's fixed precision (18 decimal digits).
LEGACY_DEC_PRECISION = 18

_QUANTUM = Decimal(1).scaleb(-LEGACY_DEC_PRECISION)  # 10^-18


def legacy_dec_string(value: Decimal | str | int | float) -> str:
    """Formats ``value`` exactly like ``math.LegacyDec.String()``: a fixed
    18 decimal places, never trimmed. Rounds (half-up, matching
    ``LegacyDec``'s own internal ``Quo``/multiplication rounding) rather
    than truncates if given a value with more than 18 fractional digits, so
    it is safe to call on a raw division result.

    Accepts a ``Decimal`` (preferred -- avoids a float round-trip), or a
    ``str``/``int`` that ``Decimal()`` can parse. Floats are accepted for
    convenience but should be avoided by callers that care about exactness.
    """
    if isinstance(value, Decimal):
        d = value
    elif isinstance(value, float):
        # Route floats through repr() so at least the nearest-representable
        # decimal is used, rather than Decimal(float)'s raw binary expansion.
        d = Decimal(repr(value))
    else:
        try:
            d = Decimal(str(value))
        except InvalidOperation as exc:
            raise ValueError(f"not a valid decimal value: {value!r}") from exc

    sign = "-" if d.is_signed() and d != 0 else ""
    d = abs(d)
    quantized = d.quantize(_QUANTUM, rounding=ROUND_HALF_UP)

    _, digits, exponent = quantized.as_tuple()
    if exponent != -LEGACY_DEC_PRECISION:
        # quantize() can only fail to land exactly on -18 if the magnitude is
        # astronomically large (more integer digits than Decimal's default
        # context precision allows) -- not a realistic price/exchange-rate value.
        raise ValueError(f"value {value!r} is out of range for an 18-decimal fixed format")

    digits_str = "".join(str(digit) for digit in digits)
    if len(digits_str) <= LEGACY_DEC_PRECISION:
        digits_str = digits_str.rjust(LEGACY_DEC_PRECISION + 1, "0")

    int_part = digits_str[:-LEGACY_DEC_PRECISION]
    frac_part = digits_str[-LEGACY_DEC_PRECISION:]
    return f"{sign}{int_part}.{frac_part}"


def parse_legacy_dec(s: str) -> Decimal:
    """Inverse of legacy_dec_string, for tests/round-tripping: parses a
    LegacyDec-formatted (or any plain decimal) string into a Decimal."""
    try:
        return Decimal(s)
    except InvalidOperation as exc:
        raise ValueError(f"not a valid decimal string: {s!r}") from exc
