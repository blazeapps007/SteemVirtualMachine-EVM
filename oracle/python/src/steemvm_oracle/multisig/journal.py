"""This signer's local, write-ahead record of every approval it has signed.
Saved before an approval is published and consulted by every review, so "at
most one live approval per withdrawal" holds even when a lagging Steem node
serves stale metadata. Port of ``oracle/go/multisig/journal.go``; the file
format is shared with the Go and JS clients.
"""

from __future__ import annotations

import json
import os
from dataclasses import dataclass, field
from datetime import datetime
from typing import Optional

from . import steemtx
from .ballot import PRUNE_AFTER
from .chain import _atomic_write

JOURNAL_FILE = "svm_bank_approvals.json"


def subject_of(tx: steemtx.Transaction) -> str:
    """What a single-operation proposal acts on: two proposals with the same
    subject must never both be approved while either can still execute."""
    if len(tx.operations) != 1:
        return ""
    v = tx.operations[0].value
    if isinstance(v, steemtx.Transfer):
        return "transfer:" + v.memo
    if isinstance(v, steemtx.AccountUpdate):
        return "account_update:" + v.account
    return ""


class CorruptJournal(RuntimeError):
    pass


@dataclass
class ApprovalJournal:
    approvals: dict[str, dict] = field(default_factory=dict)  # proposal ID -> {subject, expiration}

    @staticmethod
    def load(state_dir: str) -> "ApprovalJournal":
        """Unlike the history index this is not rebuildable from the chain, so a
        corrupt file stops the signer."""
        path = os.path.join(state_dir, JOURNAL_FILE)
        try:
            with open(path, encoding="utf-8") as f:
                d = json.load(f)
        except FileNotFoundError:
            return ApprovalJournal()
        except ValueError as exc:
            raise CorruptJournal(f"{path} is corrupt (it is safe to delete once the signer has been stopped "
                                 f"for over an hour): {exc}") from exc
        approvals = d.get("approvals") if isinstance(d, dict) else None
        if approvals is None:
            approvals = {}
        if not isinstance(approvals, dict):
            raise CorruptJournal(f"{path} is corrupt")
        return ApprovalJournal(dict(approvals))

    def save(self, state_dir: str) -> None:
        _atomic_write(os.path.join(state_dir, JOURNAL_FILE), {"approvals": self.approvals})

    def record(self, pid: str, tx: steemtx.Transaction) -> None:
        self.approvals[pid] = {"subject": subject_of(tx), "expiration": tx.expiration}

    def conflicts(self, subject: str, pid: str, now: datetime) -> bool:
        """Whether a different proposal with the same subject was approved and
        can still execute at Steem head time now. An unparseable expiration
        counts as a conflict."""
        if not subject:
            return False
        for other, e in self.approvals.items():
            if other == pid or e.get("subject") != subject:
                continue
            try:
                if now < steemtx.parse_time(e.get("expiration", "")):
                    return True
            except ValueError:
                return True
        return False

    def prune(self, now: datetime) -> None:
        cut = now - PRUNE_AFTER
        for pid in list(self.approvals):
            try:
                if steemtx.parse_time(self.approvals[pid].get("expiration", "")) < cut:
                    del self.approvals[pid]
            except ValueError:
                pass


def journal_conflicts(j: Optional[ApprovalJournal], subject: str, pid: str, now: datetime) -> bool:
    return j is not None and j.conflicts(subject, pid, now)
