// relayer.ts — the main scan/attest loop, mirroring oracle/go/relayer/relayer.go
// field-for-field. Uses the REST gRPC-gateway (:1317) for every chain query
// (params, staking validator lookup, dedup pre-checks) — see
// oracle/PROTOCOL.md §4's recommendation for Python/JS over raw CometBFT
// RPC/gRPC.

import { toBech32, fromBech32 } from "@cosmjs/encoding";
import type { Registry } from "@cosmjs/proto-signing";

import type { Config } from "./config";
import type { EthSecp256k1DirectSigner } from "./signer";
import { SteemClient, extractGatewayTransfers, extractGatewayPayouts } from "./steemClient";
import { routeMemo, buildMsg, Intent, GATEWAY_ACCOUNT } from "./router";
import { broadcastAttestations, broadcastPriceFeedMsgs, type EncodeObject } from "./broadcast";
import { loadState, saveState, loadFeederState, saveFeederState } from "./state";
import { Feeder, getAggregateVoteHash, type PriceSource } from "./priceFeeder";
import { TYPE_URL_MSG_ATTEST_WITHDRAWAL_PAYOUT } from "./broadcast";

export interface CycleLogger {
  info(msg: string, meta?: Record<string, unknown>): void;
  error(msg: string, meta?: Record<string, unknown>): void;
  debug(msg: string, meta?: Record<string, unknown>): void;
}

const VALOPER_HRP = "steemvaloper";

/** Derives the bech32 valoper address from an account bech32 address — same
 * 20 bytes, different HRP (see oracle/PROTOCOL.md §1). Only used for
 * `x/staking` bonded-status lookups; every duty message field uses the
 * account address. */
function toValoperAddress(accountAddress: string): string {
  const { data } = fromBech32(accountAddress);
  return toBech32(VALOPER_HRP, data);
}

async function getJson<T>(restUrl: string, path: string): Promise<T> {
  const resp = await fetch(`${trimSlash(restUrl)}${path}`);
  if (resp.status === 404) {
    const err = new Error(`not found: ${path}`);
    (err as any).notFound = true;
    throw err;
  }
  if (!resp.ok) {
    throw new Error(`GET ${path}: HTTP ${resp.status}: ${await resp.text().catch(() => "")}`);
  }
  return resp.json() as Promise<T>;
}

function trimSlash(url: string): string {
  return url.endsWith("/") ? url.slice(0, -1) : url;
}

interface BridgeParams {
  gateway_account: string;
  bridge_enabled: boolean;
  name_service_enabled: boolean;
  bridge_out_enabled: boolean;
  relayer_start_block: string;
}

async function queryBridgeParams(restUrl: string): Promise<BridgeParams> {
  const resp = await getJson<{ params: BridgeParams }>(restUrl, "/steemvm/steembridge/v1/params");
  return resp.params;
}

async function queryValidatorBonded(restUrl: string, valoperAddr: string): Promise<boolean> {
  try {
    const resp = await getJson<{ validator: { status: string } }>(
      restUrl,
      `/cosmos/staking/v1beta1/validators/${valoperAddr}`,
    );
    return resp.validator.status === "BOND_STATUS_BONDED";
  } catch (err) {
    if ((err as any).notFound) return false;
    throw err;
  }
}

async function depositAlreadyAttested(
  restUrl: string,
  txid: string,
  opIndex: number,
  valoperAddr: string,
): Promise<boolean> {
  try {
    const resp = await getJson<{
      deposit: { status: string; validator_confirmations: { validator_address: string }[] };
    }>(restUrl, `/steemvm/steembridge/v1/deposit_by_txid/${encodeURIComponent(txid)}/${opIndex}`);
    if (resp.deposit.status !== "DEPOSIT_STATUS_PENDING") return true;
    return resp.deposit.validator_confirmations.some((c) => c.validator_address === valoperAddr);
  } catch (err) {
    if ((err as any).notFound) return false;
    throw err;
  }
}

async function nameRegistrationAlreadyAttested(
  restUrl: string,
  txid: string,
  opIndex: number,
  valoperAddr: string,
): Promise<boolean> {
  try {
    const resp = await getJson<{
      registration: { status: string; validator_confirmations: { validator_address: string }[] };
    }>(restUrl, `/steemvm/steembridge/v1/name_registration_by_txid/${encodeURIComponent(txid)}/${opIndex}`);
    if (resp.registration.status !== "NAME_REGISTRATION_STATUS_PENDING") return true;
    return resp.registration.validator_confirmations.some((c) => c.validator_address === valoperAddr);
  } catch (err) {
    if ((err as any).notFound) return false;
    throw err;
  }
}

