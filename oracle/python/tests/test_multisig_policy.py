"""The signer's policy: payouts, duplicate proposals, validator-set syncs and
intents (mirrors oracle/go/multisig/policy_test.go)."""

from datetime import datetime, timedelta, timezone

import pytest

from _ms import BANK, NOW, Fixture, bank_authority, pub, stakes, transfer_op, ts
from steemvm_oracle.multisig import steemtx
from steemvm_oracle.multisig.ballot import Vote
from steemvm_oracle.multisig.journal import ApprovalJournal
from steemvm_oracle.multisig.policy import ABSTAIN, APPROVE, DENY, Withdrawal
from steemvm_oracle.multisig.view import REJECTED, EXPIRED

EXACT = transfer_op("rcpt", "12.345 STEEM", "svm-withdrawal 7")


def payout_fixture() -> Fixture:
    # Withdrawal 7: refund at 90000 + 50400 = 140400, so payable up to 126000.
    f = Fixture()
    f.withdrawal(7, "rcpt", 12345, "STEEM", 90000)
    return f


def _status(f, status):
    w = f.svm.all[7]
    f.svm.all[7] = Withdrawal(w.id, w.destination, w.amount_milli, w.symbol, w.created_at, status)


@pytest.mark.parametrize("name, ops, setup, want", [
    ("exact payout", [EXACT], None, APPROVE),
    ("last allowed height", [EXACT], lambda f: setattr(f.svm, "height_", 126000), APPROVE),
    ("one block late", [EXACT], lambda f: setattr(f.svm, "height_", 126001), DENY),
    ("leading zero memo", [transfer_op("rcpt", "12.345 STEEM", "svm-withdrawal 07")], None, DENY),
    ("guide's old memo", [transfer_op("rcpt", "12.345 STEEM", "svm-withdraw 7")], None, DENY),
    ("memo trailing space", [transfer_op("rcpt", "12.345 STEEM", "svm-withdrawal 7 ")], None, DENY),
    ("wrong recipient", [transfer_op("rcpt2", "12.345 STEEM", "svm-withdrawal 7")], None, DENY),
    ("one millisteem more", [transfer_op("rcpt", "12.346 STEEM", "svm-withdrawal 7")], None, DENY),
    ("one millisteem less", [transfer_op("rcpt", "12.344 STEEM", "svm-withdrawal 7")], None, DENY),
    ("wrong asset", [transfer_op("rcpt", "12.345 SBD", "svm-withdrawal 7")], None, DENY),
    ("two operations", [EXACT, transfer_op("rcpt", "0.001 STEEM", "x")], None, DENY),
    ("already paid", [EXACT], lambda f: f.hist.paid.__setitem__("7", "abc"), DENY),
    ("processed", [EXACT], lambda f: _status(f, "PROCESSED"), DENY),
    ("refunded", [EXACT], lambda f: _status(f, "REFUNDED"), DENY),
    ("not seen yet", [EXACT], lambda f: f.svm.all.pop(7), ABSTAIN),
    ("read fails", [EXACT], lambda f: (_status(f, "PROCESSED"), setattr(f.svm, "err", RuntimeError("refused"))), ABSTAIN),
    ("treasury op", [steemtx.Operation("transfer_to_vesting", steemtx.TransferToVesting(BANK, BANK, "1.000 STEEM"))], None, ABSTAIN),
])
def test_review_payout(name, ops, setup, want):
    f = payout_fixture()
    if setup:
        setup(f)
    pid = f.propose("alice", "payout", ops)
    d, reason = f.review(f.policy("bob"), pid)
    assert d == want, reason


def test_payout_cap():
    f = payout_fixture()
    pid = f.propose("alice", "payout", [EXACT])
    pol = f.policy("bob")
    pol.cfg.max_per_tx_milli = {"STEEM": 12344}
    assert f.review(pol, pid)[0] == ABSTAIN
    pol.cfg.max_per_tx_milli = {"STEEM": 12345}
    assert f.review(pol, pid)[0] == APPROVE


EARLIER = ts(NOW - timedelta(minutes=1))


def test_only_the_earliest_correct_payout():
    f = payout_fixture()
    a = f.propose("alice", "A", [EXACT], created=EARLIER)
    b = f.propose("bob", "B", [EXACT], timedelta(minutes=57))
    pol = f.policy("carol")
    assert f.review(pol, a)[0] == APPROVE
    assert f.review(pol, b)[0] == ABSTAIN


