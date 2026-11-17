"""Node pool, Steem RPC failover and the block-scan history fallback (ports
oracle/go/nodepool, relayer/steem_failover_test.go and multisig/chain_test.go)."""

from __future__ import annotations

import json

import httpx
import pytest

from _ms import BANK, NOW, ts
from steemvm_oracle.multisig import steemtx
from steemvm_oracle.multisig.chain import BLOCK_SCAN_BATCH, HistoryIndex, block_lookback, payout_link_json, PAYOUT_LINK_ID
from steemvm_oracle.nodepool import ROUND_ROBIN, STICKY, NodePool, parse
from steemvm_oracle.steem_client import SteemClient, SteemRpcError


def test_parse():
    assert parse(" https://a/ , https://b", "https://a,,https://c ") == ["https://a", "https://b", "https://c"]


def test_round_robin_spreads_calls():
    p = NodePool(ROUND_ROBIN, "a,b,c")
    assert [p.order()[0] for _ in range(6)] == ["a", "b", "c", "a", "b", "c"]


def test_sticky_and_cooldown():
    now = [1000.0]
    p = NodePool(STICKY, "own,b,c", now=lambda: now[0])
    assert p.order()[0] == "own" and p.order()[0] == "own"
    assert p.fail("own") == 30
    assert p.order() == ["b", "c", "own"]
    now[0] += 31
    assert p.order()[0] == "own"
    assert [p.fail("own") for _ in range(6)] == [60, 120, 240, 480, 600, 600]
    p.ok("own")
    assert p.fail("own") == 30


def test_all_benched_soonest_first():
    p = NodePool(ROUND_ROBIN, "a,b", now=lambda: 1000.0)
    p.fail("a")
    p.fail("a")
    p.fail("b")
    assert p.order() == ["b", "a"]


# --------------------------------------------------------------------------
# Fake nodes behind one httpx MockTransport, routed by host
# --------------------------------------------------------------------------


class Nodes:
    def __init__(self) -> None:
        self.handlers: dict[str, object] = {}
        self.calls: dict[str, int] = {}

    def add(self, host: str, handler) -> str:
        self.handlers[host] = handler
        self.calls[host] = 0
        return f"http://{host}"

    def transport(self) -> httpx.MockTransport:
        def handle(request: httpx.Request) -> httpx.Response:
            host = request.url.host
            self.calls[host] += 1
            return self.handlers[host](json.loads(request.content))
        return httpx.MockTransport(handle)


def html(_req):
    return httpx.Response(429, text="<html><body>429 Too Many Requests</body></html>")


def rpc_error(message):
    return lambda _req: httpx.Response(200, json={"jsonrpc": "2.0", "id": 1, "error": {"code": -32603, "message": message}})


def block_server(head: int, txs: dict | None = None):
    txs = txs or {}

    def handle(req):
        num = req["params"][0]
        if req["method"] != "condenser_api.get_block":
            return rpc_error("Internal Error")(req)
        if num > head:
            return httpx.Response(200, json={"jsonrpc": "2.0", "id": 1, "result": None})
        block_txs = txs.get(num, [])
        return httpx.Response(200, json={"jsonrpc": "2.0", "id": 1, "result": {
            "timestamp": ts(NOW), "transaction_ids": [f"{num:08x}{i:032d}" for i in range(len(block_txs))],
            "transactions": [{"operations": ops} for ops in block_txs]}})
    return handle


def steem_client(nodes: Nodes, *urls: str) -> SteemClient:
    c = SteemClient(",".join(urls))
    c._client = httpx.Client(transport=nodes.transport())
    return c


def test_fetch_blocks_spreads_calls():
    n = Nodes()
    c = steem_client(n, n.add("a", block_server(1000)), n.add("b", block_server(1000)), n.add("c", block_server(1000)))
    assert len(c.fetch_blocks(1, 90)) == 90
    assert n.calls == {"a": 30, "b": 30, "c": 30}


def test_fetch_blocks_skips_blocked_and_lagging_nodes():
    n = Nodes()
    c = steem_client(n, n.add("blocked", html), n.add("lagging", block_server(10)), n.add("good", block_server(1000)))
    assert len(c.fetch_blocks(1, 50)) == 50
    assert n.calls["blocked"] == 1
    assert n.calls["lagging"] <= 12


