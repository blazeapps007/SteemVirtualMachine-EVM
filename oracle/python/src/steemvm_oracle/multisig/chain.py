"""Steem reads/broadcasts for the signer, the withdrawal memo format, and the
persisted svm.bank history index. Port of ``oracle/go/multisig/chain.go``.
"""

from __future__ import annotations

import json
import os
import re
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field
from datetime import datetime, timedelta
from typing import Any, Optional

from . import steemtx

HISTORY_FILE = "svm_bank_history.json"
# Bounds the executed-trx set: proposals live at most an hour.
KEEP_EXECUTED = timedelta(hours=96)
ZERO_TX_ID = "0" * 40

# The history is complete up to the node's head; a minute of margin covers a
# node a few blocks behind (a later block scan re-reads those blocks).
HISTORY_HEAD_MARGIN = 20
# The most Steem blocks one scan_blocks call reads, and how many it fetches at
# once (spread round-robin over the nodes).
BLOCK_SCAN_BATCH = 200
BLOCK_SCAN_WORKERS = 4


def block_lookback(withdrawal_timeout_svm_blocks: int) -> int:
    """How far back (in 3s Steem blocks) a fresh index rebuilt from blocks
    starts: far enough to see the payout of any withdrawal still awaiting one
    (created at most withdrawal_timeout SVM blocks ago, counted at a
    conservative 10s each), every executed transaction the index keeps
    (KEEP_EXECUTED) and the owner cooldown, plus an hour."""
    seconds = max(withdrawal_timeout_svm_blocks * 10, int(KEEP_EXECUTED.total_seconds())) + 3600
    return seconds // 3


def _no_history(page: Any) -> Optional[str]:
    # Every account's history holds at least its creation, so an empty page
    # means the node has no account history (steemd.steemworld.org answers []
    # instead of an error). Trusting it would hide past payouts.
    return None if page else "empty account history (node has no account_history API)"


@dataclass
class Account:
    name: str
    owner: steemtx.Authority = field(default_factory=steemtx.Authority)
    active: steemtx.Authority = field(default_factory=steemtx.Authority)
    posting: steemtx.Authority = field(default_factory=steemtx.Authority)
    memo_key: str = ""
    json_metadata: str = ""
    posting_json_metadata: str = ""
    balance: str = ""
    sbd_balance: str = ""

    @staticmethod
    def from_json(obj: dict) -> "Account":
        def auth(key: str) -> steemtx.Authority:
            v = obj.get(key)
            return steemtx.Authority() if v is None else steemtx.Authority.from_json(v)

        return Account(
            name=str(obj.get("name", "")),
            owner=auth("owner"),
            active=auth("active"),
            posting=auth("posting"),
            memo_key=str(obj.get("memo_key", "") or ""),
            json_metadata=str(obj.get("json_metadata", "") or ""),
            posting_json_metadata=str(obj.get("posting_json_metadata", "") or ""),
            balance=str(obj.get("balance", "") or ""),
            sbd_balance=str(obj.get("sbd_balance", "") or ""),
        )


def get_accounts(rpc: Any, names: list[str]) -> list[Account]:
    return [Account.from_json(a) for a in rpc.call("condenser_api.get_accounts", [names]) or []]


def get_dynamic_props(rpc: Any) -> steemtx.DynamicProps:
    return steemtx.DynamicProps.from_json(rpc.call("condenser_api.get_dynamic_global_properties", []))


def broadcast(rpc: Any, tx: steemtx.Transaction) -> bool:
    """Broadcasts synchronously. Returns True if it was already on chain (a
    "Duplicate transaction" rejection is not an error)."""
    try:
        rpc.call("condenser_api.broadcast_transaction_synchronous", [tx.to_json()])
    except steemtx.ChainError as exc:
        if "Duplicate transaction" in str(exc):
            return True
        raise
    return False


# A withdrawal payout is one Steem transaction with two operations, in this
# order: the transfer, carrying the memo the user gave when bridging out, and a
# custom_json naming the withdrawal. Only svm.bank's active authority can sign
# that custom_json, so it can't be forged, and the memo means nothing to the
# bridge: a user can put anything there, including "svm-withdrawal 5", without
# affecting any withdrawal.
PAYOUT_LINK_ID = "svm-withdrawal"
_PAYOUT_LINK_RE = re.compile(r'\{"withdrawal_id":"(0|[1-9][0-9]*)"\}')


