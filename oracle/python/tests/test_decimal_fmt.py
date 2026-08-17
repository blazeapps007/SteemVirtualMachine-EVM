"""Unit tests for decimal_fmt.legacy_dec_string against oracle/PROTOCOL.md
SS7's worked examples: Cosmos SDK math.LegacyDec.String() format -- fixed 18
decimal places, trailing zeros never trimmed. This is flagged in
PROTOCOL.md as the single highest-risk formatting detail in the whole
protocol (a wrong string silently breaks the commit-reveal hash), so these
tests pin the exact worked examples plus edge cases a naive
Decimal.normalize()/str() implementation would get wrong.
"""
from __future__ import annotations

from decimal import Decimal

import pytest

from steemvm_oracle.decimal_fmt import legacy_dec_string, parse_legacy_dec


class TestLegacyDecStringWorkedExamples:
    """PROTOCOL.md SS7: '1.23' -> "1.230000000000000000", '0.5' ->
    "0.500000000000000000". Trailing zeros are never trimmed."""

    def test_one_point_two_three(self):
        assert legacy_dec_string("1.23") == "1.230000000000000000"

    def test_zero_point_five(self):
        assert legacy_dec_string("0.5") == "0.500000000000000000"

    def test_one_point_two_three_as_decimal(self):
        # Decimal input (preferred path -- avoids a float round-trip).
        assert legacy_dec_string(Decimal("1.23")) == "1.230000000000000000"


class TestLegacyDecStringEdgeCases:
    def test_integer_value(self):
        assert legacy_dec_string("1") == "1.000000000000000000"

    def test_zero(self):
        assert legacy_dec_string("0") == "0.000000000000000000"
        assert legacy_dec_string(0) == "0.000000000000000000"

    def test_no_negative_zero_sign(self):
        # -0 must not render as "-0.000...".
        assert legacy_dec_string(Decimal("-0")) == "0.000000000000000000"

    def test_negative_value(self):
        assert legacy_dec_string("-1.23") == "-1.230000000000000000"

    def test_already_full_precision(self):
        assert legacy_dec_string("1.230000000000000000") == "1.230000000000000000"

    def test_python_default_str_would_be_wrong(self):
        # The whole point of PROTOCOL.md SS7's warning: a bare str(Decimal(...))
        # or Decimal.normalize() does NOT pad to 18 fixed decimals.
        d = Decimal("1.23")
        assert str(d) != "1.230000000000000000"
        assert d.normalize().__str__() != "1.230000000000000000"
        # ...but legacy_dec_string does.
        assert legacy_dec_string(d) == "1.230000000000000000"

    def test_large_integer_part(self):
        assert legacy_dec_string("1000000.5") == "1000000.500000000000000000"

    def test_rounds_more_than_18_fractional_digits_half_up(self):
        # 19 fractional digits -> rounds (half-up) to 18, does not truncate.
        assert legacy_dec_string("1.1234567890123456785") == "1.123456789012345679"

    def test_int_and_float_inputs_accepted(self):
        assert legacy_dec_string(1) == "1.000000000000000000"
        assert legacy_dec_string(0.5) == "0.500000000000000000"

    def test_invalid_value_raises(self):
        with pytest.raises(ValueError):
            legacy_dec_string("not-a-number")


class TestParseLegacyDec:
    def test_round_trips_worked_examples(self):
        assert parse_legacy_dec("1.230000000000000000") == Decimal("1.23")
        assert parse_legacy_dec("0.500000000000000000") == Decimal("0.5")

    def test_invalid_string_raises(self):
        with pytest.raises(ValueError):
            parse_legacy_dec("not-a-number")


class TestExchangeRatesStringOrdering:
    """PROTOCOL.md SS7: pairs sorted lexicographically ascending -- plain
    byte/codepoint sort: SBD/USD < STEEM/FEED < STEEM/SBD < STEEM/USD."""

    def test_whitelist_sort_order(self):
        pairs = ["STEEM/USD", "STEEM/SBD", "SBD/USD", "STEEM/FEED"]
        assert sorted(pairs) == ["SBD/USD", "STEEM/FEED", "STEEM/SBD", "STEEM/USD"]
