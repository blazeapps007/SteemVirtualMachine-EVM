"""The deterministic, verify-independently policy: what to approve, deny or
leave for a human, and what to propose. Port of ``oracle/go/multisig/policy.go``.

Decisions: a deny vote is final, so DENY is only for content that is wrong and
can never become right; anything that may clear later (a read failed, another
proposal still live) is ABSTAIN and gets re-reviewed next pass.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from datetime import datetime, timedelta
from typing import Any, Optional, Protocol

from . import steemtx
from .apportion import StakeEntry, apportion, exceeds_tolerance
from .ballot import Vote
from .chain import parse_withdrawal_memo, withdrawal_memo
from .journal import ApprovalJournal, journal_conflicts, subject_of
from .view import EXECUTED, EXPIRED, Proposal, View

ABSTAIN, APPROVE, DENY = "abstain", "approve", "deny"

# ASCII digits only (Python's \d also matches other scripts' digits; Go's doesn't).
_SNAPSHOT_RE = re.compile(r"@svm:([0-9]+)")


@dataclass
class Withdrawal:
    """The SVM bridge-out record a payout must match."""

    id: int
    destination: str
    amount_milli: int  # NET payout (gross minus the bridge fee)
    symbol: str  # "STEEM" or "SBD"
    created_at: int  # SVM height
    status: str  # "REQUESTED", "PROCESSED", "REFUNDED"


class SVM(Protocol):
    def height(self) -> int: ...
    def withdrawal_timeout_blocks(self) -> int: ...
    def requested_withdrawals(self) -> list[Withdrawal]: ...
    def withdrawal(self, wid: int) -> Optional[Withdrawal]: ...
    def validator_stakes(self, height: int) -> list[StakeEntry]: ...
    def self_steem_account(self) -> str: ...


@dataclass
class Config:
    """Every validator should run identical values for the rules that must
    agree across signers (threshold, tolerance, snapshot spacing and age)."""

    bank_account: str = "svm.bank"
    threshold_percent: int = 75
    reweight_tolerance: int = 50
    payout_margin_blocks: int = 14400
    snapshot_every: int = 1000
    snapshot_max_age: int = 3000
    snapshot_lag: int = 10
    owner_cooldown: timedelta = timedelta(hours=1)
    fallback_delay: timedelta = timedelta(seconds=120)
    max_per_tx_milli: dict[str, int] = field(default_factory=dict)


@dataclass
class SVMState:
    height: int
    timeout: int
    requested: dict[int, Withdrawal]


@dataclass
class Intent:
    key: str
    title: str
    ops: list[steemtx.Operation]


def _single_transfer(p: Proposal) -> Optional[steemtx.Transfer]:
    if len(p.tx.operations) != 1:
        return None
    v = p.tx.operations[0].value
    return v if isinstance(v, steemtx.Transfer) else None


def _symbol_of(amount: str) -> str:
    try:
        return steemtx.parse_asset(amount).symbol
    except ValueError:
        return ""


def rank_of(members: list[str], me: str, designated: int) -> int:
    """Distance (in chain order) from the designated proposer index; -1 when
    me is not a member."""
    n = len(members)
    for i, m in enumerate(members):
        if m == me:
            return (i - designated) % n
    return -1


class Policy:
    def __init__(self, cfg: Config, svm: Any, me: str, journal: Optional[ApprovalJournal] = None):
        self.cfg, self.svm, self.me, self.journal = cfg, svm, me, journal
        self._cache: dict[int, steemtx.Authority] = {}

    def desired_at(self, height: int) -> steemtx.Authority:
        if height not in self._cache:
            a = apportion(self.svm.validator_stakes(height), self.cfg.threshold_percent)
            if len(self._cache) > 16:
                self._cache.clear()
            self._cache[height] = a
        return self._cache[height]

    def checkpoint(self, tip: int) -> int:
        s = tip - self.cfg.snapshot_lag
        return 0 if s <= 0 else s - s % self.cfg.snapshot_every

    def review(self, p: Proposal, v: View, st: SVMState, my_votes: dict[str, Vote]) -> tuple[str, str]:
        if len(p.tx.operations) != 1:
            return DENY, "more than one operation"
        op = p.tx.operations[0].value
        if isinstance(op, steemtx.Transfer):
            return self._review_transfer(p, op, v, st, my_votes)
        if isinstance(op, steemtx.AccountUpdate):
            return self._review_account_update(p, op, v, st, my_votes)
        if isinstance(op, (steemtx.TransferToVesting, steemtx.WithdrawVesting, steemtx.ChangeRecoveryAccount)):
            return ABSTAIN, "treasury/recovery operation: needs a human"
        return DENY, "operation type not allowed: " + p.tx.operations[0].type

    def payout_facts(self, t: steemtx.Transfer, v: View, st: SVMState) -> tuple[Optional[int], str, str]:
        """(withdrawal id, APPROVE if a correct payout / DENY if wrong forever /
        ABSTAIN if it can't be judged yet, reason)."""
        if t.from_ != self.cfg.bank_account:
            return None, DENY, "not from " + self.cfg.bank_account
        wid = parse_withdrawal_memo(t.memo)
        if wid is None:
            return None, DENY, 'memo is not exactly "svm-withdrawal <id>"'
        if v.history.is_paid(wid):
            return wid, DENY, "withdrawal already paid on Steem"
        w = st.requested.get(wid)
        if w is None:
            try:
                got = self.svm.withdrawal(wid)
            except Exception as exc:  # noqa: BLE001 - transient: look again next pass
                return wid, ABSTAIN, f"cannot read withdrawal: {exc}"
            if got is None:
                return wid, ABSTAIN, "SVM withdrawal not found (yet)"
            if got.status != "REQUESTED":
                return wid, DENY, "SVM withdrawal is " + got.status
            w = got
        if t.to != w.destination:
            return wid, DENY, "recipient differs from the SVM withdrawal destination"
        if t.amount != steemtx.format_milli(w.amount_milli, w.symbol):
            return wid, DENY, "amount differs from the SVM withdrawal net amount"
        if st.height + self.cfg.payout_margin_blocks > w.created_at + st.timeout:
            return wid, DENY, "too close to the automatic SVM refund"
        return wid, APPROVE, ""

    def _review_transfer(self, p: Proposal, t: steemtx.Transfer, v: View, st: SVMState,
                         my_votes: dict[str, Vote]) -> tuple[str, str]:
        wid, d, reason = self.payout_facts(t, v, st)
        if d != APPROVE:
            return d, reason
        cap = self.cfg.max_per_tx_milli.get(_symbol_of(t.amount), 0)
        if cap > 0 and steemtx.parse_asset(t.amount).amount > cap:
            return ABSTAIN, "over the configured per-transaction cap"
        # One payout per withdrawal: never sign a second proposal for the same
        # withdrawal while one this signer approved can still execute, and
        # approve only the earliest correct live one.
        if journal_conflicts(self.journal, subject_of(p.tx), p.id, v.now):
            return ABSTAIN, "already approved another unexpired proposal for this withdrawal"
        earliest = ""
        for q in v.proposals:  # sorted by (created, id)
            qt = _single_transfer(q)
            if qt is None or qt.memo != t.memo:
                continue
            # Not just live ones: a "rejected" proposal can still execute if a
            # member flips a deny to an approval before it expires.
            if q.id != p.id and q.status != EXPIRED:
                mv = my_votes.get(q.id)
                if mv is not None and mv.a == 1:
                    return ABSTAIN, "already approved another unexpired proposal for this withdrawal"
            if not earliest and q.live() and self.payout_facts(qt, v, st)[1] == APPROVE:
                earliest = q.id
        if earliest != p.id:
            return ABSTAIN, f"an earlier correct proposal exists for withdrawal {wid}"
        return APPROVE, f"pays SVM withdrawal {wid} exactly"

    def _review_account_update(self, p: Proposal, u: steemtx.AccountUpdate, v: View, st: SVMState,
                               my_votes: dict[str, Vote]) -> tuple[str, str]:
        if u.account != self.cfg.bank_account:
            return DENY, "not " + self.cfg.bank_account
        if u.owner is None or u.active is None or u.posting is None:
            return DENY, "must set owner, active and posting together"
        m = _SNAPSHOT_RE.search(p.title)
        if m is None:
            return DENY, "title has no @svm:<height> snapshot"
        s = int(m.group(1))
        if s <= 0 or s >= 1 << 63 or s % self.cfg.snapshot_every != 0 or st.height - s > self.cfg.snapshot_max_age:
            return DENY, "snapshot is not a recent checkpoint"
        if s > st.height:
            return ABSTAIN, "this signer's SVM node has not reached the snapshot yet"
        try:
            desired = self.desired_at(s)
        except Exception as exc:  # noqa: BLE001 - e.g. a pruned height: someone else decides
            return ABSTAIN, f"cannot compute the stake-proportional set: {exc}"
        if not (u.owner.equal(desired) and u.active.equal(desired) and u.posting.equal(desired)):
            return DENY, f"authority differs from the stake-proportional set at @svm:{s}"
        if u.memo_key != v.bank.memo_key:
            return DENY, "memo key changed"
        if u.json_metadata and u.json_metadata != v.bank.json_metadata:
            return DENY, "json_metadata changed"
        if not exceeds_tolerance(v.bank.owner, desired, self.cfg.reweight_tolerance):
            return DENY, "change is within the reweight tolerance"
        if journal_conflicts(self.journal, subject_of(p.tx), p.id, v.now):
            return ABSTAIN, "already approved another unexpired validator-set proposal"
        for q in v.proposals:
            if q.id == p.id or q.status in (EXPIRED, EXECUTED):
                continue
            if isinstance(q.tx.operations[0].value, steemtx.AccountUpdate):
                mv = my_votes.get(q.id)
                if mv is not None and mv.a == 1:
                    return ABSTAIN, "already approved another unexpired validator-set proposal"
        return APPROVE, f"equals the stake-proportional set at @svm:{s}"

    def intents(self, v: View, st: SVMState, first_seen: dict[str, datetime],
                now: datetime) -> tuple[list[Intent], Optional[str]]:
        """The payouts and validator-set change to propose now, plus an error
        (the payouts are still returned when only the validator-set part
        failed). first_seen tracks (locally) when each became due, so
        non-designated members wait rank * fallback_delay before stepping in."""
        members = v.sorted_members()
        n = len(members)
        if n == 0 or rank_of(members, self.me, 0) < 0:
            return [], None

        def due(key: str, designated: int) -> bool:
            first_seen.setdefault(key, now)
            rank = rank_of(members, self.me, designated)
            return rank == 0 or now - first_seen[key] >= rank * self.cfg.fallback_delay

        live: set[str] = set()
        live_update = False
        for q in v.proposals:
            if not q.live():
                continue
            qt = _single_transfer(q)
            if qt is not None:
                live.add(qt.memo)
                # Restart the fallback clock while a proposal is live.
                wid = parse_withdrawal_memo(qt.memo)
                if wid is not None:
                    first_seen.pop(f"w:{wid}", None)
            if len(q.tx.operations) == 1 and isinstance(q.tx.operations[0].value, steemtx.AccountUpdate):
                live_update = True

        balance: dict[str, int] = {}
        for s in (v.bank.balance, v.bank.sbd_balance):
            try:
                a = steemtx.parse_asset(s)
                balance[a.symbol] = a.amount
            except ValueError:
                pass

        out: list[Intent] = []
        for wid in sorted(st.requested):
            w = st.requested[wid]
            memo = withdrawal_memo(wid)
            if memo in live or v.history.is_paid(wid) or journal_conflicts(self.journal, "transfer:" + memo, "", v.now):
                continue
            if st.height + self.cfg.payout_margin_blocks > w.created_at + st.timeout:
                continue  # the automatic SVM refund will handle it
            cap = self.cfg.max_per_tx_milli.get(w.symbol, 0)
            if cap > 0 and w.amount_milli > cap:
                continue
            if balance.get(w.symbol, 0) < w.amount_milli:
                continue
            key = f"w:{wid}"
            if not due(key, wid % n):
                continue
            balance[w.symbol] -= w.amount_milli
            amount = steemtx.format_milli(w.amount_milli, w.symbol)
            out.append(Intent(key, f"Withdrawal {wid}: {amount} to @{w.destination}", [steemtx.Operation(
                "transfer", steemtx.Transfer(self.cfg.bank_account, w.destination, amount, memo))]))

        if not live_update and not journal_conflicts(self.journal, "account_update:" + self.cfg.bank_account, "", v.now):
            cooled = True
            if v.history.last_owner_change:
                try:
                    if v.now - steemtx.parse_time(v.history.last_owner_change) < self.cfg.owner_cooldown:
                        cooled = False
                except ValueError:
                    pass
            s = self.checkpoint(st.height)
            if cooled and s > 0:
                try:
                    desired = self.desired_at(s)
                except Exception as exc:  # noqa: BLE001 - keep the payouts; report the error
                    return out, f"computing the stake-proportional set at @svm:{s}: {exc}"
                if exceeds_tolerance(v.bank.owner, desired, self.cfg.reweight_tolerance):
                    key = f"v:{s}"
                    if due(key, (s // self.cfg.snapshot_every) % n):
                        out.append(Intent(key, f"Validator set @svm:{s}", [steemtx.Operation(
                            "account_update", steemtx.AccountUpdate(self.cfg.bank_account, desired, desired, desired,
                                                                    v.bank.memo_key, ""))]))
        return out, None
