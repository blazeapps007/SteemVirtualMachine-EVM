"""Shared fixtures for the svm.bank multisig tests (mirrors
oracle/go/multisig/fixture_test.go and e2e_test.go): a 4-member bank, a fake
SVM, and a fake Steem node that enforces the chain rules that matter here."""

from __future__ import annotations

import copy
import hashlib
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from typing import Any, Optional

from steemvm_oracle.multisig import steemtx
from steemvm_oracle.multisig.apportion import StakeEntry
from steemvm_oracle.multisig.ballot import Ballot, StoredProposal, Vote
from steemvm_oracle.multisig.chain import Account, HistoryIndex
from steemvm_oracle.multisig.policy import Config, Policy, SVMState, Withdrawal
from steemvm_oracle.multisig.view import Proposal, View, build_view

BANK = "svm.bank"
NOW = datetime.now(timezone.utc).replace(microsecond=0)
WEIGHTS = [("alice", 4500), ("bob", 3000), ("carol", 1500), ("dave", 1000)]


def key(name: str) -> bytes:
    return hashlib.sha256(f"svm.bank test key {name}".encode()).digest()


def pub(name: str) -> str:
    return steemtx.public_key_of(key(name))


def wif(name: str) -> str:
    payload = b"\x80" + key(name)
    return steemtx.b58encode(payload + hashlib.sha256(hashlib.sha256(payload).digest()).digest()[:4])


def key_authority(p: str) -> steemtx.Authority:
    return steemtx.Authority(1, [], [(p, 1)])


def bank_authority(weights=WEIGHTS) -> steemtx.Authority:
    return steemtx.Authority(7500, list(weights), []).sorted()


def stakes(**kv: int) -> list[StakeEntry]:
    return [StakeEntry(k, v) for k, v in kv.items()]


def ts(t: datetime) -> str:
    return steemtx.format_time(t)


def transfer_op(to: str, amount: str, memo: str, frm: str = BANK) -> steemtx.Operation:
    return steemtx.Operation("transfer", steemtx.Transfer(frm, to, amount, memo))


@dataclass
class FakeSVM:
    me: str = ""
    height_: int = 100000
    timeout: int = 50400
    all: dict[int, Withdrawal] = field(default_factory=dict)
    stakes_at: dict[int, list[StakeEntry]] = field(default_factory=dict)
    err: Optional[Exception] = None

    def as_(self, me: str) -> "FakeSVMView":
        return FakeSVMView(self, me)

    def height(self) -> int:
        return self.height_

    def withdrawal_timeout_blocks(self) -> int:
        return self.timeout

    def requested_withdrawals(self) -> list[Withdrawal]:
        return [w for w in self.all.values() if w.status == "REQUESTED"]

    def withdrawal(self, wid: int) -> Optional[Withdrawal]:
        if self.err is not None:
            raise self.err
        return self.all.get(wid)

    def validator_stakes(self, height: int) -> list[StakeEntry]:
        if height not in self.stakes_at:
            raise RuntimeError("height pruned")
        return self.stakes_at[height]

    def self_steem_account(self) -> str:
        return self.me


class FakeSVMView:
    """One validator's view of a shared FakeSVM."""

    def __init__(self, data: FakeSVM, me: str):
        self.data, self.me = data, me

    def __getattr__(self, name: str) -> Any:
        return getattr(self.data, name)

    def self_steem_account(self) -> str:
        return self.me


