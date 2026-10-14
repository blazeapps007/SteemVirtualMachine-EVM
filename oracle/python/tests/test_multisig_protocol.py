"""Ballot metadata, the shared live ballot vectors, view tallies, the journal,
memos and history paging."""

import json
from datetime import timedelta

import pytest

from _ms import BANK, NOW, FakeSteem, Fixture, bank_authority, key_authority, pub, transfer_op, ts
from steemvm_oracle.multisig import steemtx
from steemvm_oracle.multisig.ballot import (MAX_BALLOT_BYTES, Ballot, InvalidMetadata, MalformedBallot, StoredProposal,
                                            Vote, encode_metadata, parse_metadata)
from steemvm_oracle.multisig.chain import (Account, HistoryIndex, parse_withdrawal_memo, paid_memo_id,
                                           withdrawal_memo)
from steemvm_oracle.multisig.journal import ApprovalJournal, CorruptJournal
from steemvm_oracle.multisig.vectors import load_vectors
from steemvm_oracle.multisig.view import EXECUTED, EXPIRED, OPEN, READY, REJECTED, build_view

PAY = transfer_op("rcpt", "1.000 STEEM", "svm-withdrawal 1")


def test_ballot_vectors_from_mainnet():
    for case in load_vectors()["ballots"]:
        auth = steemtx.Authority.from_json(case["bank_authority"])
        bank = Account(BANK, auth, auth, auth)
        members, ballots = {}, {}
        for m in case["members"]:
            members[m["name"]] = Account(m["name"], active=key_authority(m["active_key"]))
            ballots[m["name"]] = parse_metadata(m["posting_json_metadata"])[1]
        v = build_view(steemtx.parse_time(case["now"]), None, bank, members, ballots, HistoryIndex())
        assert len(v.proposals) == len(case["expected"])
        by_id = {p.id: p for p in v.proposals}
        for want in case["expected"]:
            p = by_id[want["id"]]
            valid = sorted(n for n, vi in p.votes.items() if vi.approve and vi.valid)
            assert (p.creator, p.approve, p.deny, p.status, valid) == (
                want["creator"], want["approve"], want["deny"], want["status"], want["valid_approvals"])


def test_metadata_round_trip_preserves_other_keys():
    f = Fixture()
    pid = f.propose("alice", "Withdrawal 1: 1.000 STEEM to @rcpt", [PAY])
    f.vote("alice", pid, True)
    profile = '{"profile":{"name":"Alice <3","about":"a & b","z":1,"a":[1, 2]}}'
    raw, b = parse_metadata(profile)
    b.props[pid] = f.stored(pid)
    b.votes[pid] = f.ballots["alice"].votes[pid]
    out = encode_metadata(raw, b)
    assert out.startswith('{"profile":{"name":"Alice <3","about":"a & b","z":1,"a":[1, 2]},"svm_ms":{')
    raw2, b2 = parse_metadata(out)
    assert raw2["profile"] == raw["profile"]
    assert b2.props[pid].tx.id() == pid and b2.props[pid].t == b.props[pid].t
    assert b2.votes[pid] == b.votes[pid]
    assert encode_metadata(raw2, b2) == out


@pytest.mark.parametrize("bad", ["[1,2]", "{bad", '"string"', "42", '{"a":1} x', '{"a":1,}'])
def test_invalid_metadata_is_never_overwritten(bad):
    with pytest.raises(InvalidMetadata):
        parse_metadata(bad)


@pytest.mark.parametrize("ok", ["", "  ", "{}", "null"])
def test_empty_metadata(ok):
    raw, b = parse_metadata(ok)
    assert raw == {} and b.props == {} and b.votes == {}


def test_malformed_and_unsupported_ballots():
    with pytest.raises(MalformedBallot):
        parse_metadata('{"profile":{},"svm_ms":"x"}')
    with pytest.raises(MalformedBallot):
        parse_metadata('{"svm_ms":{"props":[],"votes":{}}}')
    _, b = parse_metadata('{"svm_ms":{"props":{"x":{"t":"t","c":"c","tx":{"ref_block_num":1,"ref_block_prefix":1,'
                          '"expiration":"2026-01-01T00:00:00","operations":[["vote",{}]],"extensions":[]}}},"votes":{}}}')
    assert b.props == {}