def payout_link_json(wid: int) -> str:
    """The exact custom_json body naming a withdrawal."""
    return '{"withdrawal_id":"' + str(wid) + '"}'


def parse_payout_link(c: steemtx.CustomJSON, bank: str) -> Optional[int]:
    """The withdrawal a custom_json names, if it is exactly a payout link signed
    by the bank's active authority alone."""
    if c.id != PAYOUT_LINK_ID or tuple(c.required_auths) != (bank,) or c.required_posting_auths:
        return None
    m = _PAYOUT_LINK_RE.fullmatch(c.json)
    if m is None:
        return None
    wid = int(m.group(1))
    return wid if wid < 1 << 64 else None


def payout_ops(bank: str, to: str, amount: str, memo: str, wid: int) -> list[steemtx.Operation]:
    """The two operations of a withdrawal payout."""
    return [
        steemtx.Operation("transfer", steemtx.Transfer(bank, to, amount, memo)),
        steemtx.Operation("custom_json", steemtx.CustomJSON((bank,), (), PAYOUT_LINK_ID, payout_link_json(wid))),
    ]


def payout_of(tx: steemtx.Transaction, bank: str) -> Optional[tuple[steemtx.Transfer, int]]:
    """A transaction's transfer and the withdrawal it pays, if the transaction
    has exactly the shape of a payout."""
    if len(tx.operations) != 2:
        return None
    t, c = tx.operations[0].value, tx.operations[1].value
    if not isinstance(t, steemtx.Transfer) or not isinstance(c, steemtx.CustomJSON):
        return None
    wid = parse_payout_link(c, bank)
    return None if wid is None else (t, wid)


