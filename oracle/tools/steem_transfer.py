"""Steem helper for the validator setup scripts (reset.sh, new-validator.sh).

Runs inside the published oracle-python image, which already carries the
svm.bank signer's byte-exact Steem transaction code (steemvm_oracle.multisig),
so the scripts need no Steem library on the host. Piped in on stdin:

    docker run --rm -i -e ORACLE_STEEM_ACTIVE_WIF -e STEEM_RPCS <oracle-python image> \
        python -I - <mode> <args...> < oracle/tools/steem_transfer.py

Modes:
    check    <account>                        the key satisfies @account's active
                                              authority and is not one of its
                                              owner keys; prints the public key
    transfer <account> <to> <amount> <memo>   signs and broadcasts one transfer
                                              from @account; prints the trx_id

The Steem ACTIVE key comes from ORACLE_STEEM_ACTIVE_WIF in the environment,
never from the command line. STEEM_RPCS is a comma-separated node list (own
node first; default https://api.steemit.com). STEEM_TRANSFER_DRY_RUN=1 makes
`transfer` print the signed transaction instead of broadcasting it.
"""

import json
import os
import sys
from datetime import timedelta

from steemvm_oracle.multisig import steemtx
from steemvm_oracle.multisig.chain import broadcast, get_dynamic_props


def fail(msg: str) -> None:
    print(f"steem_transfer: {msg}", file=sys.stderr)
    sys.exit(1)


def main(argv: list[str]) -> None:
    if not argv or argv[0] not in ("check", "transfer") or len(argv) != (2 if argv[0] == "check" else 5):
        fail("usage: check <account> | transfer <account> <to> <amount> <memo>")
    mode, account = argv[0], argv[1]

    try:
        key = steemtx.decode_wif(os.environ.get("ORACLE_STEEM_ACTIVE_WIF", ""))
    except steemtx.SteemTxError as exc:
        fail(f"ORACLE_STEEM_ACTIVE_WIF: {exc}")
    pub = steemtx.public_key_of(key)

    rpc = steemtx.RPC((os.environ.get("STEEM_RPCS") or "https://api.steemit.com").split(","))
    accounts = rpc.call("condenser_api.get_accounts", [[account]])
    if not accounts:
        fail(f"steem account @{account} not found")
    acct = accounts[0]
    if any(k == pub for k, _ in acct["owner"]["key_auths"]):
        fail(f"refusing: {pub} is an OWNER key of @{account} - use the active key")
    active = acct["active"]
    if sum(w for k, w in active["key_auths"] if k == pub) < active["weight_threshold"]:
        fail(f"the key {pub} does not satisfy @{account}'s active authority")
    if mode == "check":
        print(pub)
        return

    to, amount, memo = argv[2:]
    steemtx.parse_asset(amount)  # exact "1.000 STEEM" / "1.000 SBD" form, or raise
    op = steemtx.Operation("transfer", steemtx.Transfer(account, to, amount, memo))
    tx = steemtx.new_transaction(get_dynamic_props(rpc), [op], timedelta(minutes=10))
    tx.signatures = [steemtx.sign(key, tx.digest())]
    if os.environ.get("STEEM_TRANSFER_DRY_RUN") == "1":
        print(json.dumps({"trx_id": tx.id(), "hex": tx.serialize().hex(), "tx": tx.to_json()}))
        return
    broadcast(rpc, tx)
    print(tx.id())


if __name__ == "__main__":
    try:
        main(sys.argv[1:])
    except (steemtx.SteemTxError, steemtx.ChainError, RuntimeError) as exc:
        fail(str(exc))
