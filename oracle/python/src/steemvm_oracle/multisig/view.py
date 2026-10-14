"""Verified proposals and tallies derived identically by every client
(``oracle/multisigguide.md`` section 2.5). Port of ``oracle/go/multisig/view.go``.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime
from typing import Optional

from . import steemtx
from .ballot import Ballot
from .chain import Account, HistoryIndex

OPEN, READY, REJECTED, EXPIRED, EXECUTED = "open", "ready", "rejected", "expired", "executed"


@dataclass
class VoteInfo:
    approve: bool
    sig: str
    valid: bool  # approvals: recovers to a key that satisfies the voter's active authority


@dataclass
class Proposal:
    id: str
    creator: str
    title: str
    created: str
    tx: steemtx.Transaction
    digest: bytes
    expiration: datetime
    owner: bool  # needs svm.bank's owner authority (else active)
    votes: dict[str, VoteInfo] = field(default_factory=dict)
    approve: int = 0
    deny: int = 0
    status: str = OPEN

    def live(self) -> bool:
        return self.status in (OPEN, READY)


@dataclass
class View:
    now: datetime  # Steem head block time
    props: Optional[steemtx.DynamicProps]
    bank: Account
    members: dict[str, Account]
    history: HistoryIndex
    proposals: list[Proposal] = field(default_factory=list)  # sorted by (created, id)

    def authority(self, p: Proposal) -> steemtx.Authority:
        return self.bank.owner if p.owner else self.bank.active

    def is_member(self, name: str) -> bool:
        return self.bank.owner.weight_of(name) > 0

    def sorted_members(self) -> list[str]:
        return [n for n, _ in self.bank.owner.sorted().account_auths]

    def minimal_signatures(self, p: Proposal) -> Optional[list[str]]:
        """Walks the authority in chain order collecting valid approval
        signatures until the threshold; Steem rejects unnecessary signatures,
        so it stops exactly there. None if the threshold isn't reachable."""
        auth = self.authority(p).sorted()
        sigs: list[str] = []
        w = 0
        for name, weight in auth.account_auths:
            if w >= auth.weight_threshold:
                break
            vi = p.votes.get(name)
            if vi is not None and vi.approve and vi.valid:
                sigs.append(vi.sig)
                w += weight
        return sigs if w >= auth.weight_threshold else None


def needs_owner(tx: steemtx.Transaction) -> bool:
    for op in tx.operations:
        v = op.value
        if isinstance(v, (steemtx.AccountUpdate, steemtx.AccountUpdate2)) and v.owner is not None:
            return True
        if isinstance(v, steemtx.ChangeRecoveryAccount):
            return True
    return False


def targets_bank(tx: steemtx.Transaction, bank: str) -> bool:
    if not tx.operations:
        return False
    for op in tx.operations:
        v = op.value
        if isinstance(v, (steemtx.Transfer, steemtx.TransferToVesting)):
            acct = v.from_
        elif isinstance(v, (steemtx.WithdrawVesting, steemtx.AccountUpdate, steemtx.AccountUpdate2)):
            acct = v.account
        elif isinstance(v, steemtx.ChangeRecoveryAccount):
            acct = v.account_to_recover
        else:
            acct = ""
        if acct != bank:
            return False
    return True


def key_satisfies(active: steemtx.Authority, pub: str) -> bool:
    """Whether a single key meets an active authority."""
    return any(k == pub and w >= active.weight_threshold for k, w in active.key_auths)


def build_view(now: datetime, props: Optional[steemtx.DynamicProps], bank: Account, members: dict[str, Account],
               ballots: dict[str, Ballot], hist: HistoryIndex) -> View:
    """Verifies and tallies every proposal published by current members.
    ballots maps member name -> parsed ballot (unreadable metadata: absent)."""
    v = View(now, props, bank, members, hist)
    by_id: dict[str, Proposal] = {}
    creators = sorted(ballots)
    for creator in creators:
        if not v.is_member(creator):
            continue  # only proposals from svm.bank's current owner authority
        for pid, sp in ballots[creator].props.items():
            try:
                if sp.tx.id() != pid:
                    continue  # never trust an ID that doesn't match the transaction
                if not targets_bank(sp.tx, bank.name):
                    continue
                digest = sp.tx.digest()
                exp = steemtx.parse_time(sp.tx.expiration)
            except (ValueError, OverflowError, Exception):  # noqa: BLE001 - unserializable: unverifiable
                continue
            p = Proposal(pid, creator, sp.t, sp.c, sp.tx, digest, exp, needs_owner(sp.tx))
            prev = by_id.get(pid)
            if prev is not None and (prev.created < p.created or (prev.created == p.created and prev.creator < p.creator)):
                continue  # the same transaction published twice: keep the earliest
            by_id[pid] = p

    for p in by_id.values():
        auth = v.authority(p)
        for voter in creators:
            w = auth.weight_of(voter)
            if w == 0:
                continue
            vote = ballots[voter].votes.get(p.id)
            if vote is None:
                continue
            vi = VoteInfo(vote.a == 1, vote.s, False)
            if vi.approve:
                try:
                    pub = steemtx.recover_signer(p.digest, vote.s)
                except ValueError:
                    pub = ""
                voter_acct = members.get(voter)
                if pub and voter_acct is not None and key_satisfies(voter_acct.active, pub):
                    vi.valid = True
                    p.approve += w
            else:
                vi.valid = True
                p.deny += w
            p.votes[voter] = vi
        total = auth.total()
        if hist is not None and hist.executed.get(p.id):
            p.status = EXECUTED
        elif not now < p.expiration:
            p.status = EXPIRED
        elif total >= auth.weight_threshold and p.deny > total - auth.weight_threshold:
            p.status = REJECTED
        elif p.approve >= auth.weight_threshold:
            p.status = READY
        else:
            p.status = OPEN
        v.proposals.append(p)
    v.proposals.sort(key=lambda q: (q.created, q.id))
    return v
