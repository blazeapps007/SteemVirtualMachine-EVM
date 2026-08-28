"""CoinGecko client, used only to price STEEM/USD_External and
SBD/USD_External -- an alternative to CMCClient, selected via
``ORACLE_PRICE_SOURCE`` (see ``oracle/.env.example``). Ports
``oracle/go/relayer/coingecko.go``'s ``CoinGeckoClient``. Unlike CMC,
``api_key`` is optional: CoinGecko's public ``/simple/price`` endpoint works
keyless (at the public rate limit); a demo or pro key just raises it.
"""

from __future__ import annotations

import json
from decimal import Decimal, InvalidOperation
from typing import Iterable

import httpx

DEFAULT_BASE_URL = "https://api.coingecko.com"

# The only place the STEEM/SBD -> CoinGecko coin-id translation happens, so
# callers (and CompositePriceSource) only ever deal in "STEEM"/"SBD", exactly
# like CMCClient.
_COIN_IDS = {"STEEM": "steem", "SBD": "steem-dollars"}


class CoinGeckoClient:
    def __init__(self, api_key: str = "", base_url: str = "", timeout: float = 15.0):
        self.api_key = api_key
        self.base_url = (base_url or DEFAULT_BASE_URL).rstrip("/")
        self._client = httpx.Client(timeout=timeout)

    def close(self) -> None:
        self._client.close()

    def fetch_usd_prices(self, symbols: Iterable[str]) -> dict[str, Decimal]:
        """Returns the latest USD price for each of the given tickers (e.g.
        "STEEM", "SBD"), batched into a single API call. Tickers this client
        doesn't have a CoinGecko id for, or that CoinGecko doesn't return a
        USD price for, are simply absent from the result -- never raises for
        a missing/malformed individual symbol, only for a transport-level
        failure."""
        symbols = list(symbols)
        ids = [_COIN_IDS[sym] for sym in symbols if sym in _COIN_IDS]
        if not ids:
            return {}

        url = f"{self.base_url}/api/v3/simple/price"
        params = {"ids": ",".join(ids), "vs_currencies": "usd"}
        headers = {"Accept": "application/json"}
        if self.api_key:
            key_header = "x-cg-pro-api-key" if "pro-api.coingecko.com" in self.base_url else "x-cg-demo-api-key"
            headers[key_header] = self.api_key
        resp = self._client.get(url, params=params, headers=headers)
        if resp.status_code != 200:
            raise RuntimeError(f"coingecko: unexpected status {resp.status_code}")

        # parse_float=str preserves CoinGecko's exact textual price
        # representation, avoiding a float round-trip before it becomes a
        # Decimal -- mirrors CMCClient's identical trick.
        try:
            parsed = json.loads(resp.text, parse_float=str)
        except json.JSONDecodeError as exc:
            raise RuntimeError(f"coingecko: invalid response: {exc}") from exc

        out: dict[str, Decimal] = {}
        for sym in symbols:
            coin_id = _COIN_IDS.get(sym)
            if coin_id is None:
                continue
            usd = (parsed.get(coin_id) or {}).get("usd")
            if usd is None:
                continue
            try:
                out[sym] = Decimal(str(usd))
            except InvalidOperation:
                continue  # malformed price from upstream: skip rather than fail the batch
        return out
