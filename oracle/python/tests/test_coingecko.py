import json
from decimal import Decimal

import httpx
import pytest

from steemvm_oracle.pricesources.coingecko import CoinGeckoClient


def _client_with_mock(handler, api_key: str = "", base_url: str = "") -> CoinGeckoClient:
    """Builds a CoinGeckoClient whose internal httpx.Client is wired to a
    MockTransport instead of the network -- CoinGeckoClient itself has no
    injectable-transport constructor param, so this replaces the instance's
    private client the same way any test double would."""
    c = CoinGeckoClient(api_key=api_key, base_url=base_url)
    c._client = httpx.Client(transport=httpx.MockTransport(handler))
    return c


def test_fetch_usd_prices_preserves_precision():
    # A price with many significant digits -- regression test for the float
    # round-trip precision loss parse_float=str avoids. The JSON body is a
    # raw string literal, NOT json.dumps() on a dict with a Python float --
    # a float literal with this many significant digits would itself already
    # be rounded by the time json.dumps serializes it, defeating the point
    # of the test (this is the exact class of bug parse_float=str exists to
    # prevent, so the test must not reintroduce it on the input side).
    body = '{"steem":{"usd":0.123456789012345678},"steem-dollars":{"usd":1.0}}'

    def handler(request: httpx.Request) -> httpx.Response:
        assert request.url.path == "/api/v3/simple/price"
        assert request.url.params["ids"] == "steem,steem-dollars"
        assert request.url.params["vs_currencies"] == "usd"
        return httpx.Response(200, content=body)

    c = _client_with_mock(handler)
    prices = c.fetch_usd_prices(["STEEM", "SBD"])
    assert prices["STEEM"] == Decimal("0.123456789012345678")
    assert prices["SBD"] == Decimal("1.0")


def test_missing_id_is_skipped_not_fatal():
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, content=json.dumps({"steem": {"usd": 0.15}}))

    c = _client_with_mock(handler)
    prices = c.fetch_usd_prices(["STEEM", "SBD"])
    assert "STEEM" in prices
    assert "SBD" not in prices


@pytest.mark.parametrize(
    ("base_url", "expected_header"),
    [
        ("", "x-cg-demo-api-key"),
        ("https://pro-api.coingecko.com", "x-cg-pro-api-key"),
    ],
)
def test_auth_header_selection(base_url, expected_header):
    seen = {}

    def handler(request: httpx.Request) -> httpx.Response:
        for h in ("x-cg-demo-api-key", "x-cg-pro-api-key"):
            if h in request.headers:
                seen["header"] = h
                seen["value"] = request.headers[h]
        return httpx.Response(200, content=json.dumps({}))

    c = _client_with_mock(handler, api_key="k1", base_url=base_url)
    c.fetch_usd_prices(["STEEM"])
    assert seen["header"] == expected_header
    assert seen["value"] == "k1"


def test_no_key_sends_no_auth_header():
    seen = {}

    def handler(request: httpx.Request) -> httpx.Response:
        seen["has_demo"] = "x-cg-demo-api-key" in request.headers
        seen["has_pro"] = "x-cg-pro-api-key" in request.headers
        return httpx.Response(200, content=json.dumps({}))

    c = _client_with_mock(handler, api_key="")
    c.fetch_usd_prices(["STEEM"])
    assert seen["has_demo"] is False
    assert seen["has_pro"] is False
