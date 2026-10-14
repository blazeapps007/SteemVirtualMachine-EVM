"""One validator's automated svm.bank signer: each pass loads the chain view,
votes on live proposals, proposes due payouts / validator-set syncs, publishes
everything in one ``account_update2`` signed with the ACTIVE key, then executes
proposals that reached the threshold. Port of ``oracle/go/multisig/signer.go``.
"""

from __future__ import annotations

import logging
import os
import threading
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from typing import Any, Mapping, Optional

from . import steemtx
from .ballot import Ballot, InvalidMetadata, MalformedBallot, StoredProposal, Vote, encode_metadata, parse_metadata
from .chain import HistoryIndex, broadcast, get_accounts, get_dynamic_props
from .journal import ApprovalJournal
from .policy import ABSTAIN, APPROVE, DENY, Config, Policy, SVMState, rank_of
from .vectors import selftest
from .view import READY, View, build_view, key_satisfies

logger = logging.getLogger("steemvm_oracle.multisig")

# Only the validator's ACTIVE key belongs on the server: svm.bank's
# owner/active/posting authorities resolve through validators' active keys, so
# an owner key, posting key or master password is never needed.
FORBIDDEN_KEY_VARS = (
    "ORACLE_STEEM_OWNER_WIF", "ORACLE_STEEM_OWNER", "ORACLE_STEEM_POSTING_WIF",
    "ORACLE_STEEM_MASTER_PASSWORD", "ORACLE_STEEM_PASSWORD", "OWNER", "POSTING", "MASTER_PASSWORD",
)

LOCK_FILE = "svm_bank_signer.lock"


class SignerConfigError(RuntimeError):
    pass


def check_forbidden_key_vars(env: Mapping[str, str]) -> None:
    for k in FORBIDDEN_KEY_VARS:
        if env.get(k, ""):
            raise SignerConfigError(
                f"refusing to start: {k} is set -- only ORACLE_STEEM_ACTIVE_WIF may be configured (an owner key, "
                "posting key or master password is never needed and must not be on the server)")


@dataclass
class SignerConfig:
    policy: Config = field(default_factory=Config)
    active_wif: str = ""
    nodes: list[str] = field(default_factory=list)
    interval: float = 10.0
    state_dir: str = "/oracle-data"
    propose: bool = True
    execute: bool = True
    dry_run: bool = False
    rank_delay: float = 4.0  # seconds between members executing the same ready proposal


