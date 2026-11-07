"""Steem transaction core: shared vectors, canonical signing, strict parsing."""

import hashlib
import json
import os

import pytest

from steemvm_oracle.multisig import steemtx
from steemvm_oracle.multisig._ripemd160 import ripemd160_pure
from steemvm_oracle.multisig.vectors import load_vectors, selftest


def test_selftest_passes_the_shared_vectors():
    selftest()


@pytest.mark.parametrize("data, digest", [
    (b"", "9c1185a5c5e9fc54612808977ee8f548b2258d31"),
    (b"abc", "8eb208f7e05d987a9b044a8e98c6b087f15a0bfc"),
    (b"a" * 1000, "aa69deee9a8922e92f8105e007f76110f381e9cf"),
])
def test_pure_ripemd160(data, digest):
    assert ripemd160_pure(data).hex() == digest


def test_pure_ripemd160_matches_across_block_boundaries():
    for n in range(0, 200, 7):
        data = os.urandom(n)
        try:
            want = hashlib.new("ripemd160", data).digest()
        except ValueError:
            pytest.skip("hashlib has no ripemd160 to compare against")
        assert ripemd160_pure(data) == want


def test_sign_recover_round_trips_are_canonical():
    priv = steemtx.decode_wif(load_vectors()["keys"][0]["wif"])
    want = steemtx.public_key_of(priv)
    for i in range(2000):
        digest = hashlib.sha256(f"round trip {i}".encode()).digest()
        sig = steemtx.sign(priv, digest)
        raw = bytes.fromhex(sig)
        assert raw[0] in (0x1F, 0x20)
        assert steemtx.is_canonical(raw[1:])
        assert steemtx.recover_signer(digest, sig) == want


def test_non_canonical_signatures_are_rejected():
    digest = hashlib.sha256(b"x").digest()
    sig = bytearray(bytes.fromhex(steemtx.sign(steemtx.decode_wif(load_vectors()["keys"][0]["wif"]), digest)))
    sig[1] |= 0x80  # high bit in r
    with pytest.raises(ValueError):
        steemtx.recover_signer(digest, sig.hex())
    with pytest.raises(ValueError):
        steemtx.recover_signer(digest, "00" * 10)


@pytest.mark.parametrize("bad", [
    "1 STEEM", "1.0 STEEM", "1.0000 STEEM", "-1.000 STEEM", "+1.000 STEEM", "1.000 steem", "1.000", ".000 STEEM",
    "1,000 STEEM", "１.000 STEEM", "1.00a STEEM", "9223372036854775.808 STEEM",
])
def test_parse_asset_is_strict(bad):
    with pytest.raises(ValueError):
        steemtx.parse_asset(bad)


def test_asset_formatting():
    assert steemtx.format_milli(12345, "STEEM") == "12.345 STEEM"
    assert str(steemtx.parse_asset("0.001 SBD")) == "0.001 SBD"
    assert steemtx.parse_asset("1.000000 VESTS").amount == 1000000


def test_account_auths_sort_in_byte_order():
    a = steemtx.Authority(1, [("b", 1), ("a.b", 1), ("a-b", 1), ("a", 1)], [])
    assert [n for n, _ in a.sorted().account_auths] == ["a", "a-b", "a.b", "b"]


def test_transaction_json_round_trip_keeps_the_id():
    for tv in load_vectors()["transactions"]:
        tx = steemtx.Transaction.from_json(tv["tx"])
        again = steemtx.Transaction.from_json(json.loads(json.dumps(tx.to_json())))
        assert again.id() == tv["id"]


@pytest.mark.parametrize("tx", [
    {"ref_block_num": 70000, "ref_block_prefix": 1, "expiration": "2026-01-01T00:00:00", "operations": []},
    {"ref_block_num": 1.0, "ref_block_prefix": 1, "expiration": "2026-01-01T00:00:00", "operations": []},
    {"ref_block_num": True, "ref_block_prefix": 1, "expiration": "2026-01-01T00:00:00", "operations": []},
    {"ref_block_num": 1, "ref_block_prefix": 1, "expiration": "2026-01-01T00:00:00", "operations": [["vote", {}]]},
])
def test_transaction_parse_rejects_bad_shapes(tx):
    with pytest.raises(ValueError):
        steemtx.Transaction.from_json(tx)


def test_time_layout_is_exact():
    with pytest.raises(ValueError):
        steemtx.parse_time("2026-1-2T3:04:05")
    with pytest.raises(ValueError):
        steemtx.parse_time("2026-01-02T03:04:05Z")