def test_an_earlier_wrong_payout_does_not_block():
    f = payout_fixture()
    f.propose("alice", "wrong", [transfer_op("rcpt", "99.999 STEEM", "svm-withdrawal 7")], created=EARLIER)
    b = f.propose("bob", "B", [EXACT], timedelta(minutes=57))
    assert f.review(f.policy("carol"), b)[0] == APPROVE


def test_my_approval_of_another_blocks():
    f = payout_fixture()
    a = f.propose("alice", "A", [EXACT], created=EARLIER)
    b = f.propose("bob", "B", [EXACT], timedelta(minutes=57))
    assert f.review(f.policy("carol"), a, {b: Vote(1, "", "")})[0] == ABSTAIN


def test_approval_of_a_rejected_unexpired_proposal_still_blocks():
    f = payout_fixture()
    a = f.propose("alice", "A", [EXACT], created=EARLIER)
    f.vote("bob", a, False)
    b = f.propose("bob", "B", [EXACT], timedelta(minutes=57))
    assert f.find(f.view(), a).status == REJECTED
    assert f.review(f.policy("carol"), b, {a: Vote(1, "", "")})[0] == ABSTAIN


def test_an_expired_approval_no_longer_blocks():
    f = payout_fixture()
    a = f.propose("alice", "A", [EXACT], timedelta(minutes=1), created=EARLIER)
    b = f.propose("bob", "B", [EXACT], timedelta(minutes=57))
    f.now = NOW + timedelta(minutes=1)
    assert f.find(f.view(), a).status == EXPIRED
    assert f.review(f.policy("carol"), b, {a: Vote(1, "", "")})[0] == APPROVE


def test_the_journal_blocks_even_when_the_view_lags():
    f = payout_fixture()
    b = f.propose("bob", "B", [EXACT], timedelta(minutes=57))
    pol = f.policy("carol")
    pol.journal = ApprovalJournal({"unseen": {"subject": "transfer:svm-withdrawal 7", "expiration": ts(NOW + timedelta(minutes=1))}})
    assert f.review(pol, b)[0] == ABSTAIN
    pol.journal.approvals["unseen"]["expiration"] = ts(NOW)
    assert f.review(pol, b)[0] == APPROVE
    pol.journal.record(b, f.stored(b).tx)
    assert f.review(pol, b)[0] == APPROVE


def reweight_fixture():
    f = Fixture()
    f.svm.stakes_at[99000] = stakes(alice=50, bob=30, carol=15, dave=5)
    return f, bank_authority([("alice", 5000), ("bob", 3000), ("carol", 1500), ("dave", 500)])


def update_op(f, owner, active, posting, memo_key=None):
    return steemtx.Operation("account_update", steemtx.AccountUpdate(BANK, owner, active, posting,
                                                                     memo_key or f.bank.memo_key, ""))


@pytest.mark.parametrize("name, title, which, setup, want", [
    ("exact set", "Validator set @svm:99000", "want", None, APPROVE),
    ("one point off", "Validator set @svm:99000", "off", None, DENY),
    ("owner only", "Validator set @svm:99000", "owner_only", None, DENY),
    ("memo key changed", "Validator set @svm:99000", "memo", None, DENY),
    ("no snapshot", "Validator set", "want", None, DENY),
    ("not a checkpoint", "Validator set @svm:99500", "want", None, DENY),
    ("too old", "Validator set @svm:96000", "want", None, DENY),
    ("ahead of this node", "Validator set @svm:101000", "want", None, ABSTAIN),
    ("height pruned", "Validator set @svm:98000", "want", None, ABSTAIN),
    ("non-ascii digits", "Validator set @svm:٩٩٠٠٠", "want", None, DENY),
    ("huge snapshot", "Validator set @svm:99999999999999999999000", "want", None, DENY),
    ("already current", "Validator set @svm:99000", "want", "current", DENY),
])
def test_review_validator_set(name, title, which, setup, want):
    f, desired = reweight_fixture()
    off = bank_authority([("alice", 5001), ("bob", 2999), ("carol", 1500), ("dave", 500)])
    if setup == "current":
        f.bank.owner = f.bank.active = f.bank.posting = desired
    op = {"want": update_op(f, desired, desired, desired), "off": update_op(f, off, off, off),
          "owner_only": update_op(f, desired, None, None),
          "memo": update_op(f, desired, desired, desired, pub("attacker"))}[which]
    pid = f.propose("alice", title, [op])
    d, reason = f.review(f.policy("bob"), pid)
    assert d == want, reason


