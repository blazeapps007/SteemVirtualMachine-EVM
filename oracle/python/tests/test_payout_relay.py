"""Withdrawal payouts as the bridge relay reads them: linked by a custom_json,
never by the memo (mirrors oracle/go/relayer/relayer_test.go)."""

from __future__ import annotations

from steemvm_oracle.relayer import compare_payout
from steemvm_oracle.steem_client import (ASSET_SBD, ASSET_STEEM, PAYOUT_LINK_ID, Payout, extract_gateway_payouts,
                                         extract_gateway_transfers, parse_payout_link)

LINK = ["custom_json", {"required_auths": ["gw"], "required_posting_auths": [], "id": PAYOUT_LINK_ID,
                        "json": '{"withdrawal_id":"7"}'}]


def _transfer(frm, to, amount="1.000 STEEM", memo=""):
    return ["transfer", {"from": frm, "to": to, "amount": amount, "memo": memo}]


def test_parse_payout_link():
    assert parse_payout_link(LINK, "gw") == 7
    for body in ['{"withdrawal_id":"07"}', '{"withdrawal_id":7}', '{"withdrawal_id":"+7"}', '{ "withdrawal_id":"7"}',
                 '{"withdrawal_id":"7","x":1}', '{"withdrawal_id":"18446744073709551616"}', "svm-withdrawal 7"]:
        assert parse_payout_link(["custom_json", {**LINK[1], "json": body}], "gw") is None, body
    assert parse_payout_link(["custom_json", {**LINK[1], "id": "other"}], "gw") is None
    assert parse_payout_link(["custom_json", {**LINK[1], "required_auths": ["alice"]}], "gw") is None
    assert parse_payout_link(["custom_json", {**LINK[1], "required_auths": [], "required_posting_auths": ["gw"]}], "gw") is None
    assert parse_payout_link(["custom_json", {**LINK[1], "required_auths": ["gw", "alice"]}], "gw") is None
    assert parse_payout_link(["custom_json", {**LINK[1], "required_posting_auths": ["alice"]}], "gw") is None
    assert parse_payout_link(_transfer("gw", "a", memo="svm-withdrawal 7"), "gw") is None


def test_extract_payouts():
    block = {"timestamp": "2026-07-14T14:00:00", "transactions": [
        {"transaction_id": "b" * 40, "operations": [_transfer("gw", "alice", "10.000 STEEM", "exchange-tag-42"), LINK]},
        {"transaction_id": "c", "operations": [_transfer("gw", "bob", memo="svm-withdrawal 8")]},
        {"transaction_id": "d", "operations": [_transfer("someone", "carol"), LINK]},
        {"transaction_id": "e", "operations": [LINK, _transfer("gw", "dan")]},
        {"transaction_id": "f", "operations": [_transfer("gw", "erin"), LINK, ["vote", {"voter": "a"}]]},
        {"transaction_id": "g", "operations": [_transfer("gw", "alice", "1.000 VESTS"), LINK]},
    ]}
    got = extract_gateway_payouts(500, block, "gw", "STEEM", "SBD")
    assert got == [Payout(withdrawal_id=7, txid="b" * 40, op_index=0, steem_block=500, steem_timestamp="2026-07-14T14:00:00",
                          destination="alice", memo="exchange-tag-42", amount_millisteem=10000, asset=ASSET_STEEM)]
    assert extract_gateway_payouts(1, None, "gw", "STEEM", "SBD") == []


def test_gateway_to_itself_is_not_a_deposit():
    block = {"timestamp": "t", "transactions": [{"transaction_id": "a", "operations": [
        _transfer("gw", "gw", memo="svm-deposit 0x1111111111111111111111111111111111111111")]}]}
    assert extract_gateway_transfers(1, block, "gw", "STEEM", "") == []


def test_compare_payout():
    w = {"destination_steem_account": "alice", "memo": "tag", "amount_millisteem": "1000", "asset": "BRIDGE_ASSET_STEEM"}
    good = dict(withdrawal_id=7, txid="t", op_index=0, steem_block=1, steem_timestamp="t", destination="alice",
                memo="tag", amount_millisteem=1000, asset=ASSET_STEEM)
    assert compare_payout(Payout(**good), w) is None
    for name, change in {"recipient": {"destination": "mallory"}, "memo": {"memo": ""},
                         "amount": {"amount_millisteem": 999}, "asset": {"asset": ASSET_SBD}}.items():
        assert name in compare_payout(Payout(**{**good, **change}), w)
