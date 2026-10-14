"""The SVM reads the signer checks every decision against, over the node's
REST (gRPC-gateway) API -- this client's transport (``oracle/PROTOCOL.md``
SS4). Mirrors ``oracle/go/multisig/svmquery.go``; historical reads at a
snapshot height use the ``x-cosmos-block-height`` header.
"""

from __future__ import annotations

from typing import Optional

from ..broadcast import NotFoundError, RestClient
from ..keys import decode_address
from .apportion import StakeEntry
from .policy import Withdrawal

_HEIGHT_HEADER = "x-cosmos-block-height"


def _key_not_found(exc: NotFoundError) -> bool:
    """Only the bridge queries' "key not found": a pruned height or other
    failure must stay an error, not read as "absent"."""
    return "key not found" in str(exc).lower()


def _withdrawal(w: dict) -> Withdrawal:
    return Withdrawal(
        id=int(w.get("id", 0)),
        destination=str(w.get("destination_steem_account", "")),
        amount_milli=int(w.get("amount_millisteem", 0)),
        symbol="SBD" if w.get("asset") in ("BRIDGE_ASSET_SBD", 1) else "STEEM",
        created_at=int(w.get("created_at", 0)),
        status=str(w.get("status") or "WITHDRAWAL_STATUS_REQUESTED").removeprefix("WITHDRAWAL_STATUS_"),
    )


class RestSVM:
    def __init__(self, rest: RestClient, valoper_address: str):
        self.rest = rest
        self.valoper_address = valoper_address
        self.operator_bytes = decode_address(valoper_address)[1]

    def height(self) -> int:
        resp = self.rest.get_json("/cosmos/base/tendermint/v1beta1/blocks/latest")
        return int(resp["block"]["header"]["height"])

    def withdrawal_timeout_blocks(self) -> int:
        return int(self.rest.get_json("/steemvm/steembridge/v1/params")["params"]["withdrawal_timeout_blocks"])

    def _paginate(self, path: str, field: str, extra: Optional[dict] = None, headers: Optional[dict] = None) -> list[dict]:
        out: list[dict] = []
        key = ""
        while True:
            params = {"pagination.limit": "200", **(extra or {})}
            if key:
                params["pagination.key"] = key
            resp = self.rest.get_json(path, params=params, headers=headers)
            out.extend(resp.get(field) or [])
            key = ((resp.get("pagination") or {}).get("next_key")) or ""
            if not key:
                return out

    def requested_withdrawals(self) -> list[Withdrawal]:
        return [_withdrawal(w) for w in self._paginate("/steemvm/steembridge/v1/requested_withdrawals", "withdrawals")]

    def withdrawal(self, wid: int) -> Optional[Withdrawal]:
        try:
            return _withdrawal(self.rest.get_json(f"/steemvm/steembridge/v1/withdrawal/{wid}")["withdrawal"])
        except NotFoundError as exc:
            if _key_not_found(exc):
                return None
            raise

    def _owner_of_name(self, name: str, headers: Optional[dict]) -> Optional[bytes]:
        try:
            resp = self.rest.get_json(f"/steemvm/steembridge/v1/name/{name}", headers=headers)
        except NotFoundError as exc:
            if _key_not_found(exc):
                return None
            raise
        try:
            return decode_address(resp["name"]["address"])[1]
        except (KeyError, ValueError):
            return None

    def validator_stakes(self, height: int) -> list[StakeEntry]:
        """Bonded, unjailed validators whose moniker is an ACTIVE Steem name
        link owned by the operator, at the given SVM height."""
        headers = {_HEIGHT_HEADER: str(height)} if height > 0 else None
        out: list[StakeEntry] = []
        for val in self._paginate("/cosmos/staking/v1beta1/validators", "validators",
                                  {"status": "BOND_STATUS_BONDED"}, headers):
            tokens = int(val.get("tokens", "0") or 0)
            if val.get("jailed") or tokens <= 0:
                continue
            name = str((val.get("description") or {}).get("moniker", "")).strip()
            if not name:
                continue
            try:
                op = decode_address(val["operator_address"])[1]
            except (KeyError, ValueError):
                continue
            if self._owner_of_name(name, headers) != op:
                continue  # moniker is not an ACTIVE name link owned by this operator
            out.append(StakeEntry(name, tokens))
        return out

    def self_steem_account(self) -> str:
        try:
            resp = self.rest.get_json(f"/cosmos/staking/v1beta1/validators/{self.valoper_address}")
        except NotFoundError as exc:
            raise RuntimeError(f"this oracle's key is not a validator operator: {exc}") from exc
        name = str(resp["validator"]["description"].get("moniker", "")).strip()
        if self._owner_of_name(name, None) != self.operator_bytes:
            raise RuntimeError(f"validator moniker {name} is not an ACTIVE Steem name link owned by this operator")
        return name
