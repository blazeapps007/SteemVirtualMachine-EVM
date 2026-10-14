"""Steem reads/broadcasts for the signer, the withdrawal memo format, and the
persisted svm.bank history index. Port of ``oracle/go/multisig/chain.go``.
"""

from __future__ import annotations

import json
import os
from dataclasses import dataclass, field
from datetime import datetime, timedelta
from typing import Any, Optional

from . import steemtx

HISTORY_FILE = "svm_bank_history.json"
# Bounds the executed-trx set: proposals live at most an hour.
KEEP_EXECUTED = timedelta(hours=96)
ZERO_TX_ID = "0" * 40


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


def withdrawal_memo(wid: int) -> str:
    """The exact memo a payout must carry: the format the bridge relays parse
    (parse_withdrawal_memo in steem_client.py and its Go/JS twins), so the
    payout is attested on SVM and the withdrawal becomes PROCESSED."""
    return f"svm-withdrawal {wid}"


def paid_memo_id(memo: str) -> Optional[int]:
    """Mirrors the bridge relays' lenient parse: anything the relay would attest
    as a payout of <id> also counts as "already paid" here."""
    f = memo.strip().split()
    if len(f) != 2 or f[0] != "svm-withdrawal" or not f[1].isascii() or not f[1].isdigit():
        return None
    wid = int(f[1])
    return wid if wid < 1 << 64 else None


def parse_withdrawal_memo(memo: str) -> Optional[int]:
    """Only the canonical "svm-withdrawal <id>" form (what the signer approves)."""
    if not memo.startswith("svm-withdrawal "):
        return None
    rest = memo[len("svm-withdrawal ") :]
    if not rest or not rest.isascii() or not rest.isdigit() or (rest.startswith("0") and rest != "0"):
        return None
    wid = int(rest)
    if wid >= 1 << 64 or withdrawal_memo(wid) != memo:
        return None
    return wid


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

    @staticmethod
    def load(state_dir: str) -> "HistoryIndex":
        try:
            with open(os.path.join(state_dir, HISTORY_FILE), encoding="utf-8") as f:
                d = json.load(f)
            return HistoryIndex(int(d.get("next_seq", 0)), dict(d.get("paid") or {}), dict(d.get("executed") or {}),
                                str(d.get("last_owner_change", "") or ""))
        except FileNotFoundError:
            return HistoryIndex()
        except (ValueError, TypeError, AttributeError):
            return HistoryIndex()  # a corrupt cache is rebuilt, not trusted

    def save(self, state_dir: str) -> None:
        _atomic_write(os.path.join(state_dir, HISTORY_FILE), {
            "next_seq": self.next_seq, "paid": self.paid, "executed": self.executed,
            "last_owner_change": self.last_owner_change,
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
        call = lambda start: rpc.call("condenser_api.get_account_history", [account, start, 100]) or []  # noqa: E731
        newest = call(max(head_block, self.next_seq + 100))
        if newest:
            oldest = min(int(seq) for seq, _ in newest)
            while self.next_seq < oldest:
                if not self._apply_page(account, call(self.next_seq + 100)):
                    break
            self._apply_page(account, newest)
        self.prune_executed(now)

    def apply(self, account: str, e: dict) -> None:
        trx = e.get("trx_id", "")
        if trx and trx != ZERO_TX_ID:
            self.executed[trx] = e.get("timestamp", "")
        op = e.get("op") or []
        if len(op) != 2 or not isinstance(op[1], dict):
            return
        op_type, body = op
        if op_type == "transfer":
            if body.get("from") == account and isinstance(body.get("memo"), str):
                wid = paid_memo_id(body["memo"])
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