def _lock_state_dir(state_dir: str):
    """An exclusive, non-blocking flock so only one signer writes this
    validator's metadata (two writers would overwrite each other's votes)."""
    os.makedirs(state_dir, mode=0o700, exist_ok=True)
    f = open(os.path.join(state_dir, LOCK_FILE), "a+")  # noqa: SIM115 - held for the process lifetime
    try:
        import fcntl
    except ImportError:  # not Linux: the signer only runs in Linux containers
        return f
    try:
        fcntl.flock(f.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError as exc:
        f.close()
        raise SignerConfigError(f"one signer per validator: another signer already holds {f.name}: {exc}") from exc
    return f


def _utcnow() -> datetime:
    return datetime.now(timezone.utc)


class Signer:
    def __init__(self, cfg: SignerConfig, svm: Any, env: Optional[Mapping[str, str]] = None, rpc: Any = None,
                 log: logging.Logger = logger):
        check_forbidden_key_vars(os.environ if env is None else env)
        try:
            selftest()
        except Exception as exc:  # noqa: BLE001
            raise SignerConfigError(f"refusing to start: {exc}") from exc
        if not cfg.active_wif:
            raise SignerConfigError("ORACLE_STEEM_ACTIVE_WIF is required when the multisig signer is enabled")
        try:
            self.key = steemtx.decode_wif(cfg.active_wif)
        except ValueError as exc:
            raise SignerConfigError(f"ORACLE_STEEM_ACTIVE_WIF: {exc}") from exc
        if rpc is None and not cfg.nodes:
            raise SignerConfigError("no Steem RPC nodes configured (ORACLE_STEEM_RPCS)")
        if cfg.interval <= 0:
            cfg.interval = 10.0
        if cfg.rank_delay <= 0:
            cfg.rank_delay = 4.0
        self.cfg, self.svm, self.log = cfg, svm, log
        self.rpc = rpc if rpc is not None else steemtx.RPC(cfg.nodes)
        self.pub = steemtx.public_key_of(self.key)
        self.me = ""
        self.policy: Optional[Policy] = None
        self.hist: Optional[HistoryIndex] = None
        self.journal: Optional[ApprovalJournal] = None
        self.first_seen: dict[str, datetime] = {}
        self.ready_since: dict[str, datetime] = {}
        self.logged: dict[str, str] = {}
        self._lock = None

    def init(self) -> None:
        """Resolves this validator's Steem account and verifies the key."""
        if self._lock is None:
            self._lock = _lock_state_dir(self.cfg.state_dir)
        me = self.svm.self_steem_account()
        accts = get_accounts(self.rpc, [me])
        if len(accts) != 1:
            raise SignerConfigError(f"steem account {me} not found")
        if any(k == self.pub for k, _ in accts[0].owner.key_auths):
            raise SignerConfigError(f"refusing to run: ORACLE_STEEM_ACTIVE_WIF is an OWNER key of @{me} -- use the active key")
        if not key_satisfies(accts[0].active, self.pub):
            raise SignerConfigError(f"ORACLE_STEEM_ACTIVE_WIF ({self.pub}) does not satisfy @{me}'s active authority")
        self.hist = HistoryIndex.load(self.cfg.state_dir)
        self.journal = ApprovalJournal.load(self.cfg.state_dir)
        self.me = me
        self.policy = Policy(self.cfg.policy, self.svm, me, self.journal)
        self.log.info("svm.bank signer ready: validator=%s key=%s dry_run=%s propose=%s execute=%s",
                      me, self.pub, self.cfg.dry_run, self.cfg.propose, self.cfg.execute)

    def close(self) -> None:
        if self._lock is not None:
            self._lock.close()
            self._lock = None

    def run(self, stop: threading.Event) -> None:
        """Initializes (retrying every 30s) and then runs a pass every interval
        until stop is set."""
        try:
            while not stop.is_set():
                try:
                    self.init()
                    break
                except Exception as exc:  # noqa: BLE001
                    self.log.error("svm.bank signer: init failed, retrying: %s", exc)
                    stop.wait(30)
            while not stop.is_set():
                try:
                    self.pass_once()
                except Exception as exc:  # noqa: BLE001
                    self.log.error("svm.bank signer: pass failed: %s", exc)
                stop.wait(self.cfg.interval)
        finally:
            self.close()

    def pass_once(self) -> None:
        assert self.hist is not None and self.policy is not None and self.journal is not None
        props = get_dynamic_props(self.rpc)
        now = steemtx.parse_time(props.time)
        bank_name = self.cfg.policy.bank_account
        self.hist.sync(self.rpc, bank_name, props.head_block_number, now)
        try:
            self.hist.save(self.cfg.state_dir)
        except OSError as exc:
            self.log.error("svm.bank signer: saving history index: %s", exc)

        accts = get_accounts(self.rpc, [bank_name])
        if len(accts) != 1:
            raise RuntimeError(f"loading {bank_name}")
        bank = accts[0]
        names = [n for n, _ in bank.owner.sorted().account_auths]
        members, ballots = {}, {}
        if names:
            for a in get_accounts(self.rpc, names):
                members[a.name] = a
                try:
                    ballots[a.name] = parse_metadata(a.posting_json_metadata)[1]
                except (InvalidMetadata, MalformedBallot, ValueError):
                    pass
        view = build_view(now, props, bank, members, ballots, self.hist)

        requested = {w.id: w for w in self.svm.requested_withdrawals()}
        st = SVMState(self.svm.height(), self.svm.withdrawal_timeout_blocks(), requested)

        mine = ballots.get(self.me, Ballot())
        if view.is_member(self.me):
            try:
                self._vote_and_propose(view, st, props, now, mine)
            except Exception as exc:  # noqa: BLE001
                self.log.error("svm.bank signer: vote/propose: %s", exc)
        if self.cfg.execute:
            self._execute(view, st, mine)

    def _log_decision(self, key: str, d: str, **kv: Any) -> None:
        if self.logged.get(key) == d:
            return
        self.logged[key] = d
        detail = " ".join(f"{k}={v!r}" for k, v in kv.items())
        self.log.info("svm.bank: %s key=%s dry_run=%s %s", d, key, self.cfg.dry_run, detail)

    def _vote_and_propose(self, view: View, st: SVMState, props: steemtx.DynamicProps, now: datetime,
                          mine: Ballot) -> None:
        assert self.policy is not None and self.journal is not None
        my_votes = mine.votes
        new_votes: dict[str, Vote] = {}
        new_props: dict[str, StoredProposal] = {}
        # A working copy so approvals cast earlier in this pass count for the
        # "one live approval per withdrawal" rule later in the same pass.
        working = dict(my_votes)

        for p in view.proposals:
            if not p.live():
                continue
            mv = my_votes.get(p.id)
            mine_vi = p.votes.get(self.me)
            if mv is not None and (mv.a == 0 or (mine_vi is not None and mine_vi.valid)):
                continue  # already voted (re-vote only if our approval is invalid)
            d, reason = self.policy.review(p, view, st, working)
            self._log_decision(p.id, d, title=p.title, reason=reason)
            if d == APPROVE:
                v = Vote(1, steemtx.sign(self.key, p.digest), p.tx.expiration)
                new_votes[p.id] = working[p.id] = v
                if not self.cfg.dry_run:
                    self.journal.record(p.id, p.tx)
            elif d == DENY:
                v = Vote(0, "", p.tx.expiration)
                new_votes[p.id] = working[p.id] = v

        if self.cfg.propose:
            intents, err = self.policy.intents(view, st, self.first_seen, _utcnow())
            if err:
                self.log.error("svm.bank: computing intents: %s", err)
            for intent in intents:
                tx = steemtx.new_transaction(props, intent.ops, timedelta(minutes=58))
                pid = tx.id()
                new_props[pid] = StoredProposal(intent.title, tx, props.time)
                v = Vote(1, steemtx.sign(self.key, tx.digest()), tx.expiration)
                new_votes[pid] = working[pid] = v
                if self.cfg.dry_run:
                    # Each pass builds a fresh transaction (new ID), so log by intent.
                    self._log_decision("intent " + intent.key, APPROVE, title=intent.title)
                    continue
                self.journal.record(pid, tx)
                self.log.info("svm.bank: propose id=%s title=%r", pid, intent.title)
        self._prune_local(now)

        if not new_votes and not new_props or self.cfg.dry_run:
            return
        # Write-ahead: the journal must hold every approval before any signature
        # leaves this process. If it can't be saved, nothing is published.
        try:
            self.journal.save(self.cfg.state_dir)
        except OSError as exc:
            raise RuntimeError(f"saving the approval journal (nothing published): {exc}") from exc
        self._publish(props, now, new_votes, new_props)

    def _publish(self, props: steemtx.DynamicProps, now: datetime, votes: dict[str, Vote],
                 new_props: dict[str, StoredProposal]) -> None:
        """Read-modify-write of our own svm_ms in one account_update2, signed
        with the active key (active authority satisfies posting)."""
        accts = get_accounts(self.rpc, [self.me])
        if len(accts) != 1:
            raise RuntimeError(f"reloading @{self.me}")
        try:
            raw, ballot = parse_metadata(accts[0].posting_json_metadata)
        except (InvalidMetadata, MalformedBallot, ValueError) as exc:
            raise RuntimeError(f"refusing to overwrite @{self.me}'s posting_json_metadata: {exc}") from exc
        ballot.votes.update(votes)
        ballot.props.update(new_props)
        ballot.prune(now)
        posting = encode_metadata(raw, ballot)
        tx = steemtx.new_transaction(props, [steemtx.Operation("account_update2", steemtx.AccountUpdate2(
            account=self.me, json_metadata="", posting_json_metadata=posting))], timedelta(minutes=5))
        tx.signatures = [steemtx.sign(self.key, tx.digest())]
        broadcast(self.rpc, tx)
        self.log.info("svm.bank: published votes=%d proposals=%d", len(votes), len(new_props))

    def _execute(self, view: View, st: SVMState, mine: Ballot) -> None:
        """Broadcasts ready proposals this signer's own policy still approves,
        with only the minimal signature set. The creator goes first; others
        wait by rank (a duplicate broadcast is harmless)."""
        assert self.policy is not None
        members = view.sorted_members()
        for p in view.proposals:
            if p.status != READY:
                self.ready_since.pop(p.id, None)
                continue
            self.ready_since.setdefault(p.id, _utcnow())
            if p.expiration - timedelta(seconds=30) <= _utcnow():
                continue
            delay = 0.0
            if p.creator != self.me:
                rank = rank_of(members, self.me, 0)
                if rank < 0:
                    rank = len(members)
                delay = self.cfg.rank_delay * (rank + 1)
            if (_utcnow() - self.ready_since[p.id]).total_seconds() < delay:
                continue
            votes = {k: v for k, v in mine.votes.items() if k != p.id}
            d, reason = self.policy.review(p, view, st, votes)
            if d != APPROVE:
                self._log_decision("exec " + p.id, d, title=p.title, reason="not executing ready proposal: " + reason)
                continue
            sigs = view.minimal_signatures(p)
            if sigs is None:
                continue
            tx = steemtx.Transaction(p.tx.ref_block_num, p.tx.ref_block_prefix, p.tx.expiration, p.tx.operations, sigs)
            if self.cfg.dry_run:
                self._log_decision("exec " + p.id, APPROVE, title=p.title, would_execute_with_signatures=len(sigs))
                continue
            try:
                dup = broadcast(self.rpc, tx)
            except Exception as exc:  # noqa: BLE001
                self.log.error("svm.bank: execute failed id=%s title=%r: %s", p.id, p.title, exc)
                continue
            self.log.info("svm.bank: %s id=%s title=%r signatures=%d", "already executed" if dup else "executed",
                          p.id, p.title, len(sigs))

    def _prune_local(self, now: datetime) -> None:
        if len(self.logged) > 2000:
            self.logged.clear()
        if len(self.first_seen) > 1000:
            self.first_seen.clear()
        if self.journal is not None:
            self.journal.prune(now)


# --------------------------------------------------------------------------
# Environment (shared by the Go, Python and JS oracles; oracle/PROTOCOL.md)
# --------------------------------------------------------------------------

_TRUE = {"1", "t", "T", "TRUE", "true", "True"}
_FALSE = {"0", "f", "F", "FALSE", "false", "False"}


def env_bool(env: Mapping[str, str], key: str, default: bool) -> bool:
    v = env.get(key, "").strip()
    if not v:
        return default
    if v in _TRUE:
        return True
    if v in _FALSE:
        return False
    raise SignerConfigError(f"{key} must be true or false")


def build_signer_config(env: Mapping[str, str], steem_rpc: str, state_dir: str) -> Optional[SignerConfig]:
    """None when ORACLE_MULTISIG_ENABLED is not true."""
    from ..config import parse_duration_seconds

    if not env_bool(env, "ORACLE_MULTISIG_ENABLED", False):
        return None
    cfg = SignerConfig(active_wif=env.get("ORACLE_STEEM_ACTIVE_WIF", "").strip(), state_dir=state_dir)
    cfg.nodes = [n.strip() for n in (env.get("ORACLE_STEEM_RPCS", "").strip() or steem_rpc).split(",") if n.strip()]

    def uint(key: str) -> Optional[int]:
        v = env.get(key, "").strip()
        if not v:
            return None
        if not v.isascii() or not v.isdigit():
            raise SignerConfigError(f"{key} must be an unsigned integer")
        return int(v)

    if (iv := env.get("ORACLE_MULTISIG_INTERVAL", "").strip()):
        try:
            cfg.interval = parse_duration_seconds(iv)
        except ValueError as exc:
            raise SignerConfigError(f"ORACLE_MULTISIG_INTERVAL: {exc}") from exc
    if (n := uint("ORACLE_MULTISIG_THRESHOLD_PERCENT")) is not None:
        if not 1 <= n <= 100:
            raise SignerConfigError("ORACLE_MULTISIG_THRESHOLD_PERCENT must be 1..100")
        cfg.policy.threshold_percent = n
    if (n := uint("ORACLE_MULTISIG_REWEIGHT_TOLERANCE")) is not None:
        if n > 10000:
            raise SignerConfigError("ORACLE_MULTISIG_REWEIGHT_TOLERANCE must be 0..10000")
        cfg.policy.reweight_tolerance = n
    if (n := uint("ORACLE_MULTISIG_PAYOUT_MARGIN_BLOCKS")) is not None:
        cfg.policy.payout_margin_blocks = n
    cfg.propose = env_bool(env, "ORACLE_MULTISIG_PROPOSE", True)
    cfg.execute = env_bool(env, "ORACLE_MULTISIG_EXECUTE", True)
    cfg.dry_run = env_bool(env, "ORACLE_MULTISIG_DRY_RUN", False)
    for sym in ("STEEM", "SBD"):
        v = env.get(f"ORACLE_MULTISIG_MAX_PER_TX_{sym}", "").strip()
        if v:
            try:
                cfg.policy.max_per_tx_milli[sym] = steemtx.parse_asset(f"{v} {sym}").amount
            except ValueError as exc:
                raise SignerConfigError(f"ORACLE_MULTISIG_MAX_PER_TX_{sym}: {exc}") from exc
    return cfg
