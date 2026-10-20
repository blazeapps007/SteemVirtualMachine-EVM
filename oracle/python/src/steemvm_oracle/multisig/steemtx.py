"""Steem transaction core for the svm.bank multisig signer: JSON-RPC with node
failover, the chain's binary serialization, transaction digest and ID, WIF/STM
key handling, and canonical recoverable secp256k1 signatures.

A field-for-field port of ``oracle/go/steemtx``, pinned byte-for-byte by
``oracle/multisig/vectors.json`` (``selftest`` in ``vectors.py``). Amounts are
integers throughout -- never floats.
"""

from __future__ import annotations

import hashlib
import re
import struct
from calendar import timegm
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from typing import Any, Optional, Union

import httpx
from coincurve import PrivateKey, PublicKey
from coincurve._libsecp256k1 import ffi

from ._ripemd160 import ripemd160

TIME_LAYOUT = "%Y-%m-%dT%H:%M:%S"
_TIME_RE = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}$")

# Steem mainnet's chain ID is 32 zero bytes.
CHAIN_ID = bytes(32)


class SteemTxError(ValueError):
    """Malformed transaction content (unsupported op, bad asset, bad key...)."""


class UnsupportedOperation(SteemTxError):
    """An operation type this signer cannot encode; a proposal containing one
    can never be verified and is never approved."""


# --------------------------------------------------------------------------
# JSON-RPC
# --------------------------------------------------------------------------

_TRANSIENT = re.compile(r"(?i)upstream|circuit breaker|timeout|unavailable|too many requests|bad cast")


class ChainError(RuntimeError):
    """A rejection from the Steem chain itself (not a transport problem); it is
    never retried on another node."""


