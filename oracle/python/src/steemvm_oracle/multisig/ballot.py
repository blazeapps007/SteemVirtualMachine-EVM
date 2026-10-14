"""The ``svm_ms`` object inside a validator's ``posting_json_metadata``:
its proposals and votes (``oracle/multisigguide.md`` section 2.1). Port of
``oracle/go/multisig/ballot.go``.

Other top-level keys are kept as their exact raw JSON text, so publishing a
vote never reformats a validator's profile or anything else stored there.
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from datetime import datetime, timedelta
from json.decoder import scanstring
from typing import Any, Optional

from . import steemtx

METADATA_KEY = "svm_ms"
PRUNE_AFTER = timedelta(hours=72)
# Keeps the serialized svm_ms well inside Steem's 64 KiB transaction limit.
MAX_BALLOT_BYTES = 40 * 1024


class InvalidMetadata(ValueError):
    """posting_json_metadata is not a JSON object; a signer must never
    overwrite it in that case."""


class MalformedBallot(ValueError):
    """svm_ms exists but is malformed: readers ignore it, and a signer never
    overwrites its own."""


@dataclass
class StoredProposal:
    t: str
    tx: steemtx.Transaction
    c: str

    def to_json(self) -> dict:
        return {"t": self.t, "tx": self.tx.to_json(), "c": self.c}


@dataclass(frozen=True)
class Vote:
    a: int  # 1 approve, 0 deny
    s: str  # signature over the tx digest, approvals only
    x: str  # the tx expiration, used for pruning

    def to_json(self) -> dict:
        out: dict = {"a": self.a}
        if self.s:
            out["s"] = self.s
        out["x"] = self.x
        return out


@dataclass
class Ballot:
    props: dict[str, StoredProposal] = field(default_factory=dict)
    votes: dict[str, Vote] = field(default_factory=dict)

    def to_json(self) -> dict:
        # Map keys sorted, as Go's encoder writes them.
        return {
            "props": {k: self.props[k].to_json() for k in sorted(self.props)},
            "votes": {k: self.votes[k].to_json() for k in sorted(self.votes)},
        }

    def encoded_size(self) -> int:
        return len(_dumps(self.to_json()).encode("utf-8"))

    def prune(self, now: datetime) -> None:
        """Drops proposals/votes that expired more than PRUNE_AFTER before now,
        then (if still too large) the oldest expired entries until it fits.
        Live entries are never dropped."""
        cut = now - PRUNE_AFTER
        for pid in [k for k, p in self.props.items() if _before(p.tx.expiration, cut)]:
            del self.props[pid]
        for vid in [k for k, v in self.votes.items() if _before(v.x, cut)]:
            del self.votes[vid]
        while self.encoded_size() > MAX_BALLOT_BYTES and self._drop_oldest(now):
            pass

    def _drop_oldest(self, now: datetime) -> bool:
        oldest: Optional[tuple[datetime, str, bool]] = None
        for pid, p in self.props.items():
            oldest = _older(oldest, p.tx.expiration, pid, True, now)
        for vid, v in self.votes.items():
            oldest = _older(oldest, v.x, vid, False, now)
        if oldest is None:
            return False
        _, key, is_prop = oldest
        if is_prop:
            del self.props[key]
        else:
            del self.votes[key]
        return True


def _before(ts: str, cut: datetime) -> bool:
    try:
        return steemtx.parse_time(ts) < cut
    except ValueError:
        return False


def _older(cur, ts: str, key: str, is_prop: bool, now: datetime):
    try:
        t = steemtx.parse_time(ts)
    except ValueError:
        return cur
    if not t < now:
        return cur
    if cur is None or t < cur[0]:
        return (t, key, is_prop)
    return cur


def _dumps(obj: Any) -> str:
    return json.dumps(obj, separators=(",", ":"), ensure_ascii=False)


_WS = " \t\n\r"
_DECODER = json.JSONDecoder()


def _split_object(text: str) -> dict[str, str]:
    """Splits a JSON object into {key: raw value text}, validating all of it."""
    i, n = 0, len(text)

    def skip(j: int) -> int:
        while j < n and text[j] in _WS:
            j += 1
        return j

    i = skip(i)
    if i >= n or text[i] != "{":
        raise InvalidMetadata("posting_json_metadata is not a JSON object")
    out: dict[str, str] = {}
    i = skip(i + 1)
    if i < n and text[i] == "}":
        i += 1
    else:
        while True:
            if i >= n or text[i] != '"':
                raise InvalidMetadata("expected a key")
            key, i = scanstring(text, i + 1)
            i = skip(i)
            if i >= n or text[i] != ":":
                raise InvalidMetadata("expected ':'")
            i = skip(i + 1)
            _, end = _DECODER.raw_decode(text, i)
            out[key] = text[i:end]
            i = skip(end)
            if i < n and text[i] == ",":
                i = skip(i + 1)
                continue
            if i < n and text[i] == "}":
                i += 1
                break
            raise InvalidMetadata("expected ',' or '}'")
    if skip(i) != n:
        raise InvalidMetadata("trailing data after the JSON object")
    return out


def _vote(obj: Any) -> Vote:
    if not isinstance(obj, dict):
        raise MalformedBallot("a vote must be an object")
    a = obj.get("a", 0)
    if isinstance(a, bool) or not isinstance(a, int):
        raise MalformedBallot("vote.a must be an integer")
    s, x = obj.get("s", ""), obj.get("x", "")
    if not isinstance(s, str) or not isinstance(x, str):
        raise MalformedBallot("vote.s and vote.x must be strings")
    return Vote(a, s, x)


def parse_metadata(posting: str) -> tuple[dict[str, str], Ballot]:
    """Splits posting_json_metadata into the untouched raw entries and the
    parsed svm_ms ballot. An empty string is an empty object. Raises
    InvalidMetadata (never overwrite) or MalformedBallot (svm_ms unusable)."""
    text = (posting or "").strip()
    raw: dict[str, str] = {}
    if text and text != "null":
        try:
            raw = _split_object(text)
        except (ValueError, IndexError) as exc:
            raise InvalidMetadata(str(exc)) from exc
    ballot = Ballot()
    ms_text = raw.get(METADATA_KEY)
    if ms_text is not None and ms_text != "null":
        ms = json.loads(ms_text)
        if not isinstance(ms, dict):
            raise MalformedBallot("svm_ms must be an object")
        props, votes = ms.get("props"), ms.get("votes")
        props = {} if props is None else props
        votes = {} if votes is None else votes
        if not isinstance(props, dict) or not isinstance(votes, dict):
            raise MalformedBallot("svm_ms.props and svm_ms.votes must be objects")
        for vid, v in votes.items():
            ballot.votes[vid] = Vote(0, "", "") if v is None else _vote(v)
        for pid, p in props.items():
            try:  # unsupported operations or a bad shape: unverifiable, skipped
                if not isinstance(p, dict):
                    continue
                t, c = p.get("t", ""), p.get("c", "")
                if not isinstance(t, str) or not isinstance(c, str):
                    continue
                ballot.props[pid] = StoredProposal(t, steemtx.Transaction.from_json(p.get("tx")), c)
            except (ValueError, TypeError):
                continue
    return raw, ballot


def encode_metadata(raw: dict[str, str], ballot: Ballot) -> str:
    """Replaces only svm_ms and returns the new posting_json_metadata. Other
    keys keep their exact text; keys are written in sorted order."""
    out = dict(raw)
    out[METADATA_KEY] = _dumps(ballot.to_json())
    return "{" + ",".join(f"{_dumps(k)}:{out[k].strip()}" for k in sorted(out)) + "}"
