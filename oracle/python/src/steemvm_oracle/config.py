"""Environment-variable configuration, mirroring
``oracle/go/relayer/config.go``'s ``Config``. Actual environment PARSING
(reading ``os.environ``) lives in ``main.py``, matching how the Go relayer
package stays a pure library while ``oracle/go/main.go`` owns env access --
this module only defines the shape and a Go-duration-string parser.
"""

from __future__ import annotations

import re
from dataclasses import dataclass

_DURATION_TOKEN_RE = re.compile(r"(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)")
_UNIT_SECONDS = {
    "ns": 1e-9,
    "us": 1e-6,
    "µs": 1e-6,  # µs
    "ms": 1e-3,
    "s": 1.0,
    "m": 60.0,
    "h": 3600.0,
}


def parse_duration_seconds(value: str) -> float:
    """Parses a Go ``time.ParseDuration``-style string ("1m", "30s",
    "1h30m") into seconds. Raises ValueError on malformed input."""
    value = value.strip()
    if not value:
        raise ValueError("empty duration")
    total = 0.0
    pos = 0
    for m in _DURATION_TOKEN_RE.finditer(value):
        if m.start() != pos:
            raise ValueError(f"invalid duration {value!r}")
        total += float(m.group(1)) * _UNIT_SECONDS[m.group(2)]
        pos = m.end()
    if pos == 0 or pos != len(value):
        raise ValueError(f"invalid duration {value!r}")
    return total


@dataclass
class Config:
    """Drives the relayer loop. Populated from the environment by main.py."""

    steem_rpc_url: str = ""
    # Bridgeable STEEM symbol in transfer amounts (mainnet "STEEM"; Steem
    # *testnet* denotes the same asset as "TESTS", testnet SBD as "TBD").
    # Not configurable via env, same as the Go client -- every oracle
    # watching the same network must set the SAME symbol, since consensus
    # only ever sees the resulting integer millisteem.
    steem_symbol: str = "STEEM"
    # Overridden to "SBD" by main.py's default; empty here disables SBD extraction.
    sbd_symbol: str = ""
    poll_interval_seconds: float = 60.0
    max_blocks_per_poll: int = 100
    # 0 defers to the chain's relayer_start_block param, falling back to
    # Steem's current last-irreversible block. Ignored once a cursor exists.
    start_block: int = 0

    def enabled(self) -> bool:
        return bool(self.steem_rpc_url)
