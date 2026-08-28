"""Main scan/attest loop. Ports ``oracle/go/relayer/relayer.go``, adapted
for this client's REST-only transport (``oracle/PROTOCOL.md`` SS4): every
CometBFT-RPC/gRPC query the Go client makes has a REST (gRPC-gateway)
equivalent used here instead.

Bridge attestations are simpler than the price feeder (no fee/gas-price
concerns -- fee-exempt for bonded validators), so this module drives both:
one bridge-scan cycle, then (if configured) one price-feeder cycle, per
poll tick -- matching the Go reference's combined loop.
"""

from __future__ import annotations

import logging
import threading
from dataclasses import dataclass
from typing import Optional

from . import _protopath  # noqa: F401
from .broadcast import MAX_MSGS_PER_TX, NotFoundError, RestClient, broadcast_attestations, broadcast_price_feed_msgs
from .config import Config
from .keys import Keypair
from .pricefeeder import CompositePriceSource, Feeder
from .router import GATEWAY_ACCOUNT, Intent, build_payout_any, build_transfer_any, derive_destination, route_memo
from .state import FeederState, State, load_feeder_state, load_state, save_feeder_state, save_state
from .steem_client import SteemClient, extract_gateway_payouts, extract_gateway_transfers

logger = logging.getLogger("steemvm_oracle.relayer")


# --------------------------------------------------------------------------
# Node queries (REST). Every one has a CometBFT-RPC/gRPC counterpart in the
# Go client; oracle/PROTOCOL.md SS4 explains why REST is preferred here.
# --------------------------------------------------------------------------


def query_bridge_params(rest: RestClient) -> dict:
    return rest.get_json("/steemvm/steembridge/v1/params")["params"]


def query_oracledata_params(rest: RestClient) -> dict:
    return rest.get_json("/steemvm/oracle/data/v1/params")["params"]


def query_validator_bonded(rest: RestClient, valoper_address: str) -> bool:
    try:
        resp = rest.get_json(f"/cosmos/staking/v1beta1/validators/{valoper_address}")
    except NotFoundError:
        return False
    return resp.get("validator", {}).get("status") == "BOND_STATUS_BONDED"


def query_deposit_by_txid(rest: RestClient, txid: str, op_index: int) -> Optional[dict]:
    try:
        return rest.get_json(f"/steemvm/steembridge/v1/deposit_by_txid/{txid}/{op_index}")["deposit"]
    except NotFoundError:
        return None


def query_name_registration_by_txid(rest: RestClient, txid: str, op_index: int) -> Optional[dict]:
    try:
        return rest.get_json(f"/steemvm/steembridge/v1/name_registration_by_txid/{txid}/{op_index}")[
            "registration"
        ]
    except NotFoundError:
        return None


def query_node_chain_id(rest: RestClient) -> str:
    return rest.get_json("/cosmos/base/tendermint/v1beta1/node_info")["default_node_info"]["network"]


def query_is_syncing(rest: RestClient) -> bool:
    return bool(rest.get_json("/cosmos/base/tendermint/v1beta1/syncing").get("syncing", False))


def query_latest_height(rest: RestClient) -> int:
    resp = rest.get_json("/cosmos/base/tendermint/v1beta1/blocks/latest")
    return int(resp["block"]["header"]["height"])


# --------------------------------------------------------------------------
# Cursor / attestation-dedup helpers
# --------------------------------------------------------------------------


def _initial_cursor(local_start: int, chain_start: int, lib: int) -> int:
    """Picks the first-run scan cursor (the last "already scanned" block,
    i.e. scanning begins at cursor+1). Precedence: a local start-block
    override, then the chain-wide relayer_start_block param, then Steem's
    current last irreversible block."""
    if local_start > 0:
        return local_start - 1
    if chain_start > 0:
        return chain_start - 1
    return lib


def _has_confirmation(confirmations: list[dict], valoper_address: str) -> bool:
    return any(c.get("validator_address") == valoper_address for c in confirmations)


def _already_attested(rest: RestClient, transfer, intent: Intent, valoper_address: str) -> bool:
    """Reports whether this validator has already confirmed the transfer's
    (txid, op_index) key. Keeps rescans idempotent -- a duplicate message
    would disqualify the whole zero-fee tx. Mirrors
    oracle/go/relayer/relayer.go's alreadyAttested."""
    if intent == Intent.REGISTER:
        reg = query_name_registration_by_txid(rest, transfer.txid, transfer.op_index)
        if reg is None:
            return False
        if reg.get("status") != "NAME_REGISTRATION_STATUS_PENDING":
            return True
        return _has_confirmation(reg.get("validator_confirmations", []), valoper_address)

    dep = query_deposit_by_txid(rest, transfer.txid, transfer.op_index)
    if dep is None:
        return False
    if dep.get("status") != "DEPOSIT_STATUS_PENDING":
        return True
    return _has_confirmation(dep.get("validator_confirmations", []), valoper_address)


