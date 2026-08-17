"""Key derivation and bech32 addressing for the SteemVM ``eth_secp256k1``
chain key type. Mirrors ``oracle/PROTOCOL.md`` SS1 exactly:

- Private key: 32 raw bytes.
- Public key: **compressed** secp256k1 point, 33 bytes -- the wire form of
  ``ethsecp256k1.PubKey.Key``.
- Account address: ``Keccak256(uncompressed_pubkey[1:65])[12:32]`` -- drop
  the pubkey's ``0x04`` prefix byte, Keccak256-hash the remaining 64 bytes
  (X||Y), take the last 20 bytes. Standard Ethereum address derivation, no
  chain-specific twist.
- String form: bech32-encode those 20 bytes with HRP ``steem`` (account) or
  ``steemvaloper`` (validator operator -- same 20 bytes, different HRP;
  see ``app/config.go``). Standard bech32, not bech32m.
- Mnemonic-derived keys: BIP39 -> BIP32 path ``m/44'/60'/0'/0/0`` (standard
  Ethereum HD derivation, ``ChainCoinType = 60``), identical to
  MetaMask/``eth-account``/any stock Ethereum HD wallet.
"""

from __future__ import annotations

from dataclasses import dataclass

import bech32
from eth_account import Account as _EthAccount
from eth_keys import keys as eth_keys
from eth_utils import keccak

ACCOUNT_HRP = "steem"
VALOPER_HRP = "steemvaloper"

# Standard Ethereum HD path (BIP44 coin type 60, account/change/index 0) --
# see oracle/PROTOCOL.md SS1 and app/app.go's ChainCoinType = 60.
HD_PATH = "m/44'/60'/0'/0/0"

# eth_account gates mnemonic support behind this flag; we only ever use it to
# reproduce a standard BIP39->BIP32 derivation, never to send an Ethereum tx.
_EthAccount.enable_unaudited_hdwallet_features()


@dataclass(frozen=True)
class Keypair:
    private_key: bytes  # 32 raw bytes
    pubkey_compressed: bytes  # 33 bytes, compressed secp256k1
    address_bytes: bytes  # 20 bytes
    address: str  # bech32 "steem1..." -- the account address (tx signer, msg.Validator)
    valoper_address: str  # bech32 "steemvaloper1..." -- same 20 bytes, operator HRP

    def sign(self, digest: bytes) -> bytes:
        """Signs a 32-byte digest go-ethereum style: 65 bytes
        R(32) || S(32) || V(1), V the raw 0/1 recovery id.
        See oracle/PROTOCOL.md SS2 step 6."""
        return sign_digest(self.private_key, digest)


def sign_digest(private_key: bytes, digest: bytes) -> bytes:
    """ECDSA-signs a 32-byte digest with the secp256k1 private key,
    go-ethereum ``crypto.Sign`` style: 65 bytes ``R(32) || S(32) || V(1)``,
    V the raw recovery id (0 or 1), NOT Ethereum's 27/28 convention.
    ``eth_keys`` (coincurve-backed) produces this exact layout and a
    canonical low-S signature by default -- see
    ``tests/test_signing_vectors.py`` for the empirical confirmation.
    """
    if len(digest) != 32:
        raise ValueError(f"digest must be 32 bytes, got {len(digest)}")
    priv = eth_keys.PrivateKey(private_key)
    signature = priv.sign_msg_hash(digest)
    sig_bytes = signature.to_bytes()
    if len(sig_bytes) != 65:
        raise ValueError(f"expected a 65-byte signature, got {len(sig_bytes)}")
    return sig_bytes


def _compress_pubkey(pub: eth_keys.datatypes.PublicKey) -> bytes:
    """secp256k1 point compression: 0x02/0x03 prefix (Y parity) + 32-byte X."""
    raw = pub.to_bytes()  # eth_keys already omits the 0x04 prefix: 64 bytes X(32)||Y(32)
    if len(raw) != 64:
        raise ValueError(f"unexpected uncompressed public key length: {len(raw)}")
    x, y = raw[:32], raw[32:]
    prefix = 0x02 if (y[-1] % 2 == 0) else 0x03
    return bytes([prefix]) + x


def _from_raw_private_key(raw: bytes) -> Keypair:
    if len(raw) != 32:
        raise ValueError(f"private key must be 32 bytes, got {len(raw)}")
    priv = eth_keys.PrivateKey(raw)
    pub = priv.public_key
    pubkey_compressed = _compress_pubkey(pub)
    uncompressed_no_prefix = pub.to_bytes()  # 64 bytes X||Y, no 0x04 prefix (eth_keys convention)
    address_bytes = keccak(uncompressed_no_prefix)[12:32]
    return Keypair(
        private_key=raw,
        pubkey_compressed=pubkey_compressed,
        address_bytes=address_bytes,
        address=encode_address(address_bytes, ACCOUNT_HRP),
        valoper_address=encode_address(address_bytes, VALOPER_HRP),
    )


def from_private_key_hex(hex_str: str) -> Keypair:
    """Imports a key from its 64-hex-character raw form (a leading ``0x`` is
    accepted). Mirrors ``ORACLE_PRIVATE_KEY`` in the shared ``oracle/.env``."""
    hex_str = hex_str.strip()
    if hex_str.lower().startswith("0x"):
        hex_str = hex_str[2:]
    return _from_raw_private_key(bytes.fromhex(hex_str))


def from_mnemonic(mnemonic: str) -> Keypair:
    """Derives a key from a BIP39 mnemonic via ``m/44'/60'/0'/0/0``. Mirrors
    ``ORACLE_MNEMONIC`` in the shared ``oracle/.env``."""
    account = _EthAccount.from_mnemonic(mnemonic.strip(), account_path=HD_PATH)
    return _from_raw_private_key(bytes(account.key))


def encode_address(address_bytes: bytes, hrp: str) -> str:
    if len(address_bytes) != 20:
        raise ValueError(f"address must be 20 bytes, got {len(address_bytes)}")
    encoded = bech32.bech32_encode(hrp, bech32.convertbits(address_bytes, 8, 5))
    return encoded


def decode_address(bech32_str: str) -> tuple[str, bytes]:
    """Returns (hrp, 20-byte address). Raises ValueError on malformed input."""
    hrp, data = bech32.bech32_decode(bech32_str)
    if hrp is None or data is None:
        raise ValueError(f"invalid bech32 string: {bech32_str!r}")
    raw = bech32.convertbits(data, 5, 8, False)
    if raw is None:
        raise ValueError(f"invalid bech32 payload: {bech32_str!r}")
    return hrp, bytes(raw)