def test_desired_authority_is_exact():
    f, desired = reweight_fixture()
    assert f.policy("bob").desired_at(99000).equal(desired)


def keys_of(intents):
    return [i.key for i in intents]


def test_payout_proposer_rotation():
    # Withdrawal 6: 6 mod 4 = 2, so carol is designated; dave after 120s, alice after 240s.
    f = Fixture()
    f.withdrawal(6, "rcpt", 1000, "STEEM", 90000)
    t0 = datetime.now(timezone.utc)
    for me, after, want in [("carol", 0, 1), ("dave", 0, 0), ("dave", 119, 0), ("dave", 120, 1), ("alice", 120, 0),
                            ("alice", 240, 1), ("mallory", 3600, 0)]:
        got, err = f.policy(me).intents(f.view(), f.state(), {"w:6": t0}, t0 + timedelta(seconds=after))
        assert err is None and len(got) == want, (me, after, keys_of(got))
    seen = {}
    got, _ = f.policy("carol").intents(f.view(), f.state(), seen, t0)
    assert len(got[0].ops) == 1 and got[0].ops[0].value == steemtx.Transfer(BANK, "rcpt", "1.000 STEEM", "svm-withdrawal 6")
    pid = f.propose("carol", got[0].title, got[0].ops)
    assert f.review(f.policy("alice"), pid)[0] == APPROVE
    seen["w:6"] = t0 - timedelta(hours=1)
    got, _ = f.policy("dave").intents(f.view(), f.state(), seen, t0)
    assert got == [] and "w:6" not in seen


@pytest.mark.parametrize("name", ["balance", "margin", "paid", "cap", "journal"])
def test_payout_intent_skips(name):
    f = Fixture()
    f.withdrawal(6, "rcpt", 1000, "STEEM", 90000)
    f.svm.stakes_at[125000] = f.svm.stakes_at[99000]
    pol = f.policy("carol")
    if name == "balance":
        f.bank.balance = "0.999 STEEM"
    elif name == "margin":
        f.svm.height_ = 126001
    elif name == "paid":
        f.hist.paid["6"] = "abc"
    elif name == "cap":
        pol.cfg.max_per_tx_milli = {"STEEM": 999}
    else:
        pol.journal = ApprovalJournal({"x": {"subject": "transfer:svm-withdrawal 6", "expiration": ts(NOW + timedelta(minutes=1))}})
    got, err = pol.intents(f.view(), f.state(), {}, datetime.now(timezone.utc))
    assert err is None and got == []


def test_validator_set_intent():
    # Checkpoint(100000) = 99000; 99 mod 4 = 3, so dave is designated.
    f, desired = reweight_fixture()
    now = datetime.now(timezone.utc)
    got, err = f.policy("dave").intents(f.view(), f.state(), {}, now)
    assert err is None and keys_of(got) == ["v:99000"] and got[0].title == "Validator set @svm:99000"
    u = got[0].ops[0].value
    assert u.owner.equal(desired) and u.active.equal(desired) and u.posting.equal(desired)
    assert u.memo_key == f.bank.memo_key and u.json_metadata == ""
    pid = f.propose("dave", got[0].title, got[0].ops)
    assert f.review(f.policy("alice"), pid)[0] == APPROVE

    f, _ = reweight_fixture()
    f.hist.last_owner_change = ts(NOW - timedelta(minutes=59))
    assert f.policy("dave").intents(f.view(), f.state(), {}, now)[0] == []
    f.hist.last_owner_change = ts(NOW - timedelta(minutes=61))
    assert len(f.policy("dave").intents(f.view(), f.state(), {}, now)[0]) == 1

    f = Fixture()
    assert f.policy("dave").intents(f.view(), f.state(), {}, now)[0] == []

    # A stake read failure keeps the payouts and reports the error.
    f = Fixture()
    f.withdrawal(5, "rcpt", 1000, "STEEM", 90000)  # 5 mod 4 = 1: bob
    del f.svm.stakes_at[99000]
    got, err = f.policy("bob").intents(f.view(), f.state(), {}, now)
    assert keys_of(got) == ["w:5"] and err