def test_ballot_prune():
    t = lambda d: ts(NOW + d)  # noqa: E731
    b = Ballot(
        props={"oldp": StoredProposal("", steemtx.Transaction(expiration=t(-timedelta(hours=73))), ""),
               "recentp": StoredProposal("", steemtx.Transaction(expiration=t(-timedelta(hours=71))), "")},
        votes={"oldv": Vote(0, "", t(-timedelta(hours=73))), "recentv": Vote(0, "", t(-timedelta(hours=71))),
               "livev": Vote(1, "sig", t(timedelta(hours=1)))},
    )
    b.prune(NOW)
    assert set(b.props) == {"recentp"} and set(b.votes) == {"recentv", "livev"}

    sig = "ab" * 65
    big = Ballot()
    for i in range(400):
        big.votes[f"{i:040d}"] = Vote(1, sig, t(-timedelta(minutes=i + 1)))
    for i in range(20):
        big.votes[f"live{i:036d}"] = Vote(1, sig, t(timedelta(hours=1)))
    big.prune(NOW)
    assert big.encoded_size() <= MAX_BALLOT_BYTES
    assert all(f"live{i:036d}" in big.votes for i in range(20))
    assert f"{0:040d}" in big.votes and f"{399:040d}" not in big.votes


def test_view_statuses_and_tallies():
    f = Fixture()
    open_ = f.propose("alice", "open", [PAY])
    for m in ("alice", "carol", "dave"):  # 7000 < 7500
        f.vote(m, open_, True)
    ready = f.propose("alice", "ready", [PAY], timedelta(minutes=57))
    f.vote("alice", ready, True)  # 4500 + 3000 = exactly the threshold
    f.vote("bob", ready, True)
    margin = f.propose("bob", "deny at the margin", [PAY], timedelta(minutes=56))
    f.vote("carol", margin, False)
    f.vote("dave", margin, False)
    rejected = f.propose("bob", "rejected", [PAY], timedelta(minutes=55))
    f.vote("bob", rejected, False)
    executed = f.propose("carol", "executed", [PAY], timedelta(minutes=54))
    f.hist.executed[executed] = f.props.time
    v = f.view()
    assert {p.id: p.status for p in v.proposals} == {open_: OPEN, ready: READY, margin: OPEN, rejected: REJECTED,
                                                    executed: EXECUTED}
    assert f.find(v, open_).approve == 7000 and f.find(v, margin).deny == 2500
    f.now = NOW + timedelta(minutes=57)
    v = f.view()
    assert (f.find(v, open_).status, f.find(v, ready).status, f.find(v, executed).status) == (OPEN, EXPIRED, EXECUTED)


def test_view_verification():
    f = Fixture()
    good = f.propose("alice", "good", [PAY])
    f.vote("alice", good, True)
    f.vote("bob", good, True, signer="dave")  # someone else's key
    forged = f.propose("alice", "forged", [PAY], timedelta(minutes=50))
    f.ballots["alice"].props["0" * 40] = f.ballots["alice"].props.pop(forged)
    f.propose("mallory", "outsider", [PAY], timedelta(minutes=49))
    f.propose("alice", "other account", [transfer_op("rcpt", "1.000 STEEM", "x", frm="alice")], timedelta(minutes=48))
    v = f.view()
    assert [p.id for p in v.proposals] == [good]
    p = v.proposals[0]
    assert p.votes["alice"].valid and not p.votes["bob"].valid
    assert (p.approve, p.status) == (4500, OPEN)


@pytest.mark.parametrize("approve, want", [
    (["alice", "bob"], ["alice", "bob"]),
    (["alice", "bob", "carol", "dave"], ["alice", "bob"]),
    (["dave", "carol", "alice"], None),
    (["dave", "bob", "alice"], ["alice", "bob"]),
    (["bob", "carol", "dave"], None),
])
def test_minimal_signatures(approve, want):
    f = Fixture()
    pid = f.propose("alice", "p", [PAY])
    for m in approve:
        f.vote(m, pid, True)
    v = f.view()
    p = f.find(v, pid)
    sigs = v.minimal_signatures(p)
    if want is None:
        assert sigs is None
    else:
        assert [steemtx.recover_signer(p.digest, s) for s in sigs] == [pub(n) for n in want]


