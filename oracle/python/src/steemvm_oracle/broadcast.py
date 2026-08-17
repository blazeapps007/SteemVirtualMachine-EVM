"""REST (``:1317``) account/sequence lookup + tx broadcast/delivery polling.

Mirrors ``oracle/go/relayer/broadcast.go``'s cadence and gas conventions
(see ``oracle/PROTOCOL.md`` SS3-4): fetch the account fresh per call,
broadcast ``BROADCAST_MODE_SYNC``, then poll for delivery -- the scan
cursor / commit state must never advance past a tx that was only
CheckTx-accepted, never actually executed.
"""

from __future__ import annotations

import base64
import time
from dataclasses import dataclass
from typing import Sequence

import httpx

from .keys import Keypair
from .signing import Coin, SignedTx, parse_gas_prices, sign_tx

# Mirrors oracle/go/relayer/broadcast.go's constants.
GAS_PER_MSG = 400_000
GAS_BASE = 200_000
MAX_MSGS_PER_TX = 50
PRICE_FEED_GAS_PER_MSG = 250_000

DEFAULT_POLL_EVERY = 2.0
DEFAULT_MAX_WAIT = 45.0


class BroadcastError(RuntimeError):
    """A tx was rejected at broadcast time, failed on-chain, or delivery
    could not be confirmed within the timeout."""


class NotFoundError(RuntimeError):
    """A query targeted a record that does not exist (e.g. no deposit yet
    at a given (txid, op_index)) -- the REST equivalent of oracle/go's
    ignoreNotFound message-substring match, since different Cosmos SDK/
    gRPC-gateway versions map "key not found" ABCI errors to different HTTP
    statuses."""


@dataclass(frozen=True)
class Account:
    account_number: int
    sequence: int


class RestClient:
    """Thin wrapper around the node's Cosmos REST (gRPC-gateway) API."""

    def __init__(self, base_url: str, timeout: float = 15.0):
        self.base_url = base_url.rstrip("/")
        self._client = httpx.Client(timeout=timeout)

    def close(self) -> None:
        self._client.close()

    def __enter__(self) -> "RestClient":
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    def get_account(self, address: str) -> Account:
        """``GET /cosmos/auth/v1beta1/accounts/{address}``. Empirically
        confirmed (Milestone 1 devnet spike -- see tests/test_signing_vectors.py
        and the task report) that the account object is a plain
        ``/cosmos.auth.v1beta1.BaseAccount`` with ``account_number``/``sequence``
        as JSON strings."""
        resp = self._client.get(f"{self.base_url}/cosmos/auth/v1beta1/accounts/{address}")
        resp.raise_for_status()
        acct = resp.json()["account"]
        return Account(account_number=int(acct["account_number"]), sequence=int(acct["sequence"]))

    def get_json(self, path: str, params: dict | None = None) -> dict:
        """GET an arbitrary REST (gRPC-gateway) path and return its parsed
        JSON body. Raises NotFoundError for a "record doesn't exist" style
        response (see NotFoundError's docstring), otherwise raises on any
        non-2xx status."""
        resp = self._client.get(f"{self.base_url}{path}", params=params)
        if resp.status_code == 200:
            return resp.json()
        try:
            body = resp.json()
        except ValueError:
            body = {}
        message = str(body.get("message", ""))
        if resp.status_code in (400, 404, 500) and "not found" in message.lower():
            raise NotFoundError(message or f"not found: {path}")
        resp.raise_for_status()
        return resp.json()

    def broadcast_tx_sync(self, tx_bytes: bytes) -> dict:
        """``POST /cosmos/tx/v1beta1/txs`` with ``mode: BROADCAST_MODE_SYNC``.
        Returns the raw ``tx_response`` object (``code``/``txhash``/``raw_log``)."""
        body = {
            "tx_bytes": base64.b64encode(tx_bytes).decode("ascii"),
            "mode": "BROADCAST_MODE_SYNC",
        }
        resp = self._client.post(f"{self.base_url}/cosmos/tx/v1beta1/txs", json=body)
        resp.raise_for_status()
        return resp.json()["tx_response"]

    def get_tx(self, tx_hash: str) -> dict | None:
        """``GET /cosmos/tx/v1beta1/txs/{hash}``. Returns ``None`` if the tx
        is not yet found (a "not found" 404/5xx from the gateway), the full
        response dict otherwise."""
        resp = self._client.get(f"{self.base_url}/cosmos/tx/v1beta1/txs/{tx_hash}")
        if resp.status_code == 200:
            return resp.json()
        if resp.status_code in (400, 404, 500):
            # Different SDK/gateway versions report "not found" at different
            # status codes; treat any of them as "not yet landed" rather than
            # raising, so the caller's poll loop just keeps trying.
            return None
        resp.raise_for_status()
        return None

    def wait_for_delivery(
        self,
        tx_hash: str,
        *,
        poll_every: float = DEFAULT_POLL_EVERY,
        max_wait: float = DEFAULT_MAX_WAIT,
    ) -> dict:
        """Polls until the tx is found in a block with code 0, raising
        BroadcastError on delivery failure or timeout. Mirrors
        oracle/go/relayer/broadcast.go's waitForDelivery."""
        deadline = time.monotonic() + max_wait
        while True:
            time.sleep(poll_every)
            data = self.get_tx(tx_hash)
            if data is not None:
                tx_response = data["tx_response"]
                code = int(tx_response.get("code", 0))
                if code != 0:
                    raise BroadcastError(
                        f"attestation tx {tx_hash} failed in block "
                        f"{tx_response.get('height')} (code {code}): {tx_response.get('raw_log')}"
                    )
                return tx_response
            if time.monotonic() > deadline:
                raise BroadcastError(f"attestation tx {tx_hash} not observed in a block within {max_wait}s")