async function queryOracleDataParams(
  restUrl: string,
): Promise<{ vote_period: string; whitelist: string[] }> {
  const resp = await getJson<{ params: { vote_period: string; whitelist: string[] } }>(
    restUrl,
    "/steemvm/oracle/data/v1/params",
  );
  return resp.params;
}

async function queryLatestHeight(restUrl: string): Promise<number> {
  const resp = await getJson<{ block: { header: { height: string } } }>(
    restUrl,
    "/cosmos/base/tendermint/v1beta1/blocks/latest",
  );
  return Number(resp.block.header.height);
}

/** Waits until the node's REST API is reachable and (best-effort) not
 * catching up, returning the chain-id read from node_info. */
async function waitForLocalNode(logger: CycleLogger, restUrl: string): Promise<string> {
  for (;;) {
    try {
      const [nodeInfo, syncing] = await Promise.all([
        getJson<{ default_node_info: { network: string } }>(restUrl, "/cosmos/base/tendermint/v1beta1/node_info"),
        getJson<{ syncing: boolean }>(restUrl, "/cosmos/base/tendermint/v1beta1/syncing").catch(() => ({
          syncing: false,
        })),
      ]);
      if (syncing.syncing) {
        logger.debug("steem relayer waiting: node is catching up");
        await sleep(2_000);
        continue;
      }
      return nodeInfo.default_node_info.network;
    } catch {
      await sleep(2_000);
    }
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/** initialCursor picks the first-run scan cursor (the last "already
 * scanned" block, i.e. scanning begins at cursor+1): a local start-block
 * override wins, then the chain's relayer_start_block param, then Steem's
 * current last irreversible block. */
function initialCursor(localStart: number, localStartIsLatest: boolean, lib: number, chainStart: number): number {
  if (localStartIsLatest) return lib;
  if (localStart > 0) return localStart - 1;
  if (chainStart > 0) return chainStart - 1;
  return lib;
}

/**
 * Drives the whole oracle: bridge attestation scanning + (optionally) the
 * price feeder, on cfg.pollIntervalMs, until abortSignal fires. Mirrors
 * oracle/go/relayer/relayer.go's Run.
 */
export async function run(opts: {
  logger: CycleLogger;
  cfg: Config;
  signer: EthSecp256k1DirectSigner;
  registry: Registry;
  stateDir: string;
  priceSource: PriceSource | undefined;
  abortSignal: AbortSignal;
}): Promise<void> {
  const { logger, cfg, signer, registry, stateDir, priceSource, abortSignal } = opts;

  const valoperAddr = toValoperAddress(signer.address);
  const steem = new SteemClient(cfg.steemRpcUrl);

  const chainId = await waitForLocalNode(logger, cfg.nodeRestUrl);
  if (abortSignal.aborted) return;

  let state = await loadState(stateDir);
  const feeder = new Feeder(signer.address, priceSource);
  let lastHandledPeriod = 0;

  logger.info("steem oracle started", {
    steem_rpc: cfg.steemRpcUrl,
    node_rest: cfg.nodeRestUrl,
    chain_id: chainId,
    signer: signer.address,
    valoper: valoperAddr,
    poll_interval_ms: cfg.pollIntervalMs,
    last_scanned_block: state.last_scanned_block,
    price_feeder_enabled: priceSource !== undefined,
  });

  let notBondedLogged = false;
  // Heartbeat: the "idle"/"waiting" logs below are debug-level (filtered out
  // by default), so a quiet cycle -- no new transfers to attest, which is
  // most cycles most of the time -- produces zero output at all without
  // this. Mirrors oracle/go/relayer/relayer.go's heartbeat.
  const HEARTBEAT_EVERY = 10;
  let tickCount = 0;

  while (!abortSignal.aborted) {
    await sleepOrAbort(cfg.pollIntervalMs, abortSignal);
    if (abortSignal.aborted) break;

    try {
      const result = await runCycle(logger, cfg, chainId, signer, registry, steem, stateDir, state, valoperAddr, notBondedLogged);
      state = result.state;
      notBondedLogged = result.notBondedLogged;
    } catch (err) {
      logger.error("steem oracle cycle failed", { err: String(err) });
    }

    if (priceSource) {
      try {
        lastHandledPeriod = await runPriceFeederCycle(
          logger,
          cfg,
          chainId,
          signer,
          registry,
          feeder,
          stateDir,
          lastHandledPeriod,
        );
      } catch (err) {
        logger.error("price feeder cycle failed", { err: String(err) });
      }
    }

    tickCount++;
    if (tickCount % HEARTBEAT_EVERY === 0) {
      logger.info("steem oracle heartbeat", { last_scanned_block: state.last_scanned_block });
    }
  }

  logger.info("steem oracle stopped");
}

async function sleepOrAbort(ms: number, signal: AbortSignal): Promise<void> {
  if (signal.aborted) return;
  await new Promise<void>((resolve) => {
    const t = setTimeout(resolve, ms);
    signal.addEventListener("abort", () => {
      clearTimeout(t);
      resolve();
    }, { once: true });
  });
}

interface RelayerCycleState {
  last_scanned_block: number;
}

async function runCycle(
  logger: CycleLogger,
  cfg: Config,
  chainId: string,
  signer: EthSecp256k1DirectSigner,
  registry: Registry,
  steem: SteemClient,
  stateDir: string,
  state: RelayerCycleState,
  valoperAddr: string,
  notBondedLogged: boolean,
): Promise<{ state: RelayerCycleState; notBondedLogged: boolean }> {
  const params = await queryBridgeParams(cfg.nodeRestUrl);
  // GatewayAccount is a hardcoded chain constant, never read from params —
  // see router.GATEWAY_ACCOUNT's doc comment.
  const gateway = GATEWAY_ACCOUNT;
  if (!params.bridge_enabled && !params.name_service_enabled) {
    logger.debug("steem relayer idle: bridge and name service disabled");
    return { state, notBondedLogged };
  }

  const bonded = await queryValidatorBonded(cfg.nodeRestUrl, valoperAddr);
  if (!bonded) {
    if (!notBondedLogged) {
      logger.info("steem relayer idle: key is not a bonded validator", { valoper: valoperAddr });
    }
    return { state, notBondedLogged: true };
  }
  notBondedLogged = false;

  const lib = await steem.lastIrreversibleBlock();

  if (state.last_scanned_block === 0) {
    const cursor = initialCursor(cfg.startBlock, cfg.startBlockIsLatest, lib, Number(params.relayer_start_block));
    const next = { last_scanned_block: cursor };
    logger.info("steem relayer cursor initialized", { cursor });
    await saveState(stateDir, next);
    return { state: next, notBondedLogged };
  }

  const from = state.last_scanned_block + 1;
  const to = Math.min(lib, state.last_scanned_block + cfg.maxBlocksPerPoll);
  if (from > to) {
    return { state, notBondedLogged };
  }

  const blocks = await steem.fetchBlocks(from, to);

  const msgs: EncodeObject[] = [];
  let lastFullBlock = state.last_scanned_block;

  for (const nb of blocks) {
    const transfers = extractGatewayTransfers(nb.num, nb.block, gateway, cfg.steemSymbol, cfg.sbdSymbol);
    const blockMsgs: EncodeObject[] = [];

    for (const transfer of transfers) {
      const intent = routeMemo(transfer.memo);
      if (intent === Intent.Register && !params.name_service_enabled) continue;
      if (intent === Intent.Deposit && !params.bridge_enabled) continue;

      // Only attest transfers a claimable-destination memo parser would
      // accept, mirroring the chain's own DeriveDestination check: a bare
      // steem1.../0x address optionally prefixed svm-deposit/svm-register.
      if (!hasParseableDestination(transfer.memo)) continue;

      const attested =
        intent === Intent.Register
          ? await nameRegistrationAlreadyAttested(cfg.nodeRestUrl, transfer.txid, transfer.opIndex, valoperAddr)
          : await depositAlreadyAttested(cfg.nodeRestUrl, transfer.txid, transfer.opIndex, valoperAddr);
      if (attested) continue;

      blockMsgs.push(buildMsg(transfer, intent, signer.address, gateway));
      logger.info("attesting transfer", {
        intent: intent === Intent.Register ? "name-registration" : "deposit",
        txid: transfer.txid,
        opIndex: transfer.opIndex,
        from: transfer.from,
        amountMillisteem: transfer.amountMillisteem.toString(),
        memo: transfer.memo,
      });
    }

    if (params.bridge_out_enabled) {
      for (const payout of extractGatewayPayouts(nb.num, nb.block, gateway)) {
        blockMsgs.push({
          typeUrl: TYPE_URL_MSG_ATTEST_WITHDRAWAL_PAYOUT,
          value: {
            validator: signer.address,
            withdrawalId: payout.withdrawalId.toString(),
            steemTxid: payout.txid,
            opIndex: payout.opIndex,
            steemBlock: payout.steemBlock,
            steemTimestamp: payout.steemTimestamp,
          },
        });
        logger.info("attesting withdrawal payout", {
          withdrawalId: payout.withdrawalId.toString(),
          steemTxid: payout.txid,
          opIndex: payout.opIndex,
        });
      }
    }

    if (msgs.length + blockMsgs.length > 50) {
      break; // stop at a block boundary; the rest is picked up next cycle
    }
    msgs.push(...blockMsgs);
    lastFullBlock = nb.num;
  }

  if (msgs.length > 0) {
    const result = await broadcastAttestations({ restUrl: cfg.nodeRestUrl, chainId, signer, registry, msgs });
    if (result.code !== 0) {
      throw new Error(`attestation tx rejected (code ${result.code}): ${result.rawLog}`);
    }
    logger.info("steem relayer attested transfers", {
      count: msgs.length,
      blocks: lastFullBlock - state.last_scanned_block,
      tx: result.txHash,
    });
  }

  if (lastFullBlock !== state.last_scanned_block) {
    const next = { last_scanned_block: lastFullBlock };
    await saveState(stateDir, next);
    return { state: next, notBondedLogged };
  }
  return { state, notBondedLogged };
}

const HEX_ADDRESS_RE = /^0x[0-9a-fA-F]{40}$/;

// hasParseableDestination mirrors x/oracle/bridge/types/memo.go's
// DeriveDestination exactly enough to decide whether a transfer is worth
// attesting at all (the Go relayer applies the identical gate in
// relayer.go's runCycle) — full bech32 checksum validation via
// @cosmjs/encoding's fromBech32 (not just a shape regex), same
// single-leading-prefix stripping rule, same 0x-EVM-address fallback. The
// chain re-derives the destination itself at resolution time regardless —
// this is purely a "don't waste a broadcast on an obviously unparseable
// memo" pre-filter, never the source of truth.
function hasParseableDestination(memo: string): boolean {
  let trimmed = memo.trim();

  for (const prefix of ["svm-deposit", "svm-register"]) {
    if (trimmed.startsWith(prefix)) {
      const rest = trimmed.slice(prefix.length);
      if (rest === "" || rest[0] === " " || rest[0] === "\t") {
        trimmed = rest.trim();
      }
      break;
    }
  }

  try {
    const { prefix } = fromBech32(trimmed);
    if (prefix === ACCOUNT_HRP_FOR_MEMO) return true;
  } catch {
    // not a valid bech32 string at all — fall through to the hex check
  }

  return HEX_ADDRESS_RE.test(trimmed);
}

// Local constant to avoid importing signer.ts's ACCOUNT_HRP just for this
// one check (relayer.ts already imports enough from signer.ts's siblings);
// kept in sync with app/config.go's account bech32 prefix "steem".
const ACCOUNT_HRP_FOR_MEMO = "steem";

async function runPriceFeederCycle(
  logger: CycleLogger,
  cfg: Config,
  chainId: string,
  signer: EthSecp256k1DirectSigner,
  registry: Registry,
  feeder: Feeder,
  stateDir: string,
  lastHandledPeriod: number,
): Promise<number> {
  const params = await queryOracleDataParams(cfg.nodeRestUrl);
  const votePeriod = Number(params.vote_period);
  if (votePeriod === 0) {
    throw new Error("oracledata vote period is zero");
  }

  const height = await queryLatestHeight(cfg.nodeRestUrl);
  const period = Math.floor(height / votePeriod);
  if (period === lastHandledPeriod) {
    return lastHandledPeriod;
  }

  const prevState = await loadFeederState(stateDir);
  const { msgs, state: newState } = await feeder.step(period, params.whitelist, prevState);
  if (msgs.length === 0) {
    return period;
  }

  if (prevState.exchange_rates !== "" && prevState.prevote_period + 1 === period) {
    logger.info("submitting price vote", { period, exchangeRates: prevState.exchange_rates });
  }
  if (newState.exchange_rates !== "") {
    // Logs the hash, not the rates themselves -- mirrors the commit-reveal
    // scheme's own hide-until-reveal semantics (oracle/go/relayer does the
    // same), even though this is a local-only log with no other observer.
    logger.info("submitting price prevote", {
      period,
      hash: getAggregateVoteHash(newState.salt, newState.exchange_rates, signer.address),
    });
  }

  const result = await broadcastPriceFeedMsgs({
    restUrl: cfg.nodeRestUrl,
    chainId,
    signer,
    registry,
    msgs,
    gasPrices: cfg.gasPrices,
  });
  if (result.code !== 0) {
    throw new Error(`price feed tx rejected (code ${result.code}): ${result.rawLog}`);
  }
  logger.info("price feeder broadcast", { period, count: msgs.length, tx: result.txHash });

  await saveFeederState(stateDir, newState);
  return period;
}
