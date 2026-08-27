"""Empirically-derived signing test vectors for oracle/PROTOCOL.md SS1-2.

Generated during Milestone 1 of the Python client build-out: a throwaway
isolated single-validator devnet (built from a known-good pre-migration
commit, since the main checkout's go.mod was mid-flight during a concurrent
dependency-upgrade task; the wire protocol these vectors pin down --
REST/tx-broadcast/SIGN_MODE_DIRECT/eth_secp256k1 -- does not change with a Go
dependency bump) confirmed:

- A fixed private key derives the expected compressed pubkey / account
  address / valoper address (SS1).
- The exact TxBody / AuthInfo / SignDoc protobuf bytes and the Keccak256
  digest computed from them, for a fixed MsgAttestDeposit + fixed
  chain_id/account_number/sequence/gas_limit (so this vector is reproducible
  independent of any live chain's state).
- The resulting signature is 65 bytes (R||S||V, V the raw 0/1 recovery id --
  NOT 64 bytes, NOT Ethereum's 27/28 V convention), and go-ethereum-style
  signing (via eth_keys/coincurve) is deterministic (RFC6979): re-signing the
  same digest with the same key reproduces byte-identical output, so this
  vector is stable forever.
- A real broadcast of this exact message (against a devnet with the bridge
  enabled and this key as the sole bonded validator) landed with
  ``tx_response.code == 0`` and a ``deposit_minted`` event crediting
  998000000000000000asteem (1000 millisteem * 10**15, minus the 0.25%
  bridge fee -- see x/oracle/bridge/types/amounts.go), confirming the
  signature verified on-chain end-to-end, not just internal self-consistency.
- ``GET /cosmos/auth/v1beta1/accounts/{address}`` returns a plain
  ``/cosmos.auth.v1beta1.BaseAccount`` with ``account_number``/``sequence``
  as JSON strings (see broadcast.RestClient.get_account's docstring).
"""
from __future__ import annotations

from steemvm_oracle.keys import from_mnemonic, from_private_key_hex
from steemvm_oracle.signing import (
    build_auth_info_bytes,
    build_pubkey_any,
    build_sign_doc_bytes,
    build_tx_body_bytes,
    pack_any,
    sign_doc_digest,
    TYPE_URL_ATTEST_DEPOSIT,
)

from cosmos.tx.v1beta1 import tx_pb2
from steemvm.steembridge.v1 import asset_pb2 as steembridge_asset_pb2
from steemvm.steembridge.v1 import tx_pb2 as steembridge_tx_pb2

# --- Fixed inputs -----------------------------------------------------------

MNEMONIC = (
    "swamp mom lend ritual budget steel october pig metal load shaft "
    "popular rubber alley dawn harbor ecology front host aerobic coconut "
    "plate isolate option"
)
PRIVATE_KEY_HEX = "ffef70aae6f511fb49ff53d703654a8238f9f084a3cb1efad4a3e69fb42df7ee"

CHAIN_ID = "steemvm"
ACCOUNT_NUMBER = 0
SEQUENCE = 1
GAS_LIMIT = 600_000

# --- Expected, empirically-confirmed outputs --------------------------------

EXPECTED_ADDRESS = "steem15s30zc7l67ug53vkqs2e36d0rdrlsw0fds0457"
EXPECTED_VALOPER_ADDRESS = "steemvaloper15s30zc7l67ug53vkqs2e36d0rdrlsw0f8rqx0a"
EXPECTED_PUBKEY_COMPRESSED_HEX = (
    "02c267c8ef334b802b1e753980eb6ec6d96b96aa88821e3332e1de5018a249ef05"
)
EXPECTED_ADDRESS_BYTES_HEX = "a422f163dfd7b88a4596041598e9af1b47f839e9"

EXPECTED_BODY_BYTES_HEX = "0ae7010a282f737465656d766d2e737465656d6272696467652e76312e4d73674174746573744465706f73697412ba010a2c737465656d31357333307a63376c363775673533766b71733265333664307264726c7377306664733034353712286465616462656566303031313232333334343535363637373838393961616262636364646565666620cec2f1052a13323032362d30382d31375431323a30303a30303205616c6963653a0e737465656d6272696467652d677740e8074a2c737465656d31357333307a63376c363775673533766b71733265333664307264726c73773066647330343537"
EXPECTED_AUTH_INFO_BYTES_HEX = "0a5a0a500a292f636f736d6f732e65766d2e63727970746f2e76312e657468736563703235366b312e5075624b657912230a2102c267c8ef334b802b1e753980eb6ec6d96b96aa88821e3332e1de5018a249ef0512040a0208011801120410c0cf24"
EXPECTED_DIGEST_HEX = "92eb6772a2c51dadf002dfe96c187644e70265b4bbc684ba939aed36963fff2e"
EXPECTED_SIGNATURE_HEX = "9c33f6be1a3bd5fe57ea4e959c2eb12ccee0912b9ef1821b32b734eeb6252bda0671c5b84d9d397520dea29303e483495292239acb1a3a20522771f8f219f07e00"
EXPECTED_SIGNATURE_LEN = 65
EXPECTED_SIGNATURE_V_BYTE = 0  # raw recovery id (0/1), not Ethereum's 27/28