def test_fetch_blocks_fails_when_no_node_has_the_block():
    n = Nodes()
    c = steem_client(n, n.add("a", block_server(5)), n.add("b", block_server(5)))
    with pytest.raises(SteemRpcError, match="all 2 Steem nodes failed.*no data for irreversible block 6"):
        c.fetch_blocks(5, 6)


def test_single_node_html_error():
    n = Nodes()
    c = steem_client(n, n.add("a", html))
    with pytest.raises(SteemRpcError, match=r"steem rpc: invalid response: HTML page instead of JSON \(HTTP 429\)"):
        c.last_irreversible_block()


# --------------------------------------------------------------------------
# svm.bank signer RPC + history
# --------------------------------------------------------------------------


def _link(wid: int, auth: str = BANK) -> dict:
    return {"required_auths": [auth], "required_posting_auths": [], "id": PAYOUT_LINK_ID, "json": payout_link_json(wid)}


def history_server(entries: list):
    def handle(req):
        if req["method"] != "condenser_api.get_account_history":
            return rpc_error("unexpected")(req)
        _, start, limit = req["params"]
        top = min(start, len(entries) - 1)
        page = [[seq, entries[seq]] for seq in range(max(0, top - limit), top + 1)]
        return httpx.Response(200, json={"jsonrpc": "2.0", "id": 1, "result": page})
    return handle


@pytest.mark.parametrize("bad", [
    html,
    rpc_error("Internal Error"),
    rpc_error("Bad Cast:Invalid cast from object_type to Array"),
    lambda _req: httpx.Response(200, json={"jsonrpc": "2.0", "id": 1, "result": []}),
])
def test_history_sync_fails_over_bad_nodes(bad):
    entries = [{"trx_id": "0" * 39 + "1", "timestamp": "2026-01-01T00:00:00", "op": ["account_create", {"new_account_name": BANK}]},
               {"trx_id": "trx000", "timestamp": ts(NOW), "op": ["custom_json", _link(7)]}]
    n = Nodes()
    rpc = steemtx.RPC([n.add("bad", bad), n.add("good", history_server(entries))])
    rpc._client = httpx.Client(transport=n.transport())
    h = HistoryIndex()
    h.sync(rpc, BANK, 110000000, NOW)
    assert h.is_paid(7)
    h.sync(rpc, BANK, 110000001, NOW)
    assert n.calls["bad"] == 1, "a benched node is not asked again"


def test_chain_error_is_not_retried():
    n = Nodes()
    rpc = steemtx.RPC([n.add("a", rpc_error("missing required active authority")), n.add("b", rpc_error("x"))])
    rpc._client = httpx.Client(transport=n.transport())
    with pytest.raises(steemtx.ChainError):
        rpc.call("condenser_api.broadcast_transaction_synchronous", [{}])
    assert n.calls["b"] == 0


def test_scan_blocks_rebuilds_history():
    payout = [["transfer", {"from": BANK, "to": "rcpt", "amount": "1.000 STEEM", "memo": ""}], ["custom_json", _link(42)]]
    txs = {
        950: [payout],
        960: [[["account_update", {"account": BANK, "owner": {"weight_threshold": 1}}]]],
        970: [[["transfer", {"from": "alice", "to": "bob", "amount": "1.000 STEEM", "memo": "svm.bank"}]]],
        980: [[["custom_json", _link(43, auth="mallory")]]],  # not signed by svm.bank: not a payout
    }
    n = Nodes()
    rpc = steemtx.RPC([n.add("a", block_server(1000, txs)), n.add("b", block_server(1000, txs))], round_robin=True)
    rpc._client = httpx.Client(transport=n.transport())
    h = HistoryIndex()
    assert not h.scan_blocks(rpc, BANK, 1000, 300, NOW)
    assert h.scanned_block == 700 + BLOCK_SCAN_BATCH - 1
    assert h.scan_blocks(rpc, BANK, 1000, 300, NOW)
    assert h.scanned_block == 1000
    paid_trx = f"{950:08x}" + "0" * 32
    assert h.paid == {"42": paid_trx} and paid_trx in h.executed and h.last_owner_change == ts(NOW)
    assert min(n.calls.values()) >= 100, "round-robin spreads the blocks"
    # A block no node has yet stops the scan there; progress is kept.
    with pytest.raises(Exception, match="no block 1001"):
        h.scan_blocks(rpc, BANK, 1002, 300, NOW)
    assert h.scanned_block == 1000


def test_block_lookback():
    assert block_lookback(50400) == (50400 * 10 + 3600) // 3
    assert block_lookback(1) == (96 * 3600 + 3600) // 3
