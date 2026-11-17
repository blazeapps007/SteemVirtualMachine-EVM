"""Spreads the oracle's Steem RPC calls over several nodes and benches a node
that misbehaves, so one validator IP never hammers a single public node into
rate-limiting or blacklisting it, and one broken node never stops the oracle.
Ports ``oracle/go/nodepool``.

A node is benched after any failed call (transport error, an HTML/non-JSON
reply, a JSON-RPC error such as "Internal Error" or "Bad Cast", or an answer
that fails the caller's check) for a cooldown that doubles with each
consecutive failure (30s, 1m, 2m ... capped at 10m) and resets on success.
Benched nodes are still tried last, so a call goes out even when every node is
benched.
"""

from __future__ import annotations

import threading
import time
from typing import Callable, Iterable, Union

ROUND_ROBIN = "round_robin"  # each call starts at the next node (spreads load)
STICKY = "sticky"  # each call starts at the first healthy node (consistent view)

BASE_COOLDOWN = 30.0
MAX_COOLDOWN = 600.0


def parse(*lists: Union[str, Iterable[str], None]) -> list[str]:
    """Splits each argument on commas (a list's items too), trims, drops a
    trailing '/', and drops empty and repeated entries, keeping first-seen
    order."""
    out: list[str] = []
    for item in lists:
        if not item:
            continue
        parts = item.split(",") if isinstance(item, str) else [p for x in item for p in str(x).split(",")]
        for n in parts:
            n = n.strip().rstrip("/")
            if n and n not in out:
                out.append(n)
    return out


class NodePool:
    def __init__(self, mode: str, *nodes: Union[str, Iterable[str], None], now: Callable[[], float] = time.monotonic):
        self.mode = mode
        self.nodes = parse(*nodes)
        self._next = 0
        self._fails: dict[str, int] = {}
        self._until: dict[str, float] = {}
        self._now = now
        self._lock = threading.Lock()

    def order(self) -> list[str]:
        """The nodes to try for one call: healthy nodes first (from the
        round-robin cursor, or from the first node when sticky), then benched
        nodes, soonest-to-recover first."""
        with self._lock:
            n = len(self.nodes)
            if n == 0:
                return []
            start = 0
            if self.mode == ROUND_ROBIN:
                start = self._next % n
                self._next = (self._next + 1) % n
            now = self._now()
            rotated = [self.nodes[(start + i) % n] for i in range(n)]
            healthy = [x for x in rotated if self._until.get(x, 0.0) <= now]
            benched = sorted((x for x in rotated if self._until.get(x, 0.0) > now), key=lambda x: self._until[x])
            return healthy + benched

    def fail(self, node: str) -> float:
        """Benches node; returns the cooldown in seconds."""
        with self._lock:
            self._fails[node] = self._fails.get(node, 0) + 1
            d = min(BASE_COOLDOWN * (2 ** min(self._fails[node] - 1, 10)), MAX_COOLDOWN)
            self._until[node] = self._now() + d
            return d

    def ok(self, node: str) -> None:
        with self._lock:
            self._fails.pop(node, None)
            self._until.pop(node, None)