@dataclass
class HistoryIndex:
    """A persisted, rebuildable index of svm.bank's account history: paid
    withdrawals, executed transaction IDs and the last owner change. A cache of
    public chain data -- deleting the file just triggers a full rescan. The
    JSON shape is shared with the Go and JS clients (same state volume)."""

    next_seq: int = 0
    paid: dict[str, str] = field(default_factory=dict)
    executed: dict[str, str] = field(default_factory=dict)
    last_owner_change: str = ""
    scanned_block: int = 0  # every svm.bank op in Steem blocks <= this is applied

    @staticmethod
    def load(state_dir: str) -> "HistoryIndex":
        try:
            with open(os.path.join(state_dir, HISTORY_FILE), encoding="utf-8") as f:
                d = json.load(f)
            return HistoryIndex(int(d.get("next_seq", 0)), dict(d.get("paid") or {}), dict(d.get("executed") or {}),
                                str(d.get("last_owner_change", "") or ""), int(d.get("scanned_block", 0) or 0))
        except FileNotFoundError:
            return HistoryIndex()
        except (ValueError, TypeError, AttributeError):
            return HistoryIndex()  # a corrupt cache is rebuilt, not trusted

    def save(self, state_dir: str) -> None:
        _atomic_write(os.path.join(state_dir, HISTORY_FILE), {
            "next_seq": self.next_seq, "paid": self.paid, "executed": self.executed,
            "last_owner_change": self.last_owner_change, "scanned_block": self.scanned_block,
        })

    def is_paid(self, wid: int) -> bool:
        return str(wid) in self.paid

    def _apply_page(self, account: str, page: list) -> bool:
        """Applies the entries at or after next_seq; reports progress."""
        advanced = False
        for seq, entry in page:
            seq = int(seq)
            if seq < self.next_seq:
                continue
            self.apply(account, entry)
            advanced = True
            self.next_seq = max(self.next_seq, seq + 1)
        return advanced

    def sync(self, rpc: Any, account: str, head_block: int, now: datetime) -> None:
        """Brings the index up to the newest entry. The node returns sequences
        [start-limit, start] and asserts start >= limit. The newest window is
        requested with the head block number as start (clamped to the newest
        entry; a start that changes every block can't be answered from a stale
        cache -- multisigguide.md SS4.1). A gap behind it is paged forward with
        start = next_seq+100, i.e. [next_seq, next_seq+100] -- immutable history."""
        call = lambda start: rpc.call("condenser_api.get_account_history", [account, start, 100], check=_no_history)  # noqa: E731
        newest = call(max(head_block, self.next_seq + 100))
        if newest:
            oldest = min(int(seq) for seq, _ in newest)
            while self.next_seq < oldest:
                if not self._apply_page(account, call(self.next_seq + 100)):
                    break
            self._apply_page(account, newest)
        self.prune_executed(now)
        if head_block > HISTORY_HEAD_MARGIN:
            self.scanned_block = max(self.scanned_block, head_block - HISTORY_HEAD_MARGIN)

    def scan_blocks(self, rpc: Any, account: str, head_block: int, lookback: int, now: datetime) -> bool:
        """The fallback for when no node serves svm.bank's account history
        (multisigguide.md SS4.1): reads raw blocks from scanned_block+1 (or from
        head_block-lookback when the index is fresh or older than that), at
        most BLOCK_SCAN_BATCH per call, and applies every operation that names
        account -- the same entries get_account_history would list. Returns
        whether the index now covers head_block; until it does, the signer
        must not act on the index."""
        start = self.scanned_block + 1
        if head_block > lookback and start < head_block - lookback:
            start = head_block - lookback
        if start > head_block:
            return True
        end = min(head_block, start + BLOCK_SCAN_BATCH - 1)

        def fetch(num: int) -> Any:
            return rpc.call("condenser_api.get_block", [num], check=lambda b: None if b else f"node has no block {num}")

        error: Optional[BaseException] = None
        with ThreadPoolExecutor(max_workers=BLOCK_SCAN_WORKERS) as pool:
            futures = [pool.submit(fetch, num) for num in range(start, end + 1)]
            for num, fut in zip(range(start, end + 1), futures):
                try:
                    block = fut.result()
                except Exception as exc:  # noqa: BLE001 - a gap stops the scan; the rest is fetched next time
                    error = exc
                    break
                self.apply_block(account, block)
                self.scanned_block = num
        self.prune_executed(now)
        if error is not None and self.scanned_block < start:
            raise error
        return self.scanned_block >= head_block

    def apply_block(self, account: str, block: dict) -> None:
        """Applies every operation in block whose body names account (a quoted
        \"svm.bank\" anywhere: transfer from/to, custom_json auths,
        account_update). A false match is harmless: apply only records payout
        links signed by account and owner changes of account."""
        needle = json.dumps(account)
        ids = block.get("transaction_ids") or []
        for i, tx in enumerate(block.get("transactions") or []):
            trx = tx.get("transaction_id") or (ids[i] if i < len(ids) else "")
            for op in tx.get("operations") or []:
                if isinstance(op, list) and len(op) == 2 and needle in json.dumps(op[1]):
                    self.apply(account, {"trx_id": trx, "timestamp": block.get("timestamp", ""), "op": op})

    def apply(self, account: str, e: dict) -> None:
        trx = e.get("trx_id", "")
        if trx and trx != ZERO_TX_ID:
            self.executed[trx] = e.get("timestamp", "")
        op = e.get("op") or []
        if len(op) != 2 or not isinstance(op[1], dict):
            return
        op_type, body = op
        if op_type == "custom_json":
            try:
                value = steemtx.Operation.from_json(op).value
            except steemtx.SteemTxError:
                return
            wid = parse_payout_link(value, account)
            if wid is not None:
                self.paid[str(wid)] = trx
        elif op_type in ("account_update", "account_update2"):
            if body.get("account") == account and body.get("owner") is not None:
                self.last_owner_change = e.get("timestamp", "")

    def prune_executed(self, now: datetime) -> None:
        cut = now - KEEP_EXECUTED
        for trx in [k for k, ts in self.executed.items() if _parsed_before(ts, cut)]:
            del self.executed[trx]


def _parsed_before(ts: str, cut: datetime) -> bool:
    try:
        return steemtx.parse_time(ts) < cut
    except ValueError:
        return False


def _atomic_write(path: str, obj: Any) -> None:
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(obj, f, separators=(",", ":"))
        f.flush()
        os.fsync(f.fileno())
    os.replace(tmp, path)
