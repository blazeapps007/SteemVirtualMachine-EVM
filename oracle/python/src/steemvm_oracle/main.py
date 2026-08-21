"""Command ``python -m steemvm_oracle.main``: the standalone SteemVM bridge
oracle (Python client). Mirrors ``oracle/go/main.go``, adapted for this
client's REST-only transport (``oracle/PROTOCOL.md`` SS4).

Configuration is entirely from environment variables (see ``oracle/.env.example``):

    ORACLE_STEEM_RPC      (required) Steem RPC to scan, e.g. https://api.steemit.com
    ORACLE_NODE_REST      SteemVM REST API to broadcast/query against  (default http://steemvm:1317)
    ORACLE_PRIVATE_KEY    validator key as hex (0x... eth_secp256k1)  --+ one of these
    ORACLE_MNEMONIC       validator key as a BIP39 mnemonic            --+ is required
    ORACLE_STATE_DIR      dir for the scan-cursor / price-feeder state files  (default /oracle-data)
    ORACLE_POLL_INTERVAL  Steem poll cadence                    (default 1m)
    ORACLE_MAX_BLOCKS     max Steem blocks scanned per cycle     (default 100)
    ORACLE_START_BLOCK    fresh-scan start: a block number, or "latest" to start
                           at Steem's current tip (new validators skip all history)
                           (default 0 = use the chain's relayer_start_block anchor)
    ORACLE_SBD_SYMBOL     Steem symbol that counts as bridgeable SBD (default "SBD");
                           set to "" to disable SBD bridging
    ORACLE_CMC_API_KEY    CoinMarketCap API key, prices STEEM/USD_External + SBD/USD_External.
                           Empty skips those two pairs (see oracle/PROTOCOL.md SS7).
    ORACLE_CMC_BASE_URL   CoinMarketCap API base URL (default the production API)
    ORACLE_GAS_PRICES     price-feed txs are NOT fee-exempt, unlike bridge attestations
                           (default "1000000000asteem"; see oracle/PROTOCOL.md SS3)
"""

from __future__ import annotations

import logging
import signal
import sys
import threading
from types import FrameType
from typing import Optional

from . import _protopath  # noqa: F401
from .config import Config, parse_duration_seconds
from .keys import Keypair, from_mnemonic, from_private_key_hex
from .pricefeeder import CompositePriceSource
from .pricesources.cmc import CMCClient
from .pricesources.steem_prices import SteemPriceSource
from .relayer import run
from .steem_client import SteemClient

logger = logging.getLogger("steemvm_oracle.main")


def _env(env: dict, key: str, default: str = "") -> str:
    return env.get(key, "").strip() or default


def build_config(env: dict) -> tuple[str, str, str, Config]:
    """Returns (steem_rpc, node_rest, state_dir, Config)."""
    steem_rpc = _env(env, "ORACLE_STEEM_RPC")
    if not steem_rpc:
        raise SystemExit("ORACLE_STEEM_RPC is required (the Steem RPC to scan)")
    node_rest = _env(env, "ORACLE_NODE_REST", "http://steemvm:1317")
    state_dir = _env(env, "ORACLE_STATE_DIR", "/oracle-data")

    cfg = Config(steem_rpc_url=steem_rpc)
    # Defaults to "SBD" now that the chain supports asbd bridging. Unset ->
    # "SBD"; explicitly ORACLE_SBD_SYMBOL="" -> disabled (_env's "or default"
    # can't tell "unset" from "set empty", so check presence directly).
    cfg.sbd_symbol = env["ORACLE_SBD_SYMBOL"].strip() if "ORACLE_SBD_SYMBOL" in env else "SBD"

    poll_interval = _env(env, "ORACLE_POLL_INTERVAL")
    if poll_interval:
        try:
            cfg.poll_interval_seconds = parse_duration_seconds(poll_interval)
        except ValueError as exc:
            raise SystemExit(f"ORACLE_POLL_INTERVAL is not a valid duration (e.g. 3s): {exc}") from exc

    max_blocks = _env(env, "ORACLE_MAX_BLOCKS")
    if max_blocks:
        try:
            cfg.max_blocks_per_poll = int(max_blocks)
        except ValueError as exc:
            raise SystemExit(f"ORACLE_MAX_BLOCKS must be a valid unsigned integer: {exc}") from exc

    # ORACLE_START_BLOCK: where a *fresh* oracle (no saved cursor yet)
    # begins its Steem scan. Ignored once a cursor exists in the state dir.
    start_block_raw = _env(env, "ORACLE_START_BLOCK").lower()
    if start_block_raw in ("", "0"):
        pass  # cfg.start_block stays 0 -> use on-chain relayer_start_block, else tip
    elif start_block_raw in ("latest", "now", "tip"):
        lib = SteemClient(steem_rpc).last_irreversible_block()
        cfg.start_block = lib
        logger.info("oracle will start from the current Steem tip: start_block=%d", lib)
    else:
        try:
            cfg.start_block = int(start_block_raw, 10)
        except ValueError as exc:
            raise SystemExit(f'ORACLE_START_BLOCK must be a block number or "latest": {exc}') from exc

    return steem_rpc, node_rest, state_dir, cfg


