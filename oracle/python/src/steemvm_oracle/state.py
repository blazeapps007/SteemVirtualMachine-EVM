"""JSON state-file persistence: the Steem scan cursor and the price
feeder's pending commit. Schemas match ``oracle/PROTOCOL.md`` SS8 exactly
(same field names/types as ``oracle/go/relayer/state.go`` and
``pricefeeder_state.go``) so a validator can switch client language
mid-operation without losing progress -- both files live in the same
``oracle-data`` volume regardless of which language container mounts it.

Atomic write: write to a temp file, then rename over the target, so a crash
mid-write can never leave a truncated file behind.
"""

from __future__ import annotations

import json
import os
from dataclasses import asdict, dataclass

# Relative to <ORACLE_STATE_DIR>. Matches oracle/go/relayer/state.go's StateFileName.
STATE_FILE_NAME = "steem_relayer_state.json"

# Relative to <ORACLE_STATE_DIR>. Kept separate from STATE_FILE_NAME so the
# two duties' failure/persistence domains stay independent -- matches
# oracle/go/relayer/pricefeeder_state.go's PriceFeederStateFileName.
PRICE_FEEDER_STATE_FILE_NAME = "price_feeder_state.json"


@dataclass
class State:
    """The relayer's persisted Steem scan cursor."""

    last_scanned_block: int = 0


@dataclass
class FeederState:
    """The price feeder's cross-cycle memory: what it previously
    COMMITTED, needed to REVEAL in the following vote period."""

    prevote_period: int = 0
    salt: str = ""
    exchange_rates: str = ""


def _load(path: str, cls: type, empty: object) -> object:
    try:
        with open(path, "r", encoding="utf-8") as f:
            raw = json.load(f)
    except FileNotFoundError:
        return empty
    except json.JSONDecodeError as exc:
        raise ValueError(f"corrupt state file {path!r}: {exc}") from exc
    # Unknown/missing keys are tolerated (forward/backward compatible with a
    # future field addition, and with a state file written by another
    # language's client using the same JSON schema).
    kwargs = {field: raw[field] for field in cls.__dataclass_fields__ if field in raw}
    return cls(**kwargs)


def _save_atomic(path: str, payload: dict) -> None:
    directory = os.path.dirname(path) or "."
    tmp_path = path + ".tmp"
    with open(tmp_path, "w", encoding="utf-8") as f:
        json.dump(payload, f)
    os.replace(tmp_path, path)


def load_state(state_dir: str) -> State:
    """A missing file returns a zero State (first run), not an error."""
    return _load(os.path.join(state_dir, STATE_FILE_NAME), State, State())  # type: ignore[return-value]


def save_state(state_dir: str, state: State) -> None:
    _save_atomic(os.path.join(state_dir, STATE_FILE_NAME), asdict(state))


def load_feeder_state(state_dir: str) -> FeederState:
    """A missing file returns a zero FeederState (first run, or a feeder
    that has never committed a prevote) -- an empty/absent file means
    "nothing pending to reveal"."""
    return _load(  # type: ignore[return-value]
        os.path.join(state_dir, PRICE_FEEDER_STATE_FILE_NAME), FeederState, FeederState()
    )


def save_feeder_state(state_dir: str, state: FeederState) -> None:
    _save_atomic(os.path.join(state_dir, PRICE_FEEDER_STATE_FILE_NAME), asdict(state))