# --------------------------------------------------------------------------
# Poll cycles
# --------------------------------------------------------------------------


@dataclass
class Cycle:
    """Long-lived handles a poll cycle needs."""

    rest: RestClient
    steem: SteemClient
    keypair: Keypair
    chain_id: str
    cfg: Config
    state_dir: str


def run_cycle(cycle: Cycle, state: State, not_bonded_logged: list[bool]) -> State:
    """One poll: check feature flags and bonded status, scan new
    irreversible Steem blocks, attest gateway transfers, advance cursor.
    Mirrors oracle/go/relayer/relayer.go's runCycle."""
    params = query_bridge_params(cycle.rest)
    # GatewayAccount is a hardcoded chain constant, never read from params --
    # see router.GATEWAY_ACCOUNT's doc comment.
    gateway = GATEWAY_ACCOUNT
    bridge_enabled = bool(params.get("bridge_enabled"))
    name_service_enabled = bool(params.get("name_service_enabled"))
    bridge_out_enabled = bool(params.get("bridge_out_enabled"))

    if not (bridge_enabled or name_service_enabled):
        logger.debug("steem relayer idle: bridge and name service disabled")
        return state

    # Only bonded validators' attestations count (and only theirs are
    # fee-exempt) -- idle quietly otherwise.
    if not query_validator_bonded(cycle.rest, cycle.keypair.valoper_address):
        if not not_bonded_logged[0]:
            logger.info("steem relayer idle: key is not a bonded validator (valoper=%s)", cycle.keypair.valoper_address)
            not_bonded_logged[0] = True
        return state
    not_bonded_logged[0] = False

    lib = cycle.steem.last_irreversible_block()

    # First run: establish the cursor.
    if state.last_scanned_block == 0:
        state.last_scanned_block = _initial_cursor(cycle.cfg.start_block, int(params.get("relayer_start_block", 0)), lib)
        logger.info("steem relayer cursor initialized: cursor=%d", state.last_scanned_block)
        save_state(cycle.state_dir, state)
        return state

    from_block = state.last_scanned_block + 1
    to_block = min(lib, state.last_scanned_block + cycle.cfg.max_blocks_per_poll)
    if from_block > to_block:
        return state

    blocks = cycle.steem.fetch_blocks(from_block, to_block)

    # Accumulate attestations across blocks, keeping block boundaries so the
    # cursor only advances past fully-included blocks. One tx per cycle.
    msgs = []
    last_full_block = state.last_scanned_block
    for block_num, block in blocks:
        transfers = extract_gateway_transfers(block_num, block, gateway, cycle.cfg.steem_symbol, cycle.cfg.sbd_symbol)
        block_msgs = []
        for transfer in transfers:
            intent = route_memo(transfer.memo)
            if intent == Intent.REGISTER and not name_service_enabled:
                continue
            if intent == Intent.DEPOSIT and not bridge_enabled:
                continue
            # Only attest transfers whose memo resolves to a supported
            # destination -- the exact in-consensus parser.
            _, _, ok = derive_destination(transfer.memo)
            if not ok:
                continue
            if _already_attested(cycle.rest, transfer, intent, cycle.keypair.valoper_address):
                continue
            block_msgs.append(build_transfer_any(transfer, intent, cycle.keypair.address, gateway))
            intent_label = "name-registration" if intent == Intent.REGISTER else "deposit"
            logger.info(
                "attesting transfer: intent=%s txid=%s op_index=%d from=%s amount_millisteem=%d memo=%s",
                intent_label, transfer.txid, transfer.op_index, transfer.from_,
                transfer.amount_millisteem, transfer.memo,
            )

        # Withdrawal payouts: optimistic, no per-validator dedup query
        # needed (chain benign-no-ops duplicates at 2/3 threshold).
        if bridge_out_enabled:
            for payout in extract_gateway_payouts(block_num, block, gateway, cycle.cfg.steem_symbol, cycle.cfg.sbd_symbol):
                block_msgs.append(build_payout_any(payout, cycle.keypair.address))
                logger.info(
                    "attesting withdrawal payout: withdrawal_id=%d steem_txid=%s op_index=%d",
                    payout.withdrawal_id, payout.txid, payout.op_index,
                )

        if len(msgs) + len(block_msgs) > MAX_MSGS_PER_TX:
            break  # stop at a block boundary; the rest is picked up next cycle
        msgs.extend(block_msgs)
        last_full_block = block_num

    if msgs:
        tx_hash = broadcast_attestations(cycle.rest, cycle.keypair, cycle.chain_id, msgs)
        logger.info(
            "steem relayer attested transfers: count=%d blocks=%d tx=%s",
            len(msgs), last_full_block - state.last_scanned_block, tx_hash,
        )

    if last_full_block != state.last_scanned_block:
        state.last_scanned_block = last_full_block
        save_state(cycle.state_dir, state)

    return state


