"""The jailed/not-bonded gate: a validator that can't have its duty txs
accepted must not broadcast them (oracle/PROTOCOL.md SS10)."""

from types import SimpleNamespace

import pytest

from steemvm_oracle import relayer
from steemvm_oracle.broadcast import NotFoundError
from steemvm_oracle.validator_gate import validator_can_submit


@pytest.mark.parametrize(
    "validator, want, reason",
    [
        ({"status": "BOND_STATUS_BONDED", "jailed": False}, True, ""),
        ({"status": "BOND_STATUS_BONDED", "jailed": True}, False, "validator is jailed"),
        ({"status": "BOND_STATUS_UNBONDING", "jailed": True}, False, "validator is jailed"),
        ({"status": "BOND_STATUS_UNBONDING", "jailed": False}, False, "validator is not bonded (BOND_STATUS_UNBONDING)"),
        ({"status": "BOND_STATUS_UNBONDED", "jailed": False}, False, "validator is not bonded (BOND_STATUS_UNBONDED)"),
        (None, False, "not a validator"),
    ],
)
def test_validator_can_submit(validator, want, reason):
    assert validator_can_submit(validator) == (want, reason)


class FakeRest:
    """Stands in for the node's REST API: a vote period of 5 and a latest
    height that moves forward, plus a validator record the test controls."""

    def __init__(self, validator):
        self.validator = validator
        self.height = 100

    def get_json(self, path):
        if path == "/steemvm/oracle/data/v1/params":
            return {"params": {"vote_period": "5", "whitelist": ["STEEM/USD"]}}
        if path == "/cosmos/base/tendermint/v1beta1/blocks/latest":
            return {"block": {"header": {"height": str(self.height)}}}
        if path.startswith("/cosmos/staking/v1beta1/validators/"):
            if self.validator is None:
                raise NotFoundError(path)
            if isinstance(self.validator, Exception):
                raise self.validator
            return {"validator": self.validator}
        raise AssertionError(f"unexpected query {path}")


class RecordingFeeder:
    def __init__(self):
        self.steps = 0

    def step(self, period, whitelist, prev):
        self.steps += 1
        return ["prevote-msg"], prev


@pytest.fixture
def broadcasts(monkeypatch):
    sent = []
    monkeypatch.setattr(relayer, "broadcast_price_feed_msgs", lambda *a, **k: sent.append(a[3]) or "TXHASH")
    monkeypatch.setattr(relayer, "save_feeder_state", lambda *a, **k: None)
    return sent


def make_cycle(rest, tmp_path):
    keypair = SimpleNamespace(address="steem1signer", valoper_address="steemvaloper1x")
    return relayer.Cycle(rest=rest, steem=None, keypair=keypair, chain_id="steemvm", cfg=None, state_dir=str(tmp_path))


def test_jailed_validator_broadcasts_nothing_and_fetches_no_prices(tmp_path, broadcasts):
    rest = FakeRest({"status": "BOND_STATUS_UNBONDING", "jailed": True})
    feeder, last, idle = RecordingFeeder(), [0], [""]

    relayer.run_price_feeder_cycle(make_cycle(rest, tmp_path), feeder, "1000000000asteem", last, idle)

    assert broadcasts == []
    assert feeder.steps == 0, "no price fetch / message build while jailed"
    assert idle[0] == "validator is jailed"
    assert last[0] == 100 // 5, "a definitive 'jailed' answer skips the whole period"


def test_feeder_resumes_after_unjail(tmp_path, broadcasts):
    rest = FakeRest({"status": "BOND_STATUS_UNBONDING", "jailed": True})
    cycle = make_cycle(rest, tmp_path)
    feeder, last, idle = RecordingFeeder(), [0], [""]
    relayer.run_price_feeder_cycle(cycle, feeder, "1000000000asteem", last, idle)
    assert broadcasts == []

    rest.validator = {"status": "BOND_STATUS_BONDED", "jailed": False}
    rest.height = 105  # next vote period
    relayer.run_price_feeder_cycle(cycle, feeder, "1000000000asteem", last, idle)

    assert broadcasts == [["prevote-msg"]]
    assert idle[0] == ""


def test_lookup_failure_broadcasts_nothing_but_retries_next_tick(tmp_path, broadcasts):
    rest = FakeRest(RuntimeError("node unreachable"))
    feeder, last, idle = RecordingFeeder(), [0], [""]

    relayer.run_price_feeder_cycle(make_cycle(rest, tmp_path), feeder, "1000000000asteem", last, idle)

    assert broadcasts == []
    assert last[0] == 0, "not a definitive answer, so the period is retried"
    assert idle[0].startswith("validator lookup failed")


def test_bonded_validator_still_broadcasts(tmp_path, broadcasts):
    rest = FakeRest({"status": "BOND_STATUS_BONDED", "jailed": False})
    relayer.run_price_feeder_cycle(make_cycle(rest, tmp_path), RecordingFeeder(), "1000000000asteem", [0], [""])
    assert broadcasts == [["prevote-msg"]]