def submit_tx(
    rest: RestClient,
    keypair: Keypair,
    chain_id: str,
    msg_anys: Sequence,
    *,
    gas_limit: int,
    fee: Sequence[Coin] = (),
    memo: str = "",
) -> str:
    """Signs and broadcasts one tx (fresh account/sequence lookup each call
    -- these clients send at most one tx per poll cycle, same as the Go
    reference), waits for on-chain delivery, and returns the tx hash."""
    account = rest.get_account(keypair.address)
    signed: SignedTx = sign_tx(
        keypair,
        msg_anys,
        chain_id=chain_id,
        account_number=account.account_number,
        sequence=account.sequence,
        gas_limit=gas_limit,
        fee=fee,
        memo=memo,
    )
    tx_response = rest.broadcast_tx_sync(signed.tx_raw_bytes)
    code = int(tx_response.get("code", 0))
    tx_hash = tx_response.get("txhash", "")
    if code != 0:
        raise BroadcastError(f"tx rejected (code {code}): {tx_response.get('raw_log')}")

    # Sync broadcast only proves the tx passed CheckTx; wait for it to land
    # in a block and succeed before the caller advances any cursor/state.
    rest.wait_for_delivery(tx_hash)
    return tx_hash


def broadcast_attestations(rest: RestClient, keypair: Keypair, chain_id: str, msg_anys: Sequence) -> str | None:
    """Signs+broadcasts a zero-fee tx of bridge-attestation messages
    (fee-exempt for bonded validators -- see oracle/PROTOCOL.md SS3)."""
    if not msg_anys:
        return None
    if len(msg_anys) > MAX_MSGS_PER_TX:
        raise ValueError(f"too many messages in one tx: {len(msg_anys)} > {MAX_MSGS_PER_TX}")
    gas = GAS_BASE + GAS_PER_MSG * len(msg_anys)
    return submit_tx(rest, keypair, chain_id, msg_anys, gas_limit=gas, fee=())


def broadcast_price_feed_msgs(
    rest: RestClient,
    keypair: Keypair,
    chain_id: str,
    msg_anys: Sequence,
    gas_prices: str,
) -> str | None:
    """Signs+broadcasts price-feed messages (prevote/vote). NOT fee-exempt
    -- ``gas_prices`` must be a non-empty Cosmos SDK DecCoins string (e.g.
    ``"1000000000asteem"``) -- see oracle/PROTOCOL.md SS3."""
    if not msg_anys:
        return None
    if not gas_prices:
        raise ValueError("price feed txs are not fee-exempt: ORACLE_GAS_PRICES must be set")
    gas = PRICE_FEED_GAS_PER_MSG * len(msg_anys)
    fee = parse_gas_prices(gas_prices, gas)
    return submit_tx(rest, keypair, chain_id, msg_anys, gas_limit=gas, fee=fee)
