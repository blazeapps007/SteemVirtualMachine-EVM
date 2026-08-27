// config.ts — environment-variable configuration, mirroring oracle/go/main.go
// and relayer/config.go. All three language clients read the same
// oracle/.env (see oracle/.env.example) — this module is the JS side of
// that shared contract.

import { mnemonicToSeedSync } from "@scure/bip39";
import { HDKey } from "@scure/bip32";

import { EthSecp256k1DirectSigner } from "./signer";

/** BIP44 coin type for this chain's key derivation: 60 (Ethereum) — see
 * app/app.go's ChainCoinType and oracle/PROTOCOL.md §1. */
export const CHAIN_COIN_TYPE = 60;
const HD_PATH = `m/44'/${CHAIN_COIN_TYPE}'/0'/0/0`;

export interface Config {
  steemRpcUrl: string;
  nodeRestUrl: string;
  stateDir: string;
  steemSymbol: string;
  sbdSymbol: string;
  pollIntervalMs: number;
  maxBlocksPerPoll: number;
  startBlock: number;
  startBlockIsLatest: boolean;
  cmcApiKey: string;
  cmcBaseUrl: string;
  gasPrices: string;
}

/** Reads and validates every ORACLE_* env var, applying the same defaults
 * as the Go client (see oracle/go/main.go's doc comment). Throws with a
 * clear message on any invalid value — callers should let this surface
 * directly, matching the Go client's fail-fast startup. */
export function loadConfig(env: NodeJS.ProcessEnv = process.env): Config {
  const steemRpcUrl = (env.ORACLE_STEEM_RPC || "").trim();
  if (!steemRpcUrl) {
    throw new Error("ORACLE_STEEM_RPC is required (the Steem RPC to scan)");
  }

  const nodeRestUrl = envOr(env, "ORACLE_NODE_REST", "http://steemvm:1317");
  const stateDir = envOr(env, "ORACLE_STATE_DIR", "/oracle-data");
  // Defaults to "SBD" (mainnet's SBD symbol) now that the chain supports
  // asbd bridging. Unset -> "SBD"; explicitly ORACLE_SBD_SYMBOL="" -> disabled
  // (envOr can't tell "unset" from "set empty", so check presence directly).
  const sbdSymbol = env.ORACLE_SBD_SYMBOL === undefined ? "SBD" : env.ORACLE_SBD_SYMBOL.trim();

  const pollIntervalMs = envDurationMs(env, "ORACLE_POLL_INTERVAL", 60_000);
  const maxBlocksPerPoll = envUint(env, "ORACLE_MAX_BLOCKS", 100);

  const rawStart = (env.ORACLE_START_BLOCK || "").trim().toLowerCase();
  let startBlock = 0;
  let startBlockIsLatest = false;
  if (rawStart === "" || rawStart === "0") {
    // leave startBlock = 0 -> use on-chain relayer_start_block, else tip
  } else if (rawStart === "latest" || rawStart === "now" || rawStart === "tip") {
    startBlockIsLatest = true;
  } else if (/^[0-9]+$/.test(rawStart)) {
    startBlock = Number(rawStart);
  } else {
    throw new Error(`ORACLE_START_BLOCK must be a block number or "latest", got ${JSON.stringify(rawStart)}`);
  }

  return {
    steemRpcUrl,
    nodeRestUrl,
    stateDir,
    steemSymbol: "STEEM", // mainnet only — see relayer/config.go's DefaultConfig doc comment
    sbdSymbol,
    pollIntervalMs,
    maxBlocksPerPoll,
    startBlock,
    startBlockIsLatest,
    cmcApiKey: (env.ORACLE_CMC_API_KEY || "").trim(),
    cmcBaseUrl: (env.ORACLE_CMC_BASE_URL || "").trim(),
    // Price-feed txs are NOT fee-exempt (unlike bridge attestations — see
    // oracle/PROTOCOL.md §3), so this must be non-empty for the feeder to
    // activate. Defaults to 1000000000asteem (matches
    // Instructions/app.toml.example's minimum-gas-prices and this chain's
    // EVM feemarket base_fee) rather than leaving the feeder silently
    // disabled — a missed whitelisted price pair is a missed duty and
    // counts toward jailing/slashing the same as skipping it any other way.
    gasPrices: (env.ORACLE_GAS_PRICES || "1000000000asteem").trim(),
  };
}

/** Loads the signing key from ORACLE_PRIVATE_KEY (hex) or ORACLE_MNEMONIC
 * (BIP39 -> BIP32 m/44'/60'/0'/0/0, standard Ethereum HD derivation — see
 * PROTOCOL.md §1), matching the Go client's buildKeyring. */
export function loadSigner(env: NodeJS.ProcessEnv = process.env): EthSecp256k1DirectSigner {
  const privHex = (env.ORACLE_PRIVATE_KEY || "").trim();
  const mnemonic = (env.ORACLE_MNEMONIC || "").trim();

  if (privHex) {
    const clean = privHex.startsWith("0x") || privHex.startsWith("0X") ? privHex.slice(2) : privHex;
    if (!/^[0-9a-fA-F]{64}$/.test(clean)) {
      throw new Error("ORACLE_PRIVATE_KEY is not valid hex (expected 32 raw bytes / 64 hex chars)");
    }
    return EthSecp256k1DirectSigner.fromPrivateKey(Buffer.from(clean, "hex"));
  }

  if (mnemonic) {
    const seed = mnemonicToSeedSync(mnemonic);
    const node = HDKey.fromMasterSeed(seed).derive(HD_PATH);
    if (!node.privateKey) {
      throw new Error("deriving key from ORACLE_MNEMONIC: HD derivation produced no private key");
    }
    return EthSecp256k1DirectSigner.fromPrivateKey(node.privateKey);
  }

  throw new Error("provide the signing key via ORACLE_PRIVATE_KEY (hex) or ORACLE_MNEMONIC");
}

function envOr(env: NodeJS.ProcessEnv, key: string, def: string): string {
  const v = (env[key] || "").trim();
  return v || def;
}

function envDurationMs(env: NodeJS.ProcessEnv, key: string, defMs: number): number {
  const v = (env[key] || "").trim();
  if (!v) return defMs;
  const match = /^([0-9]+(?:\.[0-9]+)?)(ms|s|m|h)$/.exec(v);
  if (!match) {
    throw new Error(`${key} is not a valid duration (e.g. "1m", "30s"), got ${JSON.stringify(v)}`);
  }
  const [, numStr, unit] = match;
  const num = Number(numStr);
  const unitMs = { ms: 1, s: 1_000, m: 60_000, h: 3_600_000 }[unit]!;
  return num * unitMs;
}

function envUint(env: NodeJS.ProcessEnv, key: string, def: number): number {
  const v = (env[key] || "").trim();
  if (!v) return def;
  if (!/^[0-9]+$/.test(v)) {
    throw new Error(`${key} is not a valid unsigned integer, got ${JSON.stringify(v)}`);
  }
  return Number(v);
}