class RPC:
    """Calls Steem nodes in order, moving to the next only on transport errors
    (own node first is best)."""

    def __init__(self, nodes: list[str], timeout: float = 20.0):
        self.nodes = [n.strip() for n in nodes if n and n.strip()]
        self._client = httpx.Client(timeout=timeout)

    def close(self) -> None:
        self._client.close()

    def call(self, method: str, params: list) -> Any:
        if not self.nodes:
            raise RuntimeError("steemtx: no Steem RPC nodes configured")
        body = {"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
        last: Optional[BaseException] = None
        for node in self.nodes:
            try:
                data = self._client.post(node, json=body).json()
            except Exception as exc:  # noqa: BLE001 - transport or non-JSON reply: try the next node
                last = exc
                continue
            err = data.get("error") if isinstance(data, dict) else None
            if err is not None:
                message = str(err.get("message", "")) if isinstance(err, dict) else str(err)
                if _TRANSIENT.search(message):
                    last = RuntimeError(f"{node}: {message}")
                    continue
                raise ChainError(message)
            return data.get("result")
        raise RuntimeError(f"steem rpc: every node failed: {last}")


# --------------------------------------------------------------------------
# Binary writer
# --------------------------------------------------------------------------


def parse_time(s: str) -> datetime:
    """Parses Steem's UTC timestamp exactly (Go's 2006-01-02T15:04:05)."""
    if not isinstance(s, str) or not _TIME_RE.match(s):
        raise SteemTxError(f"bad time {s!r}")
    return datetime.strptime(s, TIME_LAYOUT).replace(tzinfo=timezone.utc)


def format_time(t: datetime) -> str:
    return t.astimezone(timezone.utc).strftime(TIME_LAYOUT)


def _utf8(s: str) -> bytes:
    try:
        return s.encode("utf-8")
    except UnicodeEncodeError:
        # Lone surrogates from JSON escapes: Go's decoder turns them into U+FFFD.
        return "".join("�" if 0xD800 <= ord(c) <= 0xDFFF else c for c in s).encode("utf-8")


class Writer:
    """Steem's compact little-endian binary encoding."""

    def __init__(self) -> None:
        self.buf = bytearray()

    def u8(self, v: int) -> None:
        self.buf += struct.pack("<B", v)

    def u16(self, v: int) -> None:
        self.buf += struct.pack("<H", v)

    def u32(self, v: int) -> None:
        self.buf += struct.pack("<I", v)

    def i64(self, v: int) -> None:
        self.buf += struct.pack("<q", v)

    def varint(self, v: int) -> None:
        if v < 0 or v > 0xFFFFFFFF:
            raise SteemTxError(f"varint out of range: {v}")
        while v >= 0x80:
            self.buf.append((v & 0x7F) | 0x80)
            v >>= 7
        self.buf.append(v)

    def string(self, s: str) -> None:
        b = _utf8(s)
        self.varint(len(b))
        self.buf += b

    def time(self, s: str) -> None:
        self.u32(timegm(parse_time(s).timetuple()))

    def asset(self, a: "Asset") -> None:
        self.i64(a.amount)
        self.u8(a.precision)
        self.buf += a.symbol.encode("ascii").ljust(7, b"\x00")[:7]

    def authority(self, a: "Authority") -> None:
        self.u32(a.weight_threshold)
        self.varint(len(a.account_auths))
        for name, weight in a.account_auths:
            self.string(name)
            self.u16(weight)
        self.varint(len(a.key_auths))
        for key, weight in a.key_auths:
            self.buf += decode_public_key(key)
            self.u16(weight)

    def opt_authority(self, a: Optional["Authority"]) -> None:
        if a is None:
            self.u8(0)
        else:
            self.u8(1)
            self.authority(a)


# --------------------------------------------------------------------------
# Assets
# --------------------------------------------------------------------------

_PRECISIONS = {"STEEM": 3, "SBD": 3, "VESTS": 6}


@dataclass(frozen=True)
class Asset:
    """An amount in smallest units: "1.000 STEEM" is Asset(1000, 3, "STEEM")."""

    amount: int
    precision: int
    symbol: str

    def __str__(self) -> str:
        p = 10**self.precision
        return f"{self.amount // p}.{self.amount % p:0{self.precision}d} {self.symbol}"


def parse_asset(s: str) -> Asset:
    """Parses "12.500 SBD" exactly: exactly the symbol's decimals, non-negative."""
    if not isinstance(s, str):
        raise SteemTxError(f"bad asset {s!r}")
    f = s.split()
    if len(f) != 2:
        raise SteemTxError(f"bad asset {s!r}")
    prec = _PRECISIONS.get(f[1])
    if prec is None:
        raise SteemTxError(f"unknown symbol in {s!r}")
    whole, dot, frac = f[0].partition(".")
    if not dot or len(frac) != prec or whole == "" or not (whole + frac).isascii() or not (whole + frac).isdigit():
        raise SteemTxError(f"{s!r} needs a non-negative amount with exactly {prec} decimals")
    n = int(whole + frac)
    if n > 0x7FFFFFFFFFFFFFFF:
        raise SteemTxError(f"{s!r} out of range")
    return Asset(n, prec, f[1])


def format_milli(milli: int, symbol: str) -> str:
    """12500, "SBD" -> "12.500 SBD"."""
    return f"{milli // 1000}.{milli % 1000:03d} {symbol}"


# --------------------------------------------------------------------------
# Authorities
# --------------------------------------------------------------------------


@dataclass
class Authority:
    """A Steem owner/active/posting authority. account_auths/key_auths are
    lists of (name-or-STM-key, weight)."""

    weight_threshold: int = 0
    account_auths: list[tuple[str, int]] = field(default_factory=list)
    key_auths: list[tuple[str, int]] = field(default_factory=list)

    def sorted(self) -> "Authority":
        """account_auths in chain order (byte order of the name). key_auths are
        left as given: the chain orders them by binary key, and the svm.bank
        signer keeps them empty."""
        return Authority(
            self.weight_threshold,
            sorted(self.account_auths, key=lambda a: a[0].encode("utf-8")),
            list(self.key_auths),
        )

    def total(self) -> int:
        return sum(w for _, w in self.account_auths) + sum(w for _, w in self.key_auths)

    def weight_of(self, name: str) -> int:
        for n, w in self.account_auths:
            if n == name:
                return w
        return 0

    def serialize(self) -> bytes:
        w = Writer()
        w.authority(self)
        return bytes(w.buf)

    def equal(self, other: "Authority") -> bool:
        """Whether the two serialize identically."""
        try:
            return self.serialize() == other.serialize()
        except (SteemTxError, struct.error):
            return False

    def to_json(self) -> dict:
        return {
            "weight_threshold": self.weight_threshold,
            "account_auths": [[n, w] for n, w in self.account_auths],
            "key_auths": [[k, w] for k, w in self.key_auths],
        }

    @staticmethod
    def from_json(obj: Any) -> "Authority":
        if not isinstance(obj, dict):
            raise SteemTxError("authority must be an object")

        def pairs(key: str) -> list[tuple[str, int]]:
            raw = obj.get(key)
            if raw is None:
                return []
            if not isinstance(raw, list):
                raise SteemTxError(f"{key} must be a list")
            out = []
            for p in raw:
                if not (isinstance(p, list) and len(p) == 2 and isinstance(p[0], str)):
                    raise SteemTxError(f"bad {key} entry {p!r}")
                out.append((p[0], _uint(p[1], 16)))
            return out

        return Authority(_uint(obj.get("weight_threshold", 0), 32), pairs("account_auths"), pairs("key_auths"))


def _uint(v: Any, bits: int) -> int:
    if isinstance(v, bool) or not isinstance(v, int) or v < 0 or v >= 1 << bits:
        raise SteemTxError(f"expected a uint{bits}, got {v!r}")
    return v


def _str(obj: dict, key: str) -> str:
    v = obj.get(key, "")
    if v is None:
        return ""
    if not isinstance(v, str):
        raise SteemTxError(f"{key} must be a string")
    return v


def _opt_auth(obj: dict, key: str) -> Optional[Authority]:
    v = obj.get(key)
    return None if v is None else Authority.from_json(v)


# --------------------------------------------------------------------------
# Operations
# --------------------------------------------------------------------------


@dataclass(frozen=True)
class Transfer:
    from_: str
    to: str
    amount: str
    memo: str


@dataclass(frozen=True)
class TransferToVesting:
    from_: str
    to: str
    amount: str


@dataclass(frozen=True)
class WithdrawVesting:
    account: str
    vesting_shares: str


@dataclass
class AccountUpdate:
    account: str
    owner: Optional[Authority]
    active: Optional[Authority]
    posting: Optional[Authority]
    memo_key: str
    json_metadata: str


@dataclass
class ChangeRecoveryAccount:
    account_to_recover: str
    new_recovery_account: str


@dataclass
class AccountUpdate2:
    account: str
    json_metadata: str
    posting_json_metadata: str
    owner: Optional[Authority] = None
    active: Optional[Authority] = None
    posting: Optional[Authority] = None
    memo_key: str = ""


OpValue = Union[Transfer, TransferToVesting, WithdrawVesting, AccountUpdate, ChangeRecoveryAccount, AccountUpdate2]


@dataclass
class Operation:
    type: str
    value: OpValue

    def to_json(self) -> list:
        v = self.value
        if isinstance(v, Transfer):
            body: dict = {"from": v.from_, "to": v.to, "amount": v.amount, "memo": v.memo}
        elif isinstance(v, TransferToVesting):
            body = {"from": v.from_, "to": v.to, "amount": v.amount}
        elif isinstance(v, WithdrawVesting):
            body = {"account": v.account, "vesting_shares": v.vesting_shares}
        elif isinstance(v, AccountUpdate):
            body = {"account": v.account}
            for k in ("owner", "active", "posting"):
                a = getattr(v, k)
                if a is not None:
                    body[k] = a.to_json()
            body["memo_key"] = v.memo_key
            body["json_metadata"] = v.json_metadata
        elif isinstance(v, ChangeRecoveryAccount):
            body = {"account_to_recover": v.account_to_recover, "new_recovery_account": v.new_recovery_account, "extensions": []}
        elif isinstance(v, AccountUpdate2):
            body = {"account": v.account}
            for k in ("owner", "active", "posting"):
                a = getattr(v, k)
                if a is not None:
                    body[k] = a.to_json()
            if v.memo_key:
                body["memo_key"] = v.memo_key
            body["json_metadata"] = v.json_metadata
            body["posting_json_metadata"] = v.posting_json_metadata
            body["extensions"] = []
        else:
            raise UnsupportedOperation(self.type)
        return [self.type, body]

    @staticmethod
    def from_json(raw: Any) -> "Operation":
        if not (isinstance(raw, list) and len(raw) == 2 and isinstance(raw[0], str)):
            raise SteemTxError("an operation is [type, {fields}]")
        t, b = raw
        if b is None:
            b = {}
        if not isinstance(b, dict):
            raise SteemTxError("operation fields must be an object")
        if t == "transfer":
            return Operation(t, Transfer(_str(b, "from"), _str(b, "to"), _str(b, "amount"), _str(b, "memo")))
        if t == "transfer_to_vesting":
            return Operation(t, TransferToVesting(_str(b, "from"), _str(b, "to"), _str(b, "amount")))
        if t == "withdraw_vesting":
            return Operation(t, WithdrawVesting(_str(b, "account"), _str(b, "vesting_shares")))
        if t == "account_update":
            return Operation(t, AccountUpdate(_str(b, "account"), _opt_auth(b, "owner"), _opt_auth(b, "active"),
                                              _opt_auth(b, "posting"), _str(b, "memo_key"), _str(b, "json_metadata")))
        if t == "change_recovery_account":
            return Operation(t, ChangeRecoveryAccount(_str(b, "account_to_recover"), _str(b, "new_recovery_account")))
        if t == "account_update2":
            return Operation(t, AccountUpdate2(_str(b, "account"), _str(b, "json_metadata"), _str(b, "posting_json_metadata"),
                                               _opt_auth(b, "owner"), _opt_auth(b, "active"), _opt_auth(b, "posting"),
                                               _str(b, "memo_key")))
        raise UnsupportedOperation(t)

    def write(self, w: Writer) -> None:
        v = self.value
        if isinstance(v, Transfer):
            w.varint(2)
            w.string(v.from_)
            w.string(v.to)
            w.asset(parse_asset(v.amount))
            w.string(v.memo)
        elif isinstance(v, TransferToVesting):
            w.varint(3)
            w.string(v.from_)
            w.string(v.to)
            w.asset(parse_asset(v.amount))
        elif isinstance(v, WithdrawVesting):
            w.varint(4)
            w.string(v.account)
            w.asset(parse_asset(v.vesting_shares))
        elif isinstance(v, AccountUpdate):
            w.varint(10)
            w.string(v.account)
            for a in (v.owner, v.active, v.posting):
                w.opt_authority(a)
            w.buf += decode_public_key(v.memo_key)
            w.string(v.json_metadata)
        elif isinstance(v, ChangeRecoveryAccount):
            w.varint(26)
            w.string(v.account_to_recover)
            w.string(v.new_recovery_account)
            w.varint(0)
        elif isinstance(v, AccountUpdate2):
            w.varint(43)
            w.string(v.account)
            for a in (v.owner, v.active, v.posting):
                w.opt_authority(a)
            if v.memo_key == "":
                w.u8(0)
            else:
                w.u8(1)
                w.buf += decode_public_key(v.memo_key)
            w.string(v.json_metadata)
            w.string(v.posting_json_metadata)
            w.varint(0)
        else:
            raise UnsupportedOperation(self.type)


# --------------------------------------------------------------------------
# Transactions
# --------------------------------------------------------------------------


@dataclass
class DynamicProps:
    head_block_number: int
    head_block_id: str
    time: str

    @staticmethod
    def from_json(obj: dict) -> "DynamicProps":
        return DynamicProps(int(obj["head_block_number"]), str(obj["head_block_id"]), str(obj["time"]))


@dataclass
class Transaction:
    ref_block_num: int = 0
    ref_block_prefix: int = 0
    expiration: str = ""
    operations: list[Operation] = field(default_factory=list)
    signatures: list[str] = field(default_factory=list)

    def serialize(self) -> bytes:
        """The transaction without signatures."""
        w = Writer()
        w.u16(self.ref_block_num)
        w.u32(self.ref_block_prefix)
        w.time(self.expiration)
        w.varint(len(self.operations))
        for op in self.operations:
            op.write(w)
        w.varint(0)  # extensions
        return bytes(w.buf)

    def digest(self) -> bytes:
        """What validators sign: sha256(chain_id || serialized tx)."""
        return hashlib.sha256(CHAIN_ID + self.serialize()).digest()

    def id(self) -> str:
        """The Steem trx_id and the proposal ID: hex(sha256(serialized)[:20])."""
        return hashlib.sha256(self.serialize()).digest()[:20].hex()

    def to_json(self) -> dict:
        out: dict = {
            "ref_block_num": self.ref_block_num,
            "ref_block_prefix": self.ref_block_prefix,
            "expiration": self.expiration,
            "operations": [op.to_json() for op in self.operations],
            "extensions": [],
        }
        if self.signatures:
            out["signatures"] = list(self.signatures)
        return out

    @staticmethod
    def from_json(obj: Any) -> "Transaction":
        if not isinstance(obj, dict):
            raise SteemTxError("a transaction must be an object")
        ops = obj.get("operations") or []
        if not isinstance(ops, list):
            raise SteemTxError("operations must be a list")
        sigs = obj.get("signatures") or []
        if not (isinstance(sigs, list) and all(isinstance(s, str) for s in sigs)):
            raise SteemTxError("signatures must be strings")
        return Transaction(
            ref_block_num=_uint(obj.get("ref_block_num", 0), 16),
            ref_block_prefix=_uint(obj.get("ref_block_prefix", 0), 32),
            expiration=_str(obj, "expiration"),
            operations=[Operation.from_json(op) for op in ops],
            signatures=list(sigs),
        )


def new_transaction(props: DynamicProps, ops: list[Operation], ttl: timedelta) -> Transaction:
    """References the head block and expires ttl after the head block time.
    Proposals use 58 minutes; Steem rejects anything over 60."""
    block_id = bytes.fromhex(props.head_block_id)
    if len(block_id) < 8:
        raise SteemTxError("head_block_id too short")
    return Transaction(
        ref_block_num=props.head_block_number & 0xFFFF,
        ref_block_prefix=int.from_bytes(block_id[4:8], "little"),
        expiration=format_time(parse_time(props.time) + ttl),
        operations=ops,
    )


# --------------------------------------------------------------------------
# Keys
# --------------------------------------------------------------------------

_B58 = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
_B58_INDEX = {c: i for i, c in enumerate(_B58)}


def b58encode(b: bytes) -> str:
    n = int.from_bytes(b, "big")
    out = ""
    while n:
        n, r = divmod(n, 58)
        out = _B58[r] + out
    pad = len(b) - len(b.lstrip(b"\x00"))
    return "1" * pad + out


def b58decode(s: str) -> bytes:
    n = 0
    for c in s:
        if c not in _B58_INDEX:
            raise SteemTxError("invalid base58")
        n = n * 58 + _B58_INDEX[c]
    body = n.to_bytes((n.bit_length() + 7) // 8, "big") if n else b""
    pad = len(s) - len(s.lstrip("1"))
    return b"\x00" * pad + body


def decode_wif(wif: str) -> bytes:
    """base58(0x80 || 32-byte key || first 4 bytes of sha256(sha256(0x80 || key)))."""
    raw = b58decode(wif.strip())
    if len(raw) != 37 or raw[0] != 0x80:
        raise SteemTxError("not a WIF private key")
    check = hashlib.sha256(hashlib.sha256(raw[:33]).digest()).digest()[:4]
    if check != raw[33:]:
        raise SteemTxError("bad WIF checksum")
    PrivateKey(raw[1:33])  # rejects zero / out-of-range keys
    return raw[1:33]


def decode_public_key(s: str) -> bytes:
    """"STM" + base58(33-byte key || first 4 bytes of ripemd160(key)) -> 33 bytes."""
    if not isinstance(s, str) or not s.startswith("STM"):
        raise SteemTxError("public key must start with STM")
    raw = b58decode(s[3:])
    if len(raw) != 37:
        raise SteemTxError("bad public key length")
    if ripemd160(raw[:33])[:4] != raw[33:]:
        raise SteemTxError("bad public key checksum")
    return raw[:33]


def encode_public_key(compressed: bytes) -> str:
    return "STM" + b58encode(compressed + ripemd160(compressed)[:4])


def public_key_of(private_key: bytes) -> str:
    return encode_public_key(PrivateKey(private_key).public_key.format(compressed=True))


# --------------------------------------------------------------------------
# Signatures
# --------------------------------------------------------------------------


def is_canonical(rs: bytes) -> bool:
    """Steem's byte rule on r (rs[0:32]) and s (rs[32:64]): no high bit in the
    first byte, and no leading 0x00 unless the next byte has its high bit set."""
    return (
        not rs[0] & 0x80
        and not (rs[0] == 0 and not rs[1] & 0x80)
        and not rs[32] & 0x80
        and not (rs[32] == 0 and not rs[33] & 0x80)
    )


def sign(private_key: bytes, digest: bytes) -> str:
    """A Steem compact recoverable signature of digest, as hex: header byte
    (27 + 4 + recovery id, so 0x1f or 0x20) then r then s. libsecp256k1 always
    returns low-S; about half of all signatures fail the byte rule, so this
    retries with RFC 6979 extra entropy until one passes."""
    if len(digest) != 32:
        raise ValueError("digest must be 32 bytes")
    key = PrivateKey(private_key)
    for attempt in range(256):
        if attempt == 0:
            nonce = (ffi.NULL, ffi.NULL)
        else:
            extra = ffi.new("unsigned char[32]", hashlib.sha256(digest + attempt.to_bytes(4, "little")).digest())
            nonce = (ffi.NULL, extra)
        sig = key.sign_recoverable(digest, hasher=None, custom_nonce=nonce)
        rs, recid = sig[:64], sig[64]
        if is_canonical(rs):
            return (bytes([27 + 4 + recid]) + rs).hex()
    raise RuntimeError("steemtx: no canonical signature found")


def recover_signer(digest: bytes, sig_hex: str) -> str:
    """The STM public key that produced sig_hex over digest. Non-canonical
    signatures are rejected, exactly as the chain does."""
    try:
        sig = bytes.fromhex(sig_hex)
    except (ValueError, TypeError) as exc:
        raise SteemTxError("signature must be 65 bytes of hex") from exc
    if len(sig) != 65:
        raise SteemTxError("signature must be 65 bytes of hex")
    if not is_canonical(sig[1:]):
        raise SteemTxError("signature is not canonical")
    header = sig[0]
    if header < 27 or header > 34:
        raise SteemTxError("bad signature header")
    recid = (header - 27) & 3
    try:
        pub = PublicKey.from_signature_and_message(sig[1:] + bytes([recid]), digest, hasher=None)
    except Exception as exc:  # noqa: BLE001 - libsecp256k1 rejected it
        raise SteemTxError(f"cannot recover signer: {exc}") from exc
    return encode_public_key(pub.format(compressed=True))
