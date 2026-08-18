// steemClient.ts — a minimal Steem condenser_api JSON-RPC client, mirroring
// oracle/go/relayer/steem.go field-for-field. Only reads (dynamic global
// properties + blocks + ticker/feed history), so a full Steem SDK dependency
// is deliberately avoided.

import { BridgeAsset } from "./proto/steemvm/steembridge/v1/asset";
import { parseLegacyDec, formatLegacyDec, toLegacyDecString } from "./decimalFmt";

/** One Steem transfer operation addressed to the gateway account, carrying
 * exactly the raw facts a validator attests on-chain. All fields must be
 * derived deterministically from Steem block data so every honest validator
 * submits identical values (the module rejects mismatches). */
export interface Transfer {
  txid: string;
  opIndex: number;
  steemBlock: number;
  steemTimestamp: string;
  from: string;
  amountMillisteem: bigint;
  memo: string;
  asset: BridgeAsset;
}

/** A gateway→user payout of a bridge-out on Steem, identified by its
 * "svm-withdrawal <id>" memo. */
export interface Payout {
  withdrawalId: bigint;
  txid: string;
  opIndex: number;
  steemBlock: number;
  steemTimestamp: string;
}

interface SteemBlock {
  timestamp: string;
  transaction_ids?: string[];
  transactions: SteemTx[];
}

interface SteemTx {
  transaction_id?: string;
  operations: unknown[];
}

interface TransferOp {
  from: string;
  to: string;
  amount: string;
  memo: string;
}

export interface NumberedBlock {
  num: number;
  block: SteemBlock;
}

export class SteemClient {
  private readonly url: string;
  private nextId = 1;

  constructor(rpcUrl: string) {
    this.url = rpcUrl;
  }

  private async call<T>(method: string, params: unknown[]): Promise<T> {
    const id = this.nextId++;
    const resp = await fetch(this.url, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ jsonrpc: "2.0", method, params, id }),
    });
    if (!resp.ok) {
      throw new Error(`steem rpc ${method}: HTTP ${resp.status}`);
    }
    const body = (await resp.json()) as { result?: T; error?: { code: number; message: string } };
    if (body.error) {
      throw new Error(`steem rpc ${method}: ${body.error.message} (code ${body.error.code})`);
    }
    return body.result as T;
  }

  /** Steem's current last irreversible block — the relayer only ever scans
   * irreversible blocks, so attested facts can never be undone by a Steem
   * fork. */
  async lastIrreversibleBlock(): Promise<number> {
    const dgp = await this.call<{ last_irreversible_block_num: number }>(
      "condenser_api.get_dynamic_global_properties",
      [],
    );
    if (!dgp.last_irreversible_block_num) {
      throw new Error("steem rpc: zero last irreversible block");
    }
    return dgp.last_irreversible_block_num;
  }

  /** Returns blocks from..to inclusive, fetched sequentially. A null result
   * for a block at or below the last irreversible block is an error, not a
   * skip: aborting the cycle makes the relayer retry rather than silently
   * stepping the cursor over a block a lagging RPC backend failed to serve. */
  async fetchBlocks(from: number, to: number): Promise<NumberedBlock[]> {
    if (from > to) {
      return [];
    }
    const blocks: NumberedBlock[] = [];
    for (let num = from; num <= to; num++) {
      const block = await this.call<SteemBlock | null>("condenser_api.get_block", [num]);
      if (!block) {
        throw new Error(`steem rpc returned no data for irreversible block ${num}`);
      }
      blocks.push({ num, block });
    }
    return blocks;
  }

  /** Steem's internal-market last-trade price — SBD per STEEM, i.e. the
   * STEEM/SBD_Internal pair (see oracle/PROTOCOL.md §7) — via
   * condenser_api.get_ticker's "latest" field. */
  async getTicker(): Promise<string> {
    const resp = await this.call<{ latest: string }>("condenser_api.get_ticker", []);
    return toLegacyDecString(resp.latest.trim());
  }

  /** Steem's current witness-median feed price — SBD per STEEM, i.e. the
   * Price_Feed pair — via condenser_api.get_feed_history's
   * current_median_history base/quote pair. */
  async getFeedHistory(): Promise<string> {
    const resp = await this.call<{
      current_median_history: { base: string; quote: string };
    }>("condenser_api.get_feed_history", []);
    const base = parseAssetAmountDec(resp.current_median_history.base, "SBD");
    const quote = parseAssetAmountDec(resp.current_median_history.quote, "STEEM");
    if (quote === 0n) {
      throw new Error("steem rpc: feed history quote is zero");
    }
    // base/quote as a LegacyDec-precision division: both are already scaled
    // by 1e18 (parseLegacyDec's internal representation), so
    // (base * 1e18) / quote keeps 18 fractional digits of precision.
    const scaled = (base * 10n ** 18n) / quote;
    return formatLegacyDec(scaled);
  }
}

function parseAssetAmountDec(amount: string, symbol: string): bigint {
  const parts = amount.trim().split(/\s+/);
  if (parts.length !== 2 || parts[1] !== symbol) {
    throw new Error(`expected a ${JSON.stringify(symbol)} amount, got ${JSON.stringify(amount)}`);
  }
  return parseLegacyDec(parts[0]);
}