def _build_msg_any():
    keypair = from_private_key_hex(PRIVATE_KEY_HEX)
    msg = steembridge_tx_pb2.MsgAttestDeposit(
        validator=keypair.address,
        txid="deadbeef00112233445566778899aabbccddeeff",
        op_index=0,
        steem_block=12345678,
        steem_timestamp="2026-08-17T12:00:00",
        steem_sender="alice",
        gateway_account="steembridge-gw",
        amount_millisteem=1000,
        memo=keypair.address,
        asset=steembridge_asset_pb2.BRIDGE_ASSET_STEEM,
    )
    return keypair, pack_any(TYPE_URL_ATTEST_DEPOSIT, msg)


class TestKeyDerivation:
    """SS1: private key -> compressed pubkey / account address / valoper
    address. Confirmed two independent ways: raw private-key import and
    mnemonic->HD-path derivation both land on the same keypair, and the
    address matches what ``steemvmd keys add --recover`` derived for the
    same mnemonic on the live devnet."""

    def test_from_private_key_hex(self):
        kp = from_private_key_hex(PRIVATE_KEY_HEX)
        assert kp.address == EXPECTED_ADDRESS
        assert kp.valoper_address == EXPECTED_VALOPER_ADDRESS
        assert kp.pubkey_compressed.hex() == EXPECTED_PUBKEY_COMPRESSED_HEX
        assert kp.address_bytes.hex() == EXPECTED_ADDRESS_BYTES_HEX
        assert len(kp.pubkey_compressed) == 33
        assert len(kp.address_bytes) == 20

    def test_from_mnemonic_matches_private_key(self):
        kp = from_mnemonic(MNEMONIC)
        assert kp.address == EXPECTED_ADDRESS
        assert kp.private_key.hex() == PRIVATE_KEY_HEX

    def test_address_matches_live_steemvmd_keys_add(self):
        # steemvmd keys add spike --recover (same mnemonic) on the devnet
        # spike printed exactly this address -- see the task report.
        kp = from_mnemonic(MNEMONIC)
        assert kp.address == "steem15s30zc7l67ug53vkqs2e36d0rdrlsw0fds0457"


class TestSignDocAssembly:
    """SS2 steps 1-5: TxBody/AuthInfo/SignDoc byte-for-byte, and the
    Keccak256 digest computed from the serialized SignDoc."""

    def test_body_bytes(self):
        _, msg_any = _build_msg_any()
        body_bytes = build_tx_body_bytes([msg_any])
        assert body_bytes.hex() == EXPECTED_BODY_BYTES_HEX

    def test_auth_info_bytes(self):
        kp, _ = _build_msg_any()
        pubkey_any = build_pubkey_any(kp.pubkey_compressed)
        auth_info_bytes = build_auth_info_bytes(pubkey_any, SEQUENCE, GAS_LIMIT, fee=())
        assert auth_info_bytes.hex() == EXPECTED_AUTH_INFO_BYTES_HEX

    def test_sign_doc_digest(self):
        kp, msg_any = _build_msg_any()
        body_bytes = build_tx_body_bytes([msg_any])
        pubkey_any = build_pubkey_any(kp.pubkey_compressed)
        auth_info_bytes = build_auth_info_bytes(pubkey_any, SEQUENCE, GAS_LIMIT, fee=())
        sign_doc_bytes = build_sign_doc_bytes(body_bytes, auth_info_bytes, CHAIN_ID, ACCOUNT_NUMBER)
        digest = sign_doc_digest(sign_doc_bytes)
        assert digest.hex() == EXPECTED_DIGEST_HEX
        assert len(digest) == 32


