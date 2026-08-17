"""Memo classification, destination parsing, and attestation-message
construction. Ports ``oracle/go/relayer/router.go`` and the destination
parser it depends on, ``x/oracle/bridge/types/memo.go``'s
``DeriveDestination`` -- the exact in-consensus parser, so the client's
"is this memo supported at all" pre-filter can never drift from the
chain's own claimability decision.
"""

from __future__ import annotations

import re
from enum import Enum, auto
from typing import Optional

from . import _protopath  # noqa: F401
from .keys import ACCOUNT_HRP, decode_address
from .signing import (
    TYPE_URL_ATTEST_DEPOSIT,
    TYPE_URL_ATTEST_WITHDRAWAL_PAYOUT,
    TYPE_URL_SUBMIT_NAME_REGISTRATION,
    pack_any,
)
from .steem_client import ASSET_SBD, Payout, Transfer

from steemvm.steembridge.v1 import asset_pb2  # noqa: E402
from steemvm.steembridge.v1 import tx_pb2 as steembridge_tx_pb2  # noqa: E402

# 0x-prefixed 20-byte EVM address, any case -- mirrors x/oracle/bridge/types/memo.go's hexAddressRegex.
_HEX_ADDRESS_RE = re.compile(r"^0x[0-9a-fA-F]{40}$")

_ASSET_MAP = {
    "STEEM": asset_pb2.BRIDGE_ASSET_STEEM,
    ASSET_SBD: asset_pb2.BRIDGE_ASSET_SBD,
}


class Intent(Enum):
    """What a gateway transfer's memo asks the bridge to do."""

    DEPOSIT = auto()  # mints bridged STEEM ("svm-deposit <address>" or a bare address memo)
    REGISTER = auto()  # links the sender's Steem account name ("svm-register <address>")


def route_memo(memo: str) -> Intent:
    """Classifies a gateway transfer by its memo prefix. Everything that is
    not explicitly a name registration is attested as a deposit -- the
    chain itself decides claimability from the memo."""
    trimmed = memo.strip()
    if trimmed == "svm-register" or trimmed.startswith("svm-register ") or trimmed.startswith("svm-register\t"):
        return Intent.REGISTER
    return Intent.DEPOSIT


def derive_destination(memo: str) -> tuple[Optional[bytes], str, bool]:
    """Parses a bridge deposit memo and derives the destination account.
    Returns (20-byte address or None, destination_type, ok). Mirrors
    x/oracle/bridge/types/memo.go's DeriveDestination exactly: strip an
    optional leading intent-prefix token ("svm-deposit"/"svm-register",
    requiring a separator after it), then accept either a bech32 address
    with this chain's account HRP or a "0x" + 40 hex character EVM address.
    Anything else is unparseable (ok=False)."""
    trimmed = memo.strip()

    for prefix in ("svm-deposit", "svm-register"):
        if trimmed.startswith(prefix):
            rest = trimmed[len(prefix) :]
            if rest == "" or rest[0] in (" ", "\t"):
                trimmed = rest.strip()
            break

    try:
        hrp, addr_bytes = decode_address(trimmed)
        if hrp == ACCOUNT_HRP and len(addr_bytes) == 20:
            return addr_bytes, "COSMOS", True
    except ValueError:
        pass

    if _HEX_ADDRESS_RE.match(trimmed):
        return bytes.fromhex(trimmed[2:]), "EVM", True

    return None, "NONE", False


def build_transfer_any(transfer: Transfer, intent: Intent, validator_address: str, gateway: str):
    """Builds the Any-wrapped attestation message for a scanned transfer.
    Mirrors oracle/go/relayer/router.go's BuildMsg. All Steem-side facts are
    passed through verbatim (including the memo -- the chain strips intent
    prefixes itself in DeriveDestination)."""
    # The name service is STEEM-only; an SBD transfer is always a deposit
    # even if its memo looks like a registration.
    if transfer.asset == ASSET_SBD:
        intent = Intent.DEPOSIT

    if intent == Intent.REGISTER:
        msg = steembridge_tx_pb2.MsgSubmitNameRegistration(
            validator=validator_address,
            txid=transfer.txid,
            op_index=transfer.op_index,
            steem_block=transfer.steem_block,
            steem_timestamp=transfer.steem_timestamp,
            steem_account=transfer.from_,
            gateway_account=gateway,
            amount_millisteem=transfer.amount_millisteem,
            memo=transfer.memo,
        )
        return pack_any(TYPE_URL_SUBMIT_NAME_REGISTRATION, msg)

    msg = steembridge_tx_pb2.MsgAttestDeposit(
        validator=validator_address,
        txid=transfer.txid,
        op_index=transfer.op_index,
        steem_block=transfer.steem_block,
        steem_timestamp=transfer.steem_timestamp,
        steem_sender=transfer.from_,
        gateway_account=gateway,
        amount_millisteem=transfer.amount_millisteem,
        memo=transfer.memo,
        asset=_ASSET_MAP[transfer.asset],
    )
    return pack_any(TYPE_URL_ATTEST_DEPOSIT, msg)


def build_payout_any(payout: Payout, validator_address: str):
    msg = steembridge_tx_pb2.MsgAttestWithdrawalPayout(
        validator=validator_address,
        withdrawal_id=payout.withdrawal_id,
        steem_txid=payout.txid,
        op_index=payout.op_index,
        steem_block=payout.steem_block,
        steem_timestamp=payout.steem_timestamp,
    )
    return pack_any(TYPE_URL_ATTEST_WITHDRAWAL_PAYOUT, msg)