class Fixture:
    """alice 4500, bob 3000, carol 1500, dave 1000 (threshold 7500, so
    alice+bob is exactly enough); SVM at 100000 with the 3.5-day timeout;
    stakes at the 99000 checkpoint reproduce the current weights."""

    def __init__(self) -> None:
        auth = bank_authority()
        self.bank = Account(BANK, auth, copy.deepcopy(auth), copy.deepcopy(auth), pub("memo"), "", "", "1000.000 STEEM", "50.000 SBD")
        self.members = {n: Account(n, active=key_authority(pub(n))) for n, _ in WEIGHTS}
        self.props = steemtx.DynamicProps(100000, "000186a0" + "ab" * 16, ts(NOW))
        self.now = NOW
        self.svm = FakeSVM(stakes_at={99000: stakes(alice=45, bob=30, carol=15, dave=10)})
        self.hist = HistoryIndex()
        self.ballots: dict[str, Ballot] = {n: Ballot() for n, _ in WEIGHTS}

    def withdrawal(self, wid: int, dest: str, milli: int, sym: str, created: int) -> None:
        self.svm.all[wid] = Withdrawal(wid, dest, milli, sym, created, "REQUESTED")

    def propose(self, creator: str, title: str, ops: list[steemtx.Operation], ttl: timedelta = timedelta(minutes=58),
                created: str = "") -> str:
        tx = steemtx.new_transaction(self.props, ops, ttl)
        pid = tx.id()
        self.ballots.setdefault(creator, Ballot()).props[pid] = StoredProposal(title, tx, created or self.props.time)
        return pid

    def stored(self, pid: str) -> StoredProposal:
        for b in self.ballots.values():
            if pid in b.props:
                return b.props[pid]
        raise KeyError(pid)

    def vote(self, voter: str, pid: str, approve: bool, signer: str = "") -> None:
        sp = self.stored(pid)
        if approve:
            self.ballots[voter].votes[pid] = Vote(1, steemtx.sign(key(signer or voter), sp.tx.digest()), sp.tx.expiration)
        else:
            self.ballots[voter].votes[pid] = Vote(0, "", sp.tx.expiration)

    def view(self) -> View:
        return build_view(self.now, self.props, self.bank, self.members, self.ballots, self.hist)

    def state(self) -> SVMState:
        return SVMState(self.svm.height_, self.svm.timeout, {w.id: w for w in self.svm.requested_withdrawals()})

    def policy(self, me: str) -> Policy:
        return Policy(Config(), self.svm, me)

    def find(self, v: View, pid: str) -> Proposal:
        for p in v.proposals:
            if p.id == pid:
                return p
        raise KeyError(pid)

    def review(self, pol: Policy, pid: str, my_votes: Optional[dict] = None) -> tuple[str, str]:
        v = self.view()
        return pol.review(self.find(v, pid), v, self.state(), my_votes or {})


# --------------------------------------------------------------------------
# Fake Steem node (in-process RPC)
# --------------------------------------------------------------------------


