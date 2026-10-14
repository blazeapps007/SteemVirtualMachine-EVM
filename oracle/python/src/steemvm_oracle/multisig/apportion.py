"""Exact stake-proportional apportionment of svm.bank's authority weights
(``oracle/PROTOCOL.md``, "svm.bank multisig signer"). Every signer must
produce a byte-identical authority from the same input, so this uses Python's
arbitrary-precision ints only -- never floats. Port of
``oracle/go/multisig/apportion.go``.
"""

from __future__ import annotations

from dataclasses import dataclass

from .steemtx import Authority

TOTAL_WEIGHT = 10000
MAX_ACCOUNT_AUTHS = 40


@dataclass(frozen=True)
class StakeEntry:
    """One validator's input: its Steem account and bonded SVM tokens (asteem)."""

    account: str
    tokens: int


def apportion(entries: list[StakeEntry], threshold_percent: int) -> Authority:
    """
    1. drop zero stakes; sort by tokens desc then account asc; keep the top 40;
    2. q_i = floor(10000*t_i/T), r_i = (10000*t_i) mod T;
    3. give +1 to the (10000 - sum q_i) entries with the largest r_i, ties by
       account asc (largest remainder), so the weights sum to exactly 10000;
    4. minimum weight 1, taken from the largest weight (ties by account asc);
    5. threshold = ceil(10000 * threshold_percent / 100);
    6. account_auths sorted by name; key_auths empty.
    """
    if not 1 <= threshold_percent <= 100:
        raise ValueError(f"threshold percent {threshold_percent} out of range 1..100")
    vals: list[StakeEntry] = []
    seen: set[str] = set()
    for e in entries:
        if e.tokens <= 0:
            continue
        if e.account in seen:
            raise ValueError(f"duplicate account {e.account!r} in stake input")
        seen.add(e.account)
        vals.append(e)
    if not vals:
        raise ValueError("no validators with stake")
    vals.sort(key=lambda e: (-e.tokens, e.account.encode("utf-8")))
    vals = vals[:MAX_ACCOUNT_AUTHS]

    total = sum(e.tokens for e in vals)
    rows = []  # [account, weight, remainder]
    for e in vals:
        q, r = divmod(TOTAL_WEIGHT * e.tokens, total)
        rows.append([e.account, q, r])

    deficit = TOTAL_WEIGHT - sum(r[1] for r in rows)
    if deficit < 0 or deficit >= len(rows):
        raise ValueError(f"impossible deficit {deficit} for {len(rows)} validators")
    for row in sorted(rows, key=lambda r: (-r[2], r[0].encode("utf-8")))[:deficit]:
        row[1] += 1

    # Minimum weight 1: zero-weight entries in account order, each unit taken
    # from the current largest weight (ties by account asc).
    by_name = sorted(rows, key=lambda r: r[0].encode("utf-8"))
    for row in by_name:
        while row[1] == 0:
            row[1] = 1
            donor = None
            for d in by_name:
                if donor is None or d[1] > donor[1]:
                    donor = d
            donor[1] -= 1

    if any(not 1 <= r[1] <= 65535 for r in rows) or sum(r[1] for r in rows) != TOTAL_WEIGHT:
        raise ValueError("apportionment produced an invalid weight set")
    return Authority(
        weight_threshold=(TOTAL_WEIGHT * threshold_percent + 99) // 100,
        account_auths=[(r[0], r[1]) for r in rows],
        key_auths=[],
    ).sorted()


def exceeds_tolerance(current: Authority, desired: Authority, tolerance: int) -> bool:
    """Whether desired differs from current enough to warrant a validator-set
    proposal: membership or threshold changed, or a weight moved by more than
    tolerance points."""
    if current.weight_threshold != desired.weight_threshold or len(current.account_auths) != len(desired.account_auths):
        return True
    for name, weight in desired.account_auths:
        c = current.weight_of(name)
        if c == 0:
            return True
        if abs(c - weight) > tolerance:
            return True
    return False
