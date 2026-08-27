"""SIGN_MODE_DIRECT Cosmos SDK tx assembly + eth_secp256k1 signing.

Implements ``oracle/PROTOCOL.md`` SS2 byte-for-byte:
``TxBody -> AuthInfo -> SignDoc -> Keccak256 digest -> secp256k1 sign -> TxRaw``.

Critically (PROTOCOL.md SS2 step 7): the ``body_bytes``/``auth_info_bytes``
embedded in the final ``TxRaw`` are the EXACT SAME serialized bytes used to
build the ``SignDoc`` -- each is serialized exactly once here and the bytes
are reused, never re-marshaled (protobuf encoding is not guaranteed
byte-stable across two marshal calls of the same logical message).
"""

from __future__ import annotations

from dataclasses import dataclass
from decimal import ROUND_CEILING, Decimal
from typing import Iterable, Sequence

from eth_utils import keccak
from google.protobuf.message import Message

from . import _protopath  # noqa: F401  (sys.path bootstrap)
from .keys import Keypair

# These imports resolve via the proto_gen/ directory _protopath added to
# sys.path -- see that module's docstring for why they're absolute/top-level
# rather than package-relative.
from cosmos.base.v1beta1 import coin_pb2  # noqa: E402
from cosmos.evm.crypto.v1.ethsecp256k1 import keys_pb2 as ethsecp256k1_pb2  # noqa: E402
from cosmos.tx.signing.v1beta1 import signing_pb2  # noqa: E402
from cosmos.tx.v1beta1 import tx_pb2  # noqa: E402
from google.protobuf import any_pb2  # noqa: E402

# Pubkey Any type URL -- see oracle/PROTOCOL.md SS1.
PUBKEY_TYPE_URL = "/cosmos.evm.crypto.v1.ethsecp256k1.PubKey"

# Duty message type URLs -- see oracle/PROTOCOL.md SS2 "Message type URLs".
TYPE_URL_ATTEST_DEPOSIT = "/steemvm.steembridge.v1.MsgAttestDeposit"
TYPE_URL_ATTEST_WITHDRAWAL_PAYOUT = "/steemvm.steembridge.v1.MsgAttestWithdrawalPayout"
TYPE_URL_SUBMIT_NAME_REGISTRATION = "/steemvm.steembridge.v1.MsgSubmitNameRegistration"
TYPE_URL_AGGREGATE_EXCHANGE_RATE_PREVOTE = "/steemvm.oracle.data.v1.MsgAggregateExchangeRatePrevote"
TYPE_URL_AGGREGATE_EXCHANGE_RATE_VOTE = "/steemvm.oracle.data.v1.MsgAggregateExchangeRateVote"


@dataclass(frozen=True)
class Coin:
    denom: str
    amount: str  # decimal string; integer amount in base-denom units


def pack_any(type_url: str, message: Message) -> any_pb2.Any:
    packed = any_pb2.Any()
    packed.type_url = type_url
    packed.value = message.SerializeToString()
    return packed


def build_pubkey_any(pubkey_compressed: bytes) -> any_pb2.Any:
    return pack_any(PUBKEY_TYPE_URL, ethsecp256k1_pb2.PubKey(key=pubkey_compressed))


def build_tx_body_bytes(msg_anys: Sequence[any_pb2.Any], memo: str = "") -> bytes:
    body = tx_pb2.TxBody(messages=list(msg_anys), memo=memo, timeout_height=0)
    return body.SerializeToString()


def build_auth_info_bytes(
    pubkey_any: any_pb2.Any,
    sequence: int,
    gas_limit: int,
    fee: Iterable[Coin] = (),
) -> bytes:
    fee_coins = [coin_pb2.Coin(denom=c.denom, amount=c.amount) for c in fee]
    signer_info = tx_pb2.SignerInfo(
        public_key=pubkey_any,
        mode_info=tx_pb2.ModeInfo(single=tx_pb2.ModeInfo.Single(mode=signing_pb2.SIGN_MODE_DIRECT)),
        sequence=sequence,
    )
    auth_info = tx_pb2.AuthInfo(
        signer_infos=[signer_info],
        fee=tx_pb2.Fee(amount=fee_coins, gas_limit=gas_limit, payer="", granter=""),
    )
    return auth_info.SerializeToString()


def build_sign_doc_bytes(
    body_bytes: bytes,
    auth_info_bytes: bytes,
    chain_id: str,
    account_number: int,
) -> bytes:
    sign_doc = tx_pb2.SignDoc(
        body_bytes=body_bytes,
        auth_info_bytes=auth_info_bytes,
        chain_id=chain_id,
        account_number=account_number,
    )
    return sign_doc.SerializeToString()


def sign_doc_digest(sign_doc_bytes: bytes) -> bytes:
    """Digest = Keccak256(serialized SignDoc). See oracle/PROTOCOL.md SS2 step 5."""
    return keccak(sign_doc_bytes)


@dataclass(frozen=True)
class SignedTx:
    body_bytes: bytes
    auth_info_bytes: bytes
    sign_doc_bytes: bytes
    digest: bytes
    signature: bytes  # 65 bytes R||S||V
    tx_raw_bytes: bytes


def sign_tx(
    keypair: Keypair,
    msg_anys: Sequence[any_pb2.Any],
    *,
    chain_id: str,
    account_number: int,
    sequence: int,
    gas_limit: int,
    fee: Iterable[Coin] = (),
    memo: str = "",
) -> SignedTx:
    body_bytes = build_tx_body_bytes(msg_anys, memo=memo)
    pubkey_any = build_pubkey_any(keypair.pubkey_compressed)
    auth_info_bytes = build_auth_info_bytes(pubkey_any, sequence, gas_limit, fee)
    sign_doc_bytes = build_sign_doc_bytes(body_bytes, auth_info_bytes, chain_id, account_number)
    digest = sign_doc_digest(sign_doc_bytes)
    signature = keypair.sign(digest)

    # Reuse body_bytes/auth_info_bytes verbatim -- never re-marshal (PROTOCOL.md SS2 step 7).
    tx_raw = tx_pb2.TxRaw(body_bytes=body_bytes, auth_info_bytes=auth_info_bytes, signatures=[signature])
    return SignedTx(
        body_bytes=body_bytes,
        auth_info_bytes=auth_info_bytes,
        sign_doc_bytes=sign_doc_bytes,
        digest=digest,
        signature=signature,
        tx_raw_bytes=tx_raw.SerializeToString(),
    )


def parse_gas_prices(gas_prices: str, gas_limit: int) -> list[Coin]:
    """Parses a Cosmos SDK DecCoins gas-price string (e.g.
    ``"1000000000asteem"``, optionally comma-separated for multiple denoms)
    and computes the fee ``ceil(gas_price * gas_limit)`` per denom -- the
    same rounding convention the SDK's own gas-price-derived fee uses."""
    coins: list[Coin] = []
    for part in gas_prices.split(","):
        part = part.strip()
        if not part:
            continue
        idx = 0
        while idx < len(part) and (part[idx].isdigit() or part[idx] == "."):
            idx += 1
        if idx == 0 or idx == len(part):
            raise ValueError(f"invalid gas price entry {part!r}")
        price_str, denom = part[:idx], part[idx:]
        price = Decimal(price_str)
        amount = (price * gas_limit).to_integral_value(rounding=ROUND_CEILING)
        coins.append(Coin(denom=denom, amount=str(amount)))
    return coins
