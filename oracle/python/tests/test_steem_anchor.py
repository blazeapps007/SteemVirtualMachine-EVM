"""The chain's Steem anchor (relayer_start_block + relayer_start_block_id in
genesis): oracles verify their Steem node against it and never scan before it
(mirrors oracle/go/relayer/anchor_test.go)."""

from types import SimpleNamespace

import pytest

from steemvm_oracle import relayer
from steemvm_oracle.state import State

ANCHOR = 110435764
ANCHOR_ID = "06951db4" + "ab" * 16


class FakeSteem:
    def __init__(self, block_id: str, lib: int = ANCHOR + 50):
        self.id, self.lib, self.calls = block_id, lib, 0

    def block_id(self, num):
        assert num == ANCHOR
        self.calls += 1
        return self.id

    def last_irreversible_block(self):
        return self.lib

    def fetch_blocks(self, frm, to):
        return [(n, {"transactions": []}) for n in range(frm, to + 1)]


class FakeRest:
    def __init__(self, block=ANCHOR, block_id=ANCHOR_ID):
        self.params = {"bridge_enabled": True, "name_service_enabled": True, "bridge_out_enabled": True,
                       "relayer_start_block": str(block), "relayer_start_block_id": block_id}

    def get_json(self, path, params=None, headers=None):
        if path == "/steemvm/steembridge/v1/params":
            return {"params": self.params}
        if path.startswith("/cosmos/staking/v1beta1/validators/"):
            return {"validator": {"status": "BOND_STATUS_BONDED", "jailed": False}}
        raise AssertionError(path)


def make_cycle(rest, steem, tmp_path, start_block=0):
    cfg = SimpleNamespace(start_block=start_block, max_blocks_per_poll=100, steem_symbol="STEEM", sbd_symbol="SBD")
    keypair = SimpleNamespace(address="steem1signer", valoper_address="steemvaloper1x")
    return relayer.Cycle(rest=rest, steem=steem, keypair=keypair, chain_id="steemvm-2", cfg=cfg, state_dir=str(tmp_path))


def test_anchored_cursor():
    assert relayer._anchored_cursor(500, 0) == 500
    assert relayer._anchored_cursor(100, ANCHOR) == ANCHOR - 1
    assert relayer._anchored_cursor(ANCHOR + 9, ANCHOR) == ANCHOR + 9
    assert relayer._anchored_cursor(relayer._initial_cursor(5, ANCHOR, 0), ANCHOR) == ANCHOR - 1


def test_first_run_starts_at_the_anchor_even_with_a_lower_local_override(tmp_path):
    steem = FakeSteem(ANCHOR_ID)
    state = relayer.run_cycle(make_cycle(FakeRest(), steem, tmp_path, start_block=5), State(last_scanned_block=0), [False])
    assert state.last_scanned_block == ANCHOR - 1
    assert steem.calls == 1


def test_a_saved_cursor_from_a_previous_chain_jumps_to_the_anchor(tmp_path):
    state = relayer.run_cycle(make_cycle(FakeRest(), FakeSteem(ANCHOR_ID), tmp_path), State(last_scanned_block=1000), [False])
    assert state.last_scanned_block == ANCHOR - 1


def test_anchor_verified_once(tmp_path):
    steem = FakeSteem(ANCHOR_ID)
    cycle = make_cycle(FakeRest(), steem, tmp_path)
    state = relayer.run_cycle(cycle, State(last_scanned_block=0), [False])
    relayer.run_cycle(cycle, state, [False])
    assert steem.calls == 1 and cycle.verified_anchor == ANCHOR_ID


def test_a_mismatched_anchor_attests_nothing(tmp_path, monkeypatch):
    monkeypatch.setattr(relayer, "broadcast_attestations", lambda *a, **k: pytest.fail("must not broadcast"))
    cycle = make_cycle(FakeRest(), FakeSteem("06951db4" + "cd" * 16), tmp_path)
    for _ in range(2):  # never cached as verified
        with pytest.raises(relayer.SteemAnchorMismatch):
            relayer.run_cycle(cycle, State(last_scanned_block=ANCHOR + 10), [False])


def test_no_recorded_anchor_verifies_nothing(tmp_path):
    steem = FakeSteem(ANCHOR_ID)
    relayer.run_cycle(make_cycle(FakeRest(block=0, block_id=""), steem, tmp_path), State(last_scanned_block=0), [False])
    assert steem.calls == 0
