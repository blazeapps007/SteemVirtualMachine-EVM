"""The shared conformance vectors (``oracle/multisig/vectors.json``) and the
startup selftest. The signer refuses to start unless every vector passes: a
single wrong byte in serialization would make every signature invalid, and two
signers that compute different authorities could never agree on a change.

The Docker image ships the file inside the package (the Dockerfile copies it
next to this module); a source checkout falls back to the repo copy.
"""

from __future__ import annotations

import hashlib
import json
from functools import lru_cache
from pathlib import Path

from . import steemtx
from .apportion import StakeEntry, apportion

_HERE = Path(__file__).resolve().parent


def vectors_path() -> Path:
    packaged = _HERE / "vectors.json"
    if packaged.is_file():
        return packaged
    for parent in _HERE.parents:
        candidate = parent / "oracle" / "multisig" / "vectors.json"
        if candidate.is_file():
            return candidate
        candidate = parent / "multisig" / "vectors.json"
        if candidate.is_file():
            return candidate
    raise FileNotFoundError("oracle/multisig/vectors.json not found (packaged or in the repo)")


@lru_cache(maxsize=1)
def load_vectors() -> dict:
    return json.loads(vectors_path().read_text(encoding="utf-8"))


class SelftestError(RuntimeError):
    pass


def selftest(v: dict | None = None) -> None:
    """Runs the transaction, key, signing, asset and apportionment vectors."""
    v = v if v is not None else load_vectors()
    txs, keys = v.get("transactions") or [], v.get("keys") or []
    if not txs or not keys:
        raise SelftestError("selftest: vectors file has no transactions or keys")

    for tv in txs:
        name = tv["name"]
        tx = steemtx.Transaction.from_json(tv["tx"])
        if (got := tx.serialize().hex()) != tv["serialized"]:
            raise SelftestError(f"selftest {name!r}: serialized {got}, want {tv['serialized']}")
        digest = tx.digest()
        if digest.hex() != tv["digest"]:
            raise SelftestError(f"selftest {name!r}: digest {digest.hex()}, want {tv['digest']}")
        if tx.id() != tv["id"]:
            raise SelftestError(f"selftest {name!r}: id {tx.id()}, want {tv['id']}")
        # The stored JSON must round-trip to the same ID (other clients
        # recompute proposal IDs from the JSON).
        again = steemtx.Transaction.from_json(json.loads(json.dumps(tx.to_json())))
        if again.id() != tv["id"]:
            raise SelftestError(f"selftest {name!r}: JSON round-trip changed the id to {again.id()}")
        for i, s in enumerate(tv.get("signatures") or []):
            pub = steemtx.recover_signer(digest, s["sig"])
            if pub != s["pubkey"]:
                raise SelftestError(f"selftest {name!r}: signature {i} recovers to {pub}, want {s['pubkey']}")

    for kv in keys:
        priv = steemtx.decode_wif(kv["wif"])
        if (got := steemtx.public_key_of(priv)) != kv["pubkey"]:
            raise SelftestError(f"selftest key: pubkey {got}, want {kv['pubkey']}")
        if steemtx.decode_public_key(kv["pubkey"]).hex() != kv["compressed"]:
            raise SelftestError("selftest key: decode_public_key mismatch")
        # Sign/recover round trips: every signature canonical, recovering to the key.
        for i in range(64):
            digest = hashlib.sha256(f"svm.bank selftest {i}".encode()).digest()
            sig = steemtx.sign(priv, digest)
            if sig[:2] not in ("1f", "20"):
                raise SelftestError(f"selftest sign {i}: header {sig[:2]}, want 1f or 20")
            if steemtx.recover_signer(digest, sig) != kv["pubkey"]:
                raise SelftestError(f"selftest sign {i}: does not recover to {kv['pubkey']}")

    for av in v.get("assets") or []:
        milli = int(av["millisteem"])
        if (got := steemtx.format_milli(milli, av["symbol"])) != av["string"]:
            raise SelftestError(f"selftest asset: format_milli({milli}, {av['symbol']}) = {got!r}, want {av['string']!r}")
        a = steemtx.parse_asset(av["string"])
        if a.amount != milli or a.symbol != av["symbol"] or str(a) != av["string"]:
            raise SelftestError(f"selftest asset: parse_asset({av['string']!r}) = {a}")

    cases = v.get("apportionment") or []
    if not cases:
        raise SelftestError("selftest apportionment: no cases")
    for c in cases:
        entries = [StakeEntry(val["account"], int(val["tokens"], 10)) for val in c["validators"]]
        got = apportion(entries, int(c["threshold_percent"]))
        want = steemtx.Authority.from_json({**c["expected"], "key_auths": []})
        if not got.equal(want):
            raise SelftestError(f"selftest apportionment {c['name']!r}:\n got  {got.to_json()}\n want {want.to_json()}")
