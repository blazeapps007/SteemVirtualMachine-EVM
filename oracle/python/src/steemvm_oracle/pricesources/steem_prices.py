"""STEEM/SBD (internal market) and STEEM/FEED (witness feed) pricing, both
sourced from the SAME Steem RPC node used for bridge scanning -- no
separate endpoint needed. See ``oracle/PROTOCOL.md`` SS7's price-source
table and ``oracle/go/relayer/steem.go``'s ``GetTicker``/``GetFeedHistory``
(this module is a thin per-pair wrapper around those, mirroring the Go
client's ``CompositePriceSource`` dispatch for just these two pairs).
"""

from __future__ import annotations

from decimal import Decimal

from ..steem_client import SteemClient

PAIR_STEEM_SBD = "STEEM/SBD"
PAIR_STEEM_FEED = "STEEM/FEED"


class SteemPriceSource:
    def __init__(self, steem: SteemClient):
        self._steem = steem

    def fetch_prices(self, pairs: list[str]) -> dict[str, Decimal]:
        """Prices whichever of STEEM/SBD, STEEM/FEED are requested. Never
        raises: a failure for one pair just omits it, matching the
        "partial/empty map is fine" contract every price source shares."""
        out: dict[str, Decimal] = {}
        if PAIR_STEEM_SBD in pairs:
            try:
                out[PAIR_STEEM_SBD] = self._steem.get_ticker()
            except Exception:  # noqa: BLE001 - a flaky Steem RPC just skips this pair
                pass
        if PAIR_STEEM_FEED in pairs:
            try:
                out[PAIR_STEEM_FEED] = self._steem.get_feed_history()
            except Exception:  # noqa: BLE001
                pass
        return out
