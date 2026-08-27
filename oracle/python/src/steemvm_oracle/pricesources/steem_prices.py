"""STEEM/SBD_Internal (internal market) and Price_Feed (Steem's own
witness-median feed price) pricing, both sourced from the SAME Steem RPC
node used for bridge scanning -- no separate endpoint needed. See
``oracle/PROTOCOL.md`` SS7's price-source table and
``oracle/go/relayer/steem.go``'s ``GetTicker``/``GetFeedHistory`` (this
module is a thin per-pair wrapper around those, mirroring the Go client's
``CompositePriceSource`` dispatch for just these two pairs). Price_Feed is
deliberately NOT pair-shaped -- it's a single blockchain-native value, not
a tradeable market rate.
"""

from __future__ import annotations

from decimal import Decimal

from ..steem_client import SteemClient

PAIR_STEEM_SBD_INTERNAL = "STEEM/SBD_Internal"
PAIR_PRICE_FEED = "Price_Feed"


class SteemPriceSource:
    def __init__(self, steem: SteemClient):
        self._steem = steem

    def fetch_prices(self, pairs: list[str]) -> dict[str, Decimal]:
        """Prices whichever of STEEM/SBD_Internal, Price_Feed are requested.
        Never raises: a failure for one pair just omits it, matching the
        "partial/empty map is fine" contract every price source shares."""
        out: dict[str, Decimal] = {}
        if PAIR_STEEM_SBD_INTERNAL in pairs:
            try:
                out[PAIR_STEEM_SBD_INTERNAL] = self._steem.get_ticker()
            except Exception:  # noqa: BLE001 - a flaky Steem RPC just skips this pair
                pass
        if PAIR_PRICE_FEED in pairs:
            try:
                out[PAIR_PRICE_FEED] = self._steem.get_feed_history()
            except Exception:  # noqa: BLE001
                pass
        return out
