"""Steem ``condenser_api`` JSON-RPC client, gateway-transfer/payout
extraction, and Steem-sourced price pairs. Ports
``oracle/go/relayer/steem.go`` field-for-field.

Only reads (dynamic global properties, blocks, ticker, feed history) --
mirrors the Go client's choice to avoid a full Steem SDK dependency.
"""

from __future__ import annotations

from dataclasses import dataclass
from decimal import Decimal
from typing import Any, Optional

import httpx

# Bridge asset tags -- mirrors steemvm.steembridge.v1.BridgeAsset (see
# steemvm_oracle.router, which owns the protobuf-facing enum values; this
# module only needs to distinguish the two at the Python level).
ASSET_STEEM = "STEEM"
ASSET_SBD = "SBD"


@dataclass(frozen=True)
class Transfer:
    """One Steem transfer operation addressed to the gateway account,
    carrying exactly the raw facts a validator attests on-chain."""

    txid: str
    op_index: int
    steem_block: int
    steem_timestamp: str
    from_: str
    amount_millisteem: int
    memo: str
    asset: str  # ASSET_STEEM or ASSET_SBD


@dataclass(frozen=True)
class Payout:
    """A gateway->user payout of a bridge-out on Steem, identified by its
    "svm-withdrawal <id>" memo."""

    withdrawal_id: int
    txid: str
    op_index: int
    steem_block: int
    steem_timestamp: str


class SteemRpcError(RuntimeError):
    pass


class SteemClient:
    def __init__(self, rpc_url: str, timeout: float = 15.0):
        self.url = rpc_url
        self._client = httpx.Client(timeout=timeout)

    def close(self) -> None:
        self._client.close()

    def _call(self, method: str, params: Optional[list] = None) -> Any:
        body = {"jsonrpc": "2.0", "method": method, "params": params or [], "id": 1}
        resp = self._client.post(self.url, json=body)
        resp.raise_for_status()
        data = resp.json()
        if data.get("error") is not None:
            err = data["error"]
            raise SteemRpcError(f"steem rpc: {err.get('message')} (code {err.get('code')})")
        return data.get("result")

    def last_irreversible_block(self) -> int:
        dgp = self._call("condenser_api.get_dynamic_global_properties")
        lib = int(dgp.get("last_irreversible_block_num", 0)) if dgp else 0
        if lib == 0:
            raise SteemRpcError("steem rpc: zero last irreversible block")
        return lib

    def fetch_blocks(self, from_num: int, to_num: int) -> list[tuple[int, dict]]:
        """Returns [(block_num, block_dict), ...] for from_num..to_num
        inclusive, fetched sequentially. A null result for a block at or
        below the last irreversible block is an error, not a skip (mirrors
        oracle/go/relayer/steem.go's FetchBlocks): it means the RPC endpoint
        hasn't served the block yet, so the caller should retry the whole
        cycle rather than silently step the cursor over it."""
        if from_num > to_num:
            return []
        blocks: list[tuple[int, dict]] = []
        for num in range(from_num, to_num + 1):
            block = self._call("condenser_api.get_block", [num])
            if not block:
                raise SteemRpcError(f"steem rpc returned no data for irreversible block {num}")
            blocks.append((num, block))
        return blocks

    def get_ticker(self) -> Decimal:
        """STEEM/SBD (Steem's internal-market last-trade price) via
        condenser_api.get_ticker's "latest" field."""
        resp = self._call("condenser_api.get_ticker")
        latest = (resp or {}).get("latest", "")
        try:
            return Decimal(str(latest).strip())
        except Exception as exc:  # noqa: BLE001 - re-raise with context
            raise SteemRpcError(f"steem rpc: invalid ticker latest price {latest!r}: {exc}") from exc

    def get_feed_history(self) -> Decimal:
        """STEEM/FEED (Steem's witness-median feed price) via
        condenser_api.get_feed_history's current_median_history base/quote
        pair (e.g. base "0.250 SBD", quote "1.000 STEEM" -> price 0.25)."""
        resp = self._call("condenser_api.get_feed_history")
        median = (resp or {}).get("current_median_history", {})
        base = _parse_asset_amount_dec(median.get("base", ""), "SBD")
        quote = _parse_asset_amount_dec(median.get("quote", ""), "STEEM")
        if quote == 0:
            raise SteemRpcError("steem rpc: feed history quote is zero")
        return base / quote


def _parse_asset_amount_dec(amount: str, symbol: str) -> Decimal:
    parts = str(amount).split()
    if len(parts) != 2 or parts[1] != symbol:
        raise SteemRpcError(f"expected a {symbol!r} amount, got {amount!r}")
    try:
        return Decimal(parts[0])
    except Exception as exc:  # noqa: BLE001
        raise SteemRpcError(f"invalid {symbol} amount {amount!r}: {exc}") from exc