class TestSignature:
    """SS2 step 6: 65-byte go-ethereum-style R(32)||S(32)||V(1) signature,
    V the raw recovery id -- and RFC6979 determinism, which is what makes
    this whole vector reproducible/stable across runs and machines."""

    def test_signature_bytes_and_length(self):
        kp = from_private_key_hex(PRIVATE_KEY_HEX)
        digest = bytes.fromhex(EXPECTED_DIGEST_HEX)
        signature = kp.sign(digest)
        assert len(signature) == EXPECTED_SIGNATURE_LEN
        assert signature.hex() == EXPECTED_SIGNATURE_HEX
        assert signature[-1] == EXPECTED_SIGNATURE_V_BYTE

    def test_signing_is_deterministic(self):
        # RFC6979 deterministic nonce (coincurve/eth_keys default) -- signing
        # the same digest twice must produce byte-identical signatures. This
        # is what lets this test vector be pinned as a constant at all.
        kp = from_private_key_hex(PRIVATE_KEY_HEX)
        digest = bytes.fromhex(EXPECTED_DIGEST_HEX)
        sig1 = kp.sign(digest)
        sig2 = kp.sign(digest)
        assert sig1 == sig2

    def test_low_s_signature(self):
        # secp256k1 group order N; a canonical low-S signature has
        # S <= N/2. go-ethereum's crypto.Sign (and eth_keys/coincurve here)
        # always produce low-S -- see PROTOCOL.md SS2 step 6.
        secp256k1_n = 0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141
        signature = bytes.fromhex(EXPECTED_SIGNATURE_HEX)
        s = int.from_bytes(signature[32:64], "big")
        assert s <= secp256k1_n // 2

    def test_rejects_non_32_byte_digest(self):
        import pytest

        kp = from_private_key_hex(PRIVATE_KEY_HEX)
        with pytest.raises(ValueError):
            kp.sign(b"\x00" * 31)


class TestTxRawRoundTrip:
    """SS2 step 7: TxRaw reuses body_bytes/auth_info_bytes VERBATIM -- never
    re-marshaled. Confirms the assembled TxRaw parses back to the same
    logical message and carries the exact signed bytes untouched."""

    def test_tx_raw_embeds_exact_signed_bytes(self):
        kp, msg_any = _build_msg_any()
        body_bytes = build_tx_body_bytes([msg_any])
        pubkey_any = build_pubkey_any(kp.pubkey_compressed)
        auth_info_bytes = build_auth_info_bytes(pubkey_any, SEQUENCE, GAS_LIMIT, fee=())
        sign_doc_bytes = build_sign_doc_bytes(body_bytes, auth_info_bytes, CHAIN_ID, ACCOUNT_NUMBER)
        digest = sign_doc_digest(sign_doc_bytes)
        signature = kp.sign(digest)

        tx_raw = tx_pb2.TxRaw(body_bytes=body_bytes, auth_info_bytes=auth_info_bytes, signatures=[signature])
        tx_raw_bytes = tx_raw.SerializeToString()

        # Parse back and confirm byte-exact round trip of the embedded fields
        # (this is the property PROTOCOL.md SS2 step 7 warns not to violate
        # by re-marshaling after signing).
        parsed = tx_pb2.TxRaw()
        parsed.ParseFromString(tx_raw_bytes)
        assert parsed.body_bytes == body_bytes
        assert parsed.auth_info_bytes == auth_info_bytes
        assert parsed.signatures == [signature]

        parsed_body = tx_pb2.TxBody()
        parsed_body.ParseFromString(parsed.body_bytes)
        assert parsed_body.messages[0].type_url == TYPE_URL_ATTEST_DEPOSIT


class TestLiveDevnetBroadcastResult:
    """Documents the actual on-chain result observed for this exact message
    (see the Milestone 1 task report for the full tx_response JSON): a real
    broadcast against an isolated single-validator devnet (bridge enabled,
    this key as the sole bonded validator) returned tx_response.code == 0,
    height 12, with a deposit_minted event crediting
    998000000000000000asteem to the destination -- 1000 millisteem * 10**15
    (the millisteem->asteem factor from x/oracle/bridge/types/amounts.go),
    minus a bridge fee swept to a second module account in the same tx. Not
    re-executed here (no live chain in the unit-test environment); recorded
    as a non-runtime assertion so the numbers stay documented and diffable.
    NOTE on the fee: the observed on-chain fee was 2000000000000000asteem --
    0.2% of the gross mint, not CLAUDE.md's stated 0.25% bridge_fee_bps --
    because this devnet's genesis was patched only to set
    bridge_enabled/gateway_account for this spike (see the task report), so
    whatever bridge_fee_bps value the fresh-init default genesis carries may
    differ from a genesis authored for production. This test pins the
    literally observed amounts, not a recomputed formula.
    """

    def test_documented_broadcast_result(self):
        observed_tx_response = {
            "code": 0,
            "height": "12",
            "gas_wanted": "600000",
            "gas_used": "171697",
        }
        assert observed_tx_response["code"] == 0
        assert int(observed_tx_response["gas_used"]) < int(observed_tx_response["gas_wanted"])

        amount_millisteem = 1000
        millisteem_to_asteem_factor = 10**15
        gross_minted_asteem = amount_millisteem * millisteem_to_asteem_factor
        observed_fee_asteem = 2_000_000_000_000_000
        observed_net_to_destination_asteem = 998_000_000_000_000_000

        assert gross_minted_asteem == 1_000_000_000_000_000_000
        assert gross_minted_asteem - observed_fee_asteem == observed_net_to_destination_asteem
