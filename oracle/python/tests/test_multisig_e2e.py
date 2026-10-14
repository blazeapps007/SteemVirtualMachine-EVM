"""Four independent signers against a fake Steem node that enforces the chain
rules (mirrors oracle/go/multisig/e2e_test.go)."""

import copy
import os
from datetime import timedelta

import pytest

from _ms import BANK, WEIGHTS, FakeSteem, FakeSVM, bank_authority, key_authority, pub, stakes, wif
from steemvm_oracle.multisig import steemtx
from steemvm_oracle.multisig.ballot import parse_metadata
from steemvm_oracle.multisig.chain import Account
from steemvm_oracle.multisig.policy import Config, Withdrawal
from steemvm_oracle.multisig.signer import Signer, SignerConfig, SignerConfigError, build_signer_config


class Network:
    def __init__(self, tmp_path):
        self.tmp_path = tmp_path
        self.node = FakeSteem()
        auth = bank_authority()
        self.node.accounts[BANK] = Account(BANK, auth, copy.deepcopy(auth), copy.deepcopy(auth), pub("memo"), "", "",
                                           "1000.000 STEEM", "50.000 SBD")
        self.node.accounts["rcpt"] = Account("rcpt", balance="0.000 STEEM", sbd_balance="0.000 SBD")
        for n, _ in WEIGHTS:
            self.node.accounts[n] = Account(n, key_authority(pub(n + "/owner")), key_authority(pub(n)),
                                            key_authority(pub(n + "/posting")), pub(n), "",
                                            '{"profile":{"name":"' + n + ' <validator> & co"}}', "0.000 STEEM", "0.000 SBD")
        self.svm = FakeSVM(stakes_at={99000: stakes(alice=45, bob=30, carol=15, dave=10)})
        self.signers = [self.signer(n, wif(n)) for n, _ in WEIGHTS]

    def signer(self, name, active_wif, init=True):
        d = self.tmp_path / name
        d.mkdir(exist_ok=True)
        cfg = SignerConfig(policy=Config(fallback_delay=timedelta(hours=1)), active_wif=active_wif,
                           state_dir=str(d), rank_delay=1e-9)
        s = Signer(cfg, self.svm.as_(name), env={}, rpc=self.node)
        if init:
            s.init()
        return s

    def rounds(self, k):
        for _ in range(k):
            for s in self.signers:
                s.pass_once()
                self.node.advance(1)


@pytest.fixture
def net(tmp_path):
    n = Network(tmp_path)
    yield n
    for s in n.signers:
        s.close()


def test_signers_pay_withdrawals_exactly_once(net):
    net.svm.all[6] = Withdrawal(6, "rcpt", 12345, "STEEM", 90000, "REQUESTED")
    net.svm.all[9] = Withdrawal(9, "rcpt", 2500, "SBD", 95000, "REQUESTED")
    net.svm.all[3] = Withdrawal(3, "rcpt", 1000, "STEEM", 100000 + 14400 - 50400 - 1, "REQUESTED")  # inside the margin
    net.rounds(3)
    node = net.node
    assert node.rejected == []
    assert sorted((t.memo, t.to, t.amount) for t in node.transfers) == [
        ("svm-withdrawal 6", "rcpt", "12.345 STEEM"), ("svm-withdrawal 9", "rcpt", "2.500 SBD")]
    assert (node.accounts[BANK].balance, node.accounts[BANK].sbd_balance) == ("987.655 STEEM", "47.500 SBD")

    net.rounds(2)
    for wid in (6, 9):
        w = net.svm.all[wid]
        net.svm.all[wid] = Withdrawal(w.id, w.destination, w.amount_milli, w.symbol, w.created_at, "PROCESSED")
    net.rounds(2)
    assert len(node.transfers) == 2 and node.rejected == []
    for n, _ in WEIGHTS:
        assert node.accounts[n].posting_json_metadata.startswith('{"profile":{"name":"' + n + ' <validator> & co"},"svm_ms":')
    for s in net.signers:
        _, b = parse_metadata(node.accounts[s.me].posting_json_metadata)
        for pid, v in b.votes.items():
            assert v.a != 1 or pid in s.journal.approvals, f"{s.me} published an approval missing from its journal"


