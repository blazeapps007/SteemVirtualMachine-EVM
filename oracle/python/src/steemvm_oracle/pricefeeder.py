"""Commit-reveal price feed for ``x/oracle/data``. Ports
``oracle/go/relayer/pricefeeder.go`` and its ``CompositePriceSource``
dispatcher from ``oracle/go/relayer/pricesource.go``.

The on-chain flow is commit-reveal: a prevote in period N commits
``sha256(salt:exchangeRates:validator)``; the matching vote in period N+1
reveals the salt + exact exchange-rates string, and the chain recomputes
the hash to verify (``oracle/PROTOCOL.md`` SS7). So the salt and the
byte-exact string must survive from the prevote to the following period's
reveal -- that's what ``state.FeederState`` persists.
"""

from __future__ import annotations

import hashlib
import secrets
from dataclasses import dataclass
from decimal import Decimal
from typing import Optional, Sequence

from . import _protopath  # noqa: F401
from .decimal_fmt import legacy_dec_string
from .pricesources.cmc import CMCClient
from .pricesources.steem_prices import PAIR_PRICE_FEED, PAIR_STEEM_SBD_INTERNAL, SteemPriceSource
from .signing import (
    TYPE_URL_AGGREGATE_EXCHANGE_RATE_PREVOTE,
    TYPE_URL_AGGREGATE_EXCHANGE_RATE_VOTE,
    pack_any,
)
from .state import FeederState

from steemvm.oracle.data.v1 import tx_pb2 as oracledata_tx_pb2  # noqa: E402

PAIR_STEEM_USD = "STEEM/USD_External"
PAIR_SBD_USD = "SBD/USD_External"

# Aggregate vote hash hex length (20 bytes) -- matches
# x/oracle/data/types/vote.go's aggregateVoteHashLen.
AGGREGATE_VOTE_HASH_LEN = 40


class CompositePriceSource:
    """Dispatches each whitelisted pair to the source that actually prices
    it: CoinMarketCap for STEEM/USD_External and SBD/USD_External, the Steem
    RPC node for STEEM/SBD_Internal and Price_Feed. Never raises: a source
    being ``None`` (not configured) or failing for a given pair just omits that pair from the
    result -- one flaky upstream should never block the others, and a
    whole-cycle miss is already meaningful on its own (the unified slashing
    engine counts it as a price-duty miss)."""

    def __init__(self, cmc: Optional[CMCClient], steem: Optional[SteemPriceSource]):
        self.cmc = cmc
        self.steem = steem

    def fetch_prices(self, pairs: Sequence[str]) -> dict[str, Decimal]:
        want = set(pairs)
        out: dict[str, Decimal] = {}

        if self.cmc is not None and (PAIR_STEEM_USD in want or PAIR_SBD_USD in want):
            symbols = []
            if PAIR_STEEM_USD in want:
                symbols.append("STEEM")
            if PAIR_SBD_USD in want:
                symbols.append("SBD")
            try:
                prices = self.cmc.fetch_usd_prices(symbols)
            except Exception:  # noqa: BLE001 - CMC failure never blocks Steem-sourced pairs
                prices = {}
            if "STEEM" in prices and PAIR_STEEM_USD in want:
                out[PAIR_STEEM_USD] = prices["STEEM"]
            if "SBD" in prices and PAIR_SBD_USD in want:
                out[PAIR_SBD_USD] = prices["SBD"]

        if self.steem is not None:
            steem_pairs = [p for p in (PAIR_STEEM_SBD_INTERNAL, PAIR_PRICE_FEED) if p in want]
            if steem_pairs:
                out.update(self.steem.fetch_prices(steem_pairs))

        return out


def build_exchange_rates_string(rates: dict[str, Decimal]) -> str:
    """Renders a rate map as "PAIR:rate,PAIR:rate" with pairs sorted
    lexicographically ascending, so two honest feeders that fetched the
    same prices produce a byte-identical string. Each rate uses the
    LegacyDec.String()-compatible canonical decimal form. See
    oracle/PROTOCOL.md SS7."""
    parts = [f"{pair}:{legacy_dec_string(rates[pair])}" for pair in sorted(rates.keys())]
    return ",".join(parts)


def get_aggregate_vote_hash(salt: str, exchange_rates: str, validator: str) -> str:
    """sha256(f"{salt}:{exchangeRates}:{validator}"), hex-encoded and
    truncated to the first 40 characters (20 bytes). Must byte-match
    x/oracle/data/types/vote.go's GetAggregateVoteHash."""
    source = f"{salt}:{exchange_rates}:{validator}".encode("utf-8")
    digest = hashlib.sha256(source).hexdigest()
    return digest[:AGGREGATE_VOTE_HASH_LEN]


def new_salt() -> str:
    """A fresh random 32-hex-character (16-byte) salt for a prevote commit."""
    return secrets.token_hex(16)


def build_prevote_any(validator: str, exchange_rates: str, salt: str):
    msg = oracledata_tx_pb2.MsgAggregateExchangeRatePrevote(
        validator=validator,
        hash=get_aggregate_vote_hash(salt, exchange_rates, validator),
    )
    return pack_any(TYPE_URL_AGGREGATE_EXCHANGE_RATE_PREVOTE, msg)


def build_vote_any(validator: str, salt: str, exchange_rates: str):
    msg = oracledata_tx_pb2.MsgAggregateExchangeRateVote(
        validator=validator,
        salt=salt,
        exchange_rates=exchange_rates,
    )
    return pack_any(TYPE_URL_AGGREGATE_EXCHANGE_RATE_VOTE, msg)


@dataclass
class Feeder:
    """Builds a validator's price prevote/reveal messages. Holds no chain
    or network handles -- the caller supplies the current vote period and
    whitelist (read from the node) and broadcasts the returned messages.
    ``validator`` is the bech32 ACCOUNT address (not valoper) -- see
    oracle/PROTOCOL.md SS1."""

    validator: str
    source: Optional[CompositePriceSource]

    def step(
        self,
        period: int,
        whitelist: Sequence[str],
        prev: FeederState,
    ) -> tuple[list, FeederState]:
        """Advances the feeder by one vote period. Returns (messages to
        broadcast now, state to persist for next period). Mirrors
        oracle/go/relayer/pricefeeder.go's Feeder.Step:
          - a REVEAL of the previous period's commit, but only when that
            commit was made in the immediately preceding period (a wider
            gap means the reveal window was missed -- node downtime, etc
            -- and the commit is abandoned, not late-revealed).
          - a fresh PREVOTE committing this period's prices, when a source
            is configured and returns at least one whitelisted pair.
        When no source is set (or it returns no prices), only the reveal
        (if any) is emitted and the returned state is empty."""
        msgs = []

        if prev.exchange_rates and prev.prevote_period + 1 == period:
            msgs.append(build_vote_any(self.validator, prev.salt, prev.exchange_rates))

        if self.source is None:
            return msgs, FeederState()

        rates = self.source.fetch_prices(whitelist)

        allowed = set(whitelist)
        filtered = {
            pair: rate for pair, rate in rates.items() if pair in allowed and rate is not None and rate >= 0
        }
        if not filtered:
            return msgs, FeederState()

        salt = new_salt()
        exchange_rates = build_exchange_rates_string(filtered)
        msgs.append(build_prevote_any(self.validator, exchange_rates, salt))
        return msgs, FeederState(prevote_period=period, salt=salt, exchange_rates=exchange_rates)
