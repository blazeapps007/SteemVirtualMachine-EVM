"""CoinMarketCap client, used only to price STEEM/USD and SBD/USD (Steem
has no native USD market). Ports ``oracle/go/relayer/pricesource.go``'s
``CMCClient``. Configured via ``ORACLE_CMC_API_KEY``/``ORACLE_CMC_BASE_URL``
-- see ``oracle/.env.example``.
"""

from __future__ import annotations

import json
from decimal import Decimal, InvalidOperation
from typing import Iterable

import httpx

DEFAULT_BASE_URL = "https://pro-api.coinmarketcap.com"


class CMCClient:
    def __init__(self, api_key: str, base_url: str = "", timeout: float = 15.0):
        self.api_key = api_key
        self.base_url = (base_url or DEFAULT_BASE_URL).rstrip("/")
        self._client = httpx.Client(timeout=timeout)

    def close(self) -> None:
        self._client.close()

    def fetch_usd_prices(self, symbols: Iterable[str]) -> dict[str, Decimal]:
        """Returns the latest USD price for each of the given CoinMarketCap
        symbols (e.g. "STEEM", "SBD"), batched into a single API call.
        Symbols CoinMarketCap doesn't return are simply absent from the
        result -- never raises for a missing/malformed individual symbol,
        only for a transport-level failure."""
        symbols = list(symbols)
        if not symbols:
            return {}

        url = f"{self.base_url}/v2/cryptocurrency/quotes/latest"
        params = {"symbol": ",".join(symbols), "convert": "USD"}
        headers = {"X-CMC_PRO_API_KEY": self.api_key, "Accept": "application/json"}
        resp = self._client.get(url, params=params, headers=headers)
        if resp.status_code != 200:
            raise RuntimeError(f"coinmarketcap: unexpected status {resp.status_code}")

        # parse_float=str preserves CoinMarketCap's exact textual price
        # representation, avoiding a float round-trip before it becomes a
        # Decimal -- mirrors the Go client's json.Number use (dec.UseNumber()).
        try:
            parsed = json.loads(resp.text, parse_float=str)
        except json.JSONDecodeError as exc:
            raise RuntimeError(f"coinmarketcap: invalid response: {exc}") from exc

        data = parsed.get("data") or {}
        out: dict[str, Decimal] = {}
        for sym in symbols:
            entries = data.get(sym)
            if not entries:
                continue
            usd = (entries[0].get("quote") or {}).get("USD") or {}
            price_raw = usd.get("price")
            if price_raw is None:
                continue
            try:
                out[sym] = Decimal(str(price_raw))
            except InvalidOperation:
                continue  # malformed price from upstream: skip rather than fail the batch
        return out