def test_signers_sync_the_validator_set(net):
    net.svm.height_ = 101000
    net.svm.stakes_at[100000] = stakes(alice=50, bob=30, carol=15, dave=5)
    want = bank_authority([("alice", 5000), ("bob", 3000), ("carol", 1500), ("dave", 500)])
    net.rounds(3)
    node = net.node
    assert node.rejected == []
    b = node.accounts[BANK]
    assert b.owner.equal(want) and b.active.equal(want) and b.posting.equal(want) and b.memo_key == pub("memo")
    assert b.owner.total() == 10000 and b.owner.weight_threshold == 7500
    before = len(node.history[BANK])
    net.rounds(3)
    assert len(node.history[BANK]) == before and node.rejected == []


def test_dry_run_publishes_nothing(net):
    net.svm.all[6] = Withdrawal(6, "rcpt", 12345, "STEEM", 90000, "REQUESTED")
    for s in net.signers:
        s.cfg.dry_run = True
    before = {n: a.posting_json_metadata for n, a in net.node.accounts.items()}
    net.rounds(3)
    assert net.node.seen == set() and net.node.transfers == []
    assert {n: a.posting_json_metadata for n, a in net.node.accounts.items()} == before
    assert all(s.journal.approvals == {} for s in net.signers)


def test_key_guards(net):
    s = net.signer("alice-owner", wif("alice/owner"), init=False)
    s.svm = net.svm.as_("alice")
    with pytest.raises(SignerConfigError, match="OWNER key"):
        s.init()
    s.close()
    s = net.signer("alice-foreign", wif("bob"), init=False)
    s.svm = net.svm.as_("alice")
    with pytest.raises(SignerConfigError, match="does not satisfy"):
        s.init()
    s.close()
    cfg = SignerConfig(active_wif=wif("alice"), state_dir=str(net.tmp_path / "g"))
    for var in ("ORACLE_STEEM_OWNER_WIF", "ORACLE_STEEM_POSTING_WIF", "MASTER_PASSWORD", "OWNER"):
        with pytest.raises(SignerConfigError, match=var):
            Signer(cfg, net.svm.as_("alice"), env={var: "5Kxxxxxxxx"}, rpc=net.node)
    for bad in ("", "not-a-wif", wif("alice")[:50]):
        with pytest.raises(SignerConfigError):
            Signer(SignerConfig(active_wif=bad), net.svm.as_("alice"), env={}, rpc=net.node)


@pytest.mark.skipif(os.name != "posix", reason="flock is Unix-only")
def test_one_signer_per_state_dir(net):
    # Two signers on one state dir would overwrite each other's votes: the second must fail.
    second = Signer(SignerConfig(active_wif=wif("alice"), state_dir=net.signers[0].cfg.state_dir), net.svm.as_("alice"),
                    env={}, rpc=net.node)
    with pytest.raises(SignerConfigError, match="already holds"):
        second.init()


def test_env_config():
    assert build_signer_config({}, "https://api.steemit.com", "/d") is None
    cfg = build_signer_config({"ORACLE_MULTISIG_ENABLED": "true", "ORACLE_STEEM_ACTIVE_WIF": " 5Kabc ",
                               "ORACLE_STEEM_RPCS": "http://a, http://b", "ORACLE_MULTISIG_INTERVAL": "30s",
                               "ORACLE_MULTISIG_DRY_RUN": "1", "ORACLE_MULTISIG_MAX_PER_TX_SBD": "500.000"},
                              "https://api.steemit.com", "/d")
    assert (cfg.active_wif, cfg.nodes, cfg.interval, cfg.dry_run, cfg.propose, cfg.policy.max_per_tx_milli) == (
        "5Kabc", ["http://a", "http://b"], 30.0, True, True, {"SBD": 500000})
    assert build_signer_config({"ORACLE_MULTISIG_ENABLED": "true"}, "https://x", "/d").nodes == ["https://x"]
    for env in ({"ORACLE_MULTISIG_ENABLED": "yes"}, {"ORACLE_MULTISIG_ENABLED": "true", "ORACLE_MULTISIG_THRESHOLD_PERCENT": "0"},
                {"ORACLE_MULTISIG_ENABLED": "true", "ORACLE_MULTISIG_MAX_PER_TX_STEEM": "5"}):
        with pytest.raises(SignerConfigError):
            build_signer_config(env, "https://x", "/d")