def run_price_feeder_cycle(
    cycle: Cycle,
    feeder: Feeder,
    gas_prices: str,
    last_handled_period: list[int],
) -> None:
    """Checks whether the chain has entered a new vote period since the
    last handled one and, if so, runs one Feeder.step. Mirrors
    oracle/go/relayer/relayer.go's runPriceFeederCycle."""
    oracle_params = query_oracledata_params(cycle.rest)
    vote_period = int(oracle_params.get("vote_period", 0))
    if vote_period == 0:
        raise RuntimeError("oracledata vote period is zero")
    whitelist = oracle_params.get("whitelist", [])

    height = query_latest_height(cycle.rest)
    period = height // vote_period

    if period == last_handled_period[0]:
        return  # already acted this period; wait for the next boundary

    prev_state: FeederState = load_feeder_state(cycle.state_dir)
    msgs, new_state = feeder.step(period, whitelist, prev_state)

    if not msgs:
        last_handled_period[0] = period
        return

    tx_hash = broadcast_price_feed_msgs(cycle.rest, cycle.keypair, cycle.chain_id, msgs, gas_prices)
    logger.info("price feeder broadcast: period=%d count=%d tx=%s", period, len(msgs), tx_hash)

    save_feeder_state(cycle.state_dir, new_state)
    last_handled_period[0] = period


# --------------------------------------------------------------------------
# Top-level Run loop
# --------------------------------------------------------------------------


def wait_for_local_node(rest: RestClient, stop: threading.Event) -> str:
    """Blocks until the local node REST responds and is done catching up,
    returning the chain-id (empty when stop is set). Mirrors
    oracle/go/relayer/relayer.go's waitForLocalNode."""
    while not stop.is_set():
        if stop.wait(2.0):
            return ""
        try:
            if query_is_syncing(rest):
                logger.debug("steem relayer waiting: node is catching up")
                continue
            return query_node_chain_id(rest)
        except Exception:  # noqa: BLE001 - node still booting
            continue
    return ""


def run(
    keypair: Keypair,
    node_rest: str,
    cfg: Config,
    state_dir: str,
    price_source: Optional[CompositePriceSource],
    gas_prices: str,
    stop: threading.Event,
) -> None:
    """Drives the relayer loop until `stop` is set. Mirrors
    oracle/go/relayer/relayer.go's Run. `price_source`, if non-None,
    activates the price feeder (x/oracle/data) on the same poll loop --
    gas_prices must be set too (price-feed txs are NOT fee-exempt)."""
    rest = RestClient(node_rest)
    steem = SteemClient(cfg.steem_rpc_url)
    try:
        chain_id = wait_for_local_node(rest, stop)
        if not chain_id:
            return  # stop was set while waiting

        state = load_state(state_dir)
        feeder = Feeder(validator=keypair.address, source=price_source)
        last_handled_period = [0]
        not_bonded_logged = [False]

        logger.info(
            "steem oracle started: steem_rpc=%s node_rest=%s chain_id=%s signer=%s valoper=%s "
            "poll_interval=%ss last_scanned_block=%d price_feeder_enabled=%s",
            cfg.steem_rpc_url, node_rest, chain_id, keypair.address, keypair.valoper_address,
            cfg.poll_interval_seconds, state.last_scanned_block, price_source is not None,
        )

        cycle = Cycle(rest=rest, steem=steem, keypair=keypair, chain_id=chain_id, cfg=cfg, state_dir=state_dir)

        # Heartbeat: the "idle"/"waiting" logs below are debug-level (filtered
        # out by default), so a quiet cycle -- no new transfers to attest,
        # which is most cycles most of the time -- produces zero output at
        # all without this. Mirrors oracle/go/relayer/relayer.go's heartbeat.
        HEARTBEAT_EVERY = 10
        tick_count = 0

        while not stop.is_set():
            if stop.wait(cfg.poll_interval_seconds):
                break

            try:
                state = run_cycle(cycle, state, not_bonded_logged)
            except Exception:  # noqa: BLE001
                logger.exception("steem oracle cycle failed")

            if price_source is not None:
                try:
                    run_price_feeder_cycle(cycle, feeder, gas_prices, last_handled_period)
                except Exception:  # noqa: BLE001
                    logger.exception("price feeder cycle failed")

            tick_count += 1
            if tick_count % HEARTBEAT_EVERY == 0:
                logger.info("steem oracle heartbeat: last_scanned_block=%d", state.last_scanned_block)

        logger.info("steem oracle stopped")
    finally:
        rest.close()
        steem.close()