def test_journal(tmp_path):
    j = ApprovalJournal.load(str(tmp_path))
    f = Fixture()
    a = f.propose("alice", "A", [PAY])
    j.record(a, f.stored(a).tx)
    j.save(str(tmp_path))
    j2 = ApprovalJournal.load(str(tmp_path))
    assert j2.approvals[a]["subject"] == "transfer:svm-withdrawal 1"
    assert j2.conflicts("transfer:svm-withdrawal 1", "other", NOW)
    assert not j2.conflicts("transfer:svm-withdrawal 1", a, NOW)
    assert not j2.conflicts("transfer:svm-withdrawal 2", "other", NOW)
    assert not j2.conflicts("transfer:svm-withdrawal 1", "other", NOW + timedelta(minutes=58))
    j2.prune(NOW + timedelta(minutes=58, hours=72, seconds=1))
    assert j2.approvals == {}
    (tmp_path / "svm_bank_approvals.json").write_text("{corrupt")
    with pytest.raises(CorruptJournal):
        ApprovalJournal.load(str(tmp_path))


def test_journal_file_is_shared_with_the_go_client(tmp_path):
    # Exactly what oracle/go/multisig/journal.go writes.
    (tmp_path / "svm_bank_approvals.json").write_text(
        '{"approvals":{"abc":{"subject":"transfer:svm-withdrawal 3","expiration":"' + ts(NOW + timedelta(minutes=5)) + '"}}}')
    assert ApprovalJournal.load(str(tmp_path)).conflicts("transfer:svm-withdrawal 3", "x", NOW)


def test_withdrawal_memos():
    for memo, wid in {"svm-withdrawal 0": 0, "svm-withdrawal 7": 7,
                      "svm-withdrawal 18446744073709551615": 18446744073709551615}.items():
        assert parse_withdrawal_memo(memo) == wid and withdrawal_memo(wid) == memo
    for memo in ["svm-withdrawal 07", "svm-withdrawal +7", "svm-withdrawal 7 ", " svm-withdrawal 7", "svm-withdrawal  7",
                 "svm-withdrawal", "svm-withdrawal ", "svm-withdraw 7", "SVM-WITHDRAWAL 7", "svm-withdrawal 0x7",
                 "svm-withdrawal 18446744073709551616", "svm-withdrawal -1", "svm-withdrawal 7\n", "svm-withdrawal ٧"]:
        assert parse_withdrawal_memo(memo) is None, memo
    assert paid_memo_id("  svm-withdrawal   9 ") == 9
    assert paid_memo_id("svm-withdrawal x") is None


def test_history_apply_and_sync_paging():
    node = FakeSteem()
    for i in range(250):
        node.add_history(BANK, f"trx{i:03d}", ts(NOW), "transfer",
                         {"from": BANK, "to": "rcpt", "amount": "1.000 STEEM", "memo": withdrawal_memo(i)})
    node.add_history(BANK, "dep", ts(NOW), "transfer", {"from": "user", "to": BANK, "amount": "1.000 STEEM", "memo": "svm-withdrawal 999"})
    node.add_history(BANK, "own", ts(NOW), "account_update", {"account": BANK, "owner": bank_authority().to_json()})
    h = HistoryIndex()
    h.sync(node, BANK, 110000000, NOW)
    assert h.next_seq == 252 and len(h.paid) == 250 and h.paid["249"] == "trx249" and not h.is_paid(999)
    assert h.last_owner_change == ts(NOW)
    node.add_history(BANK, "trx250", ts(NOW), "transfer", {"from": BANK, "to": "rcpt", "amount": "1.000 STEEM", "memo": "svm-withdrawal 250"})
    node.starts = []
    h.sync(node, BANK, 110000001, NOW)
    assert h.next_seq == 253 and h.is_paid(250)
    # Steady state: one request, keyed by the head block (no stale cached window).
    assert node.starts == [110000001]
    empty = HistoryIndex()
    empty.sync(node, "nobody", 110000001, NOW)
    assert empty.next_seq == 0