def parse_steem_amount(amount: str, symbol: str) -> Optional[int]:
    """Converts a Steem asset string like "70.561 STEEM" into millisteem
    (70561). Only the given bridgeable symbol qualifies. Returns None for a
    wrong symbol or malformed amount. Integer-only parsing (no float
    round-trip) -- mirrors oracle/go/relayer/steem.go's ParseSteemAmount so
    every validator computes the identical integer."""
    parts = amount.strip().split(" ")
    if len(parts) != 2 or parts[1] != symbol:
        return None

    value = parts[0]
    if "." in value:
        int_part, frac_part = value.split(".", 1)
    else:
        int_part, frac_part = value, ""
    if int_part == "" or len(frac_part) > 3:
        return None
    frac_part = frac_part + ("0" * (3 - len(frac_part)))

    digits = int_part + frac_part
    if not digits.isdigit():
        return None
    return int(digits)


def parse_withdrawal_memo(memo: str) -> Optional[int]:
    """Parses a gateway payout memo "svm-withdrawal <id>" into the
    withdrawal id, or None if the memo doesn't match."""
    fields = memo.strip().split()
    if len(fields) != 2 or fields[0] != "svm-withdrawal":
        return None
    try:
        return int(fields[1], 10)
    except ValueError:
        return None


def _op_transfer_payload(op: Any) -> Optional[dict]:
    """Operations are ["type", {body}] tuples; returns the body dict if
    op is a "transfer" operation, else None."""
    if not (isinstance(op, list) and len(op) == 2):
        return None
    op_type, body = op
    if op_type != "transfer" or not isinstance(body, dict):
        return None
    return body


def extract_gateway_transfers(
    block_num: int,
    block: dict,
    gateway: str,
    steem_symbol: str,
    sbd_symbol: str,
) -> list[Transfer]:
    """Scans a block for STEEM and SBD transfer operations whose recipient
    is the gateway account. Leaving sbd_symbol "" disables SBD extraction
    (the v0.0.3 feature-gate). Mirrors
    oracle/go/relayer/steem.go's ExtractGatewayTransfers."""
    if not block:
        return []

    transaction_ids = block.get("transaction_ids") or []
    transactions = block.get("transactions") or []
    timestamp = block.get("timestamp", "")

    transfers: list[Transfer] = []
    for tx_num, tx in enumerate(transactions):
        txid = tx.get("transaction_id") or (transaction_ids[tx_num] if tx_num < len(transaction_ids) else "")
        if not txid:
            continue

        for op_index, raw_op in enumerate(tx.get("operations") or []):
            body = _op_transfer_payload(raw_op)
            if body is None:
                continue
            if body.get("to") != gateway:
                continue

            amount_str = body.get("amount", "")
            amount = parse_steem_amount(amount_str, steem_symbol)
            if amount is not None:
                asset = ASSET_STEEM
            elif sbd_symbol:
                amount = parse_steem_amount(amount_str, sbd_symbol)
                if amount is None:
                    continue
                asset = ASSET_SBD
            else:
                continue

            transfers.append(
                Transfer(
                    txid=txid,
                    op_index=op_index,
                    steem_block=block_num,
                    steem_timestamp=timestamp,
                    from_=body.get("from", ""),
                    amount_millisteem=amount,
                    memo=body.get("memo", ""),
                    asset=asset,
                )
            )
    return transfers


def extract_gateway_payouts(block_num: int, block: dict, gateway: str) -> list[Payout]:
    """Scans a block for transfer operations sent FROM the gateway account
    whose memo is "svm-withdrawal <id>". Mirrors
    oracle/go/relayer/steem.go's ExtractGatewayPayouts."""
    if not block:
        return []

    transaction_ids = block.get("transaction_ids") or []
    transactions = block.get("transactions") or []
    timestamp = block.get("timestamp", "")

    payouts: list[Payout] = []
    for tx_num, tx in enumerate(transactions):
        txid = tx.get("transaction_id") or (transaction_ids[tx_num] if tx_num < len(transaction_ids) else "")
        if not txid:
            continue

        for op_index, raw_op in enumerate(tx.get("operations") or []):
            body = _op_transfer_payload(raw_op)
            if body is None:
                continue
            if body.get("from") != gateway:
                continue
            withdrawal_id = parse_withdrawal_memo(body.get("memo", ""))
            if withdrawal_id is None:
                continue
            payouts.append(
                Payout(
                    withdrawal_id=withdrawal_id,
                    txid=txid,
                    op_index=op_index,
                    steem_block=block_num,
                    steem_timestamp=timestamp,
                )
            )
    return payouts