/** Parses a gateway payout memo "svm-withdrawal <id>" into the withdrawal
 * id. The prefix must be a whole token and the id a plain base-10
 * integer; anything else returns undefined so the relayer ignores it. */
export function parseWithdrawalMemo(memo: string): bigint | undefined {
  const fields = memo.trim().split(/\s+/);
  if (fields.length !== 2 || fields[0] !== "svm-withdrawal") {
    return undefined;
  }
  if (!/^[0-9]+$/.test(fields[1])) {
    return undefined;
  }
  return BigInt(fields[1]);
}

/** Converts a Steem asset string like "70.561 STEEM" into millisteem
 * (70561n). Only the given bridgeable symbol qualifies; other symbols and
 * malformed amounts return undefined. Parsing is integer-only — floats
 * would risk validator-to-validator divergence. The asset has exactly 3
 * decimal places on both mainnet and testnet. */
export function parseSteemAmount(amount: string, symbol: string): bigint | undefined {
  const parts = amount.trim().split(" ");
  if (parts.length !== 2 || parts[1] !== symbol) {
    return undefined;
  }
  const value = parts[0];
  const dot = value.indexOf(".");
  let intPart = value;
  let fracPart = "";
  if (dot >= 0) {
    intPart = value.slice(0, dot);
    fracPart = value.slice(dot + 1);
  }
  if (intPart === "" || fracPart.length > 3) {
    return undefined;
  }
  fracPart = fracPart + "0".repeat(3 - fracPart.length);
  const digits = intPart + fracPart;
  if (!/^[0-9]+$/.test(digits)) {
    return undefined;
  }
  return BigInt(digits);
}

/** Scans a block for STEEM and SBD transfer operations whose recipient is
 * the gateway account. steemSymbol qualifies as STEEM; if sbdSymbol is
 * non-empty, transfers in that symbol are also extracted and tagged
 * BRIDGE_ASSET_SBD — leaving sbdSymbol "" disables SBD (the v0.0.3
 * feature-gate). Other symbols and non-transfer ops are ignored. */
export function extractGatewayTransfers(
  blockNum: number,
  block: SteemBlock,
  gateway: string,
  steemSymbol: string,
  sbdSymbol: string,
): Transfer[] {
  const transfers: Transfer[] = [];
  block.transactions.forEach((tx, txNum) => {
    const txid = tx.transaction_id || block.transaction_ids?.[txNum];
    if (!txid) return;

    tx.operations.forEach((rawOp, opIndex) => {
      const parsed = parseTransferOp(rawOp);
      if (!parsed || parsed.to !== gateway) return;

      let amount: bigint | undefined;
      let asset: BridgeAsset;
      const steemAmount = parseSteemAmount(parsed.amount, steemSymbol);
      if (steemAmount !== undefined) {
        amount = steemAmount;
        asset = BridgeAsset.BRIDGE_ASSET_STEEM;
      } else if (sbdSymbol !== "") {
        const sbdAmount = parseSteemAmount(parsed.amount, sbdSymbol);
        if (sbdAmount === undefined) return;
        amount = sbdAmount;
        asset = BridgeAsset.BRIDGE_ASSET_SBD;
      } else {
        return;
      }

      transfers.push({
        txid,
        opIndex,
        steemBlock: blockNum,
        steemTimestamp: block.timestamp,
        from: parsed.from,
        amountMillisteem: amount,
        memo: parsed.memo,
        asset,
      });
    });
  });
  return transfers;
}

/** Scans a block for transfer operations sent FROM the gateway account
 * whose memo is "svm-withdrawal <id>" — the gateway's on-Steem payout of a
 * bridge-out. The amount/symbol is deliberately not checked: the chain
 * already knows the withdrawal's net payout, validators only report where
 * the payout landed. */
export function extractGatewayPayouts(blockNum: number, block: SteemBlock, gateway: string): Payout[] {
  const payouts: Payout[] = [];
  block.transactions.forEach((tx, txNum) => {
    const txid = tx.transaction_id || block.transaction_ids?.[txNum];
    if (!txid) return;

    tx.operations.forEach((rawOp, opIndex) => {
      const parsed = parseTransferOp(rawOp);
      if (!parsed || parsed.from !== gateway) return;
      const id = parseWithdrawalMemo(parsed.memo);
      if (id === undefined) return;
      payouts.push({
        withdrawalId: id,
        txid,
        opIndex,
        steemBlock: blockNum,
        steemTimestamp: block.timestamp,
      });
    });
  });
  return payouts;
}

// parseTransferOp validates and extracts a ["transfer", {...}] operation
// tuple, returning undefined for anything else (malformed shape or a
// different op type).
function parseTransferOp(rawOp: unknown): TransferOp | undefined {
  if (!Array.isArray(rawOp) || rawOp.length !== 2) return undefined;
  const [opType, body] = rawOp;
  if (opType !== "transfer") return undefined;
  if (typeof body !== "object" || body === null) return undefined;
  const { from, to, amount, memo } = body as Record<string, unknown>;
  if (typeof from !== "string" || typeof to !== "string" || typeof amount !== "string") return undefined;
  return { from, to, amount, memo: typeof memo === "string" ? memo : "" };
}