def build_keypair(env: dict) -> Keypair:
    priv_hex = _env(env, "ORACLE_PRIVATE_KEY")
    mnemonic = _env(env, "ORACLE_MNEMONIC")
    if priv_hex:
        try:
            return from_private_key_hex(priv_hex)
        except ValueError as exc:
            raise SystemExit(f"ORACLE_PRIVATE_KEY is not valid: {exc}") from exc
    if mnemonic:
        try:
            return from_mnemonic(mnemonic)
        except ValueError as exc:
            raise SystemExit(f"ORACLE_MNEMONIC is not valid: {exc}") from exc
    raise SystemExit("provide the signing key via ORACLE_PRIVATE_KEY (hex) or ORACLE_MNEMONIC")


def build_price_source(env: dict, steem_rpc: str) -> tuple[Optional[CompositePriceSource], str]:
    """Returns (price_source_or_None, gas_prices). Price-feed txs are NOT
    fee-exempt (unlike bridge attestations -- see oracle/PROTOCOL.md SS3), so
    gas_prices must be non-empty for the feeder to activate. Defaults to
    1000000000asteem (matches Instructions/app.toml.example's
    minimum-gas-prices and this chain's EVM feemarket base_fee) rather than
    leaving the feeder silently disabled -- a missed whitelisted price pair
    is a missed duty and counts toward jailing/slashing the same as skipping
    it any other way."""
    gas_prices = _env(env, "ORACLE_GAS_PRICES", "1000000000asteem")

    cmc = None
    cmc_key = _env(env, "ORACLE_CMC_API_KEY")
    if cmc_key:
        cmc = CMCClient(cmc_key, _env(env, "ORACLE_CMC_BASE_URL"))
    else:
        logger.info("price feeder: ORACLE_CMC_API_KEY not set, STEEM/USD_External and SBD/USD_External will be skipped")

    steem_source = SteemPriceSource(SteemClient(steem_rpc))
    logger.info("price feeder enabled: gas_prices=%s", gas_prices)
    return CompositePriceSource(cmc=cmc, steem=steem_source), gas_prices


def main() -> None:
    import os

    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s steem-oracle: %(message)s")

    env = dict(os.environ)
    steem_rpc, node_rest, state_dir, cfg = build_config(env)
    keypair = build_keypair(env)
    logger.info("oracle key loaded: address=%s node_rest=%s steem_rpc=%s", keypair.address, node_rest, steem_rpc)

    price_source, gas_prices = build_price_source(env, steem_rpc)

    # Cancel cleanly on SIGINT/SIGTERM so the container stops promptly.
    stop = threading.Event()

    def _handle_signal(signum: int, frame: Optional[FrameType]) -> None:  # noqa: ARG001
        logger.info("received signal %s, shutting down", signum)
        stop.set()

    signal.signal(signal.SIGINT, _handle_signal)
    signal.signal(signal.SIGTERM, _handle_signal)

    try:
        run(keypair, node_rest, cfg, state_dir, price_source, gas_prices, stop)
    except Exception:
        logger.exception("oracle exited")
        sys.exit(1)


if __name__ == "__main__":
    main()