class FakeSteem:
    """Serves the condenser_api calls the signer uses and applies broadcasts
    under the chain's rules: signature authority (active satisfies posting), no
    unnecessary signatures, expiry, duplicates, one owner change per hour, and
    history paging that asserts start >= limit."""

    def __init__(self) -> None:
        self.head = 100000
        self.now = NOW
        self.accounts: dict[str, Account] = {}
        self.history: dict[str, list[dict]] = {}
        self.seen: set[str] = set()
        self.last_owner: dict[str, datetime] = {}
        self.transfers: list[steemtx.Transfer] = []
        self.rejected: list[str] = []
        self.starts: list[int] = []  # get_account_history start params, in order

    def advance(self, blocks: int = 1) -> None:
        self.head += blocks
        self.now += timedelta(seconds=3 * blocks)

    def add_history(self, account: str, trx: str, timestamp: str, op_type: str, body: dict) -> None:
        self.history.setdefault(account, []).append({"trx_id": trx, "timestamp": timestamp, "op": [op_type, body]})

    def call(self, method: str, params: list) -> Any:
        if method == "condenser_api.get_dynamic_global_properties":
            return {"head_block_number": self.head, "head_block_id": f"{self.head:08x}" + "cd" * 16, "time": ts(self.now)}
        if method == "condenser_api.get_accounts":
            return [_account_json(self.accounts[n]) for n in params[0] if n in self.accounts]
        if method == "condenser_api.get_account_history":
            account, start, limit = params
            self.starts.append(start)
            if start < limit:
                raise steemtx.ChainError("Assert Exception:args.start >= args.limit: start must be greater than limit")
            h = self.history.get(account, [])
            if not h:
                return []
            top = min(start, len(h) - 1)
            return [[seq, h[seq]] for seq in range(max(0, top - limit), top + 1)]
        if method == "condenser_api.broadcast_transaction_synchronous":
            try:
                return self.apply(steemtx.Transaction.from_json(params[0]))
            except steemtx.ChainError as exc:
                if "Duplicate transaction" not in str(exc):
                    self.rejected.append(str(exc))
                raise
        raise AssertionError(f"unexpected method {method}")

    def _satisfies(self, auth: steemtx.Authority, keys: set[str], used: set[str], depth: int = 0) -> bool:
        total = 0
        for k, w in auth.key_auths:
            if k in keys:
                used.add(k)
                total += w
                if total >= auth.weight_threshold:
                    return True
        if depth < 2:
            for name, w in auth.sorted().account_auths:
                acc = self.accounts.get(name)
                if acc is not None and self._satisfies(acc.active, keys, used, depth + 1):
                    total += w
                    if total >= auth.weight_threshold:
                        return True
        return total >= auth.weight_threshold

    def apply(self, tx: steemtx.Transaction) -> dict:
        tid = tx.id()
        if tid in self.seen:
            raise steemtx.ChainError("Duplicate transaction check failed")
        exp = steemtx.parse_time(tx.expiration)
        if not self.now < exp or exp - self.now > timedelta(hours=1):
            raise steemtx.ChainError("transaction expiration out of range")
        if len(tx.operations) != 1:
            raise steemtx.ChainError("test node applies single-operation transactions only")
        digest = tx.digest()
        keys: set[str] = set()
        for s in tx.signatures:
            p = steemtx.recover_signer(digest, s)
            if p in keys:
                raise steemtx.ChainError("duplicate signature")
            keys.add(p)
        used: set[str] = set()
        op = tx.operations[0].value
        if isinstance(op, steemtx.AccountUpdate2):
            acc, account = self.accounts.get(op.account), op.account
            ok = acc is not None and op.owner is None and (
                self._satisfies(acc.posting, keys, used) or self._satisfies(acc.active, keys, used)
                or self._satisfies(acc.owner, keys, used))
        elif isinstance(op, steemtx.Transfer):
            acc, account = self.accounts.get(op.from_), op.from_
            ok = acc is not None and self._satisfies(acc.active, keys, used)
        elif isinstance(op, steemtx.AccountUpdate):
            acc, account = self.accounts.get(op.account), op.account
            ok = acc is not None and self._satisfies(acc.owner if op.owner is not None else acc.active, keys, used)
        else:
            raise steemtx.ChainError("operation not supported by the test node")
        if not ok:
            raise steemtx.ChainError("Missing Authority")
        if used != keys:
            raise steemtx.ChainError("Unnecessary signature(s) detected")

        if isinstance(op, steemtx.AccountUpdate2):
            if op.posting_json_metadata:
                acc.posting_json_metadata = op.posting_json_metadata
        elif isinstance(op, steemtx.Transfer):
            self._move(op.from_, op.to, steemtx.parse_asset(op.amount))
            self.transfers.append(op)
        elif isinstance(op, steemtx.AccountUpdate):
            if op.owner is not None:
                last = self.last_owner.get(op.account)
                if last is not None and self.now - last < timedelta(hours=1):
                    raise steemtx.ChainError("Owner authority can only be updated once an hour")
                acc.owner, self.last_owner[op.account] = op.owner.sorted(), self.now
            if op.active is not None:
                acc.active = op.active.sorted()
            if op.posting is not None:
                acc.posting = op.posting.sorted()
            acc.memo_key = op.memo_key
        self.seen.add(tid)
        self.add_history(account, tid, ts(self.now), tx.operations[0].type, tx.operations[0].to_json()[1])
        return {"id": tid, "block_num": self.head + 1}

    def _move(self, frm: str, to: str, amt: steemtx.Asset) -> None:
        attr = "sbd_balance" if amt.symbol == "SBD" else "balance"
        src, dst = self.accounts[frm], self.accounts[to]
        have = steemtx.parse_asset(getattr(src, attr))
        if have.amount < amt.amount:
            raise steemtx.ChainError("insufficient funds")
        got = steemtx.parse_asset(getattr(dst, attr))
        setattr(src, attr, steemtx.format_milli(have.amount - amt.amount, amt.symbol))
        setattr(dst, attr, steemtx.format_milli(got.amount + amt.amount, amt.symbol))


def _account_json(a: Account) -> dict:
    return {"name": a.name, "owner": a.owner.to_json(), "active": a.active.to_json(), "posting": a.posting.to_json(),
            "memo_key": a.memo_key, "json_metadata": a.json_metadata, "posting_json_metadata": a.posting_json_metadata,
            "balance": a.balance, "sbd_balance": a.sbd_balance}
