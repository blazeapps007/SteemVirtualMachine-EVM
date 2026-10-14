// svm.ts — the SVM reads the signer checks every decision against, over the
// node's REST (gRPC-gateway) API, this client's transport (oracle/PROTOCOL.md
// §4). Mirrors oracle/go/multisig/svmquery.go; historical reads at a
// snapshot height use the x-cosmos-block-height header.

import { fromBech32 } from "@cosmjs/encoding";

import type { StakeEntry } from "./apportion";
import type { SVM, Withdrawal } from "./policy";

const HEIGHT_HEADER = "x-cosmos-block-height";

class KeyNotFound extends Error {}

function bytesOf(bech32: string): string {
  return Buffer.from(fromBech32(bech32).data).toString("hex");
}

function toWithdrawal(w: any): Withdrawal {
  return {
    id: BigInt(w.id ?? 0),
    destination: String(w.destination_steem_account ?? ""),
    amountMilli: BigInt(w.amount_millisteem ?? 0),
    symbol: w.asset === "BRIDGE_ASSET_SBD" || w.asset === 1 ? "SBD" : "STEEM",
    createdAt: BigInt(w.created_at ?? 0),
    status: String(w.status || "WITHDRAWAL_STATUS_REQUESTED").replace(/^WITHDRAWAL_STATUS_/, ""),
  };
}

export class RestSVM implements SVM {
  private readonly base: string;
  private readonly operatorHex: string;

  constructor(restUrl: string, private readonly valoperAddress: string) {
    this.base = restUrl.replace(/\/+$/, "");
    this.operatorHex = bytesOf(valoperAddress);
  }

  private async get(path: string, params: Record<string, string> = {}, height?: bigint): Promise<any> {
    const qs = new URLSearchParams(params).toString();
    const resp = await fetch(`${this.base}${path}${qs ? "?" + qs : ""}`, {
      headers: height !== undefined && height > 0n ? { [HEIGHT_HEADER]: height.toString() } : {},
      signal: AbortSignal.timeout(20_000),
    });
    if (resp.ok) return resp.json();
    const body: any = await resp.json().catch(() => ({}));
    const message = String(body?.message ?? "");
    // Only the bridge queries' "key not found" means absent: a pruned height or
    // any other failure must stay an error.
    if (message.toLowerCase().includes("key not found")) throw new KeyNotFound(message);
    throw new Error(`GET ${path}: HTTP ${resp.status} ${message}`);
  }

  private async paginate(path: string, field: string, extra: Record<string, string> = {}, height?: bigint): Promise<any[]> {
    const out: any[] = [];
    let key = "";
    for (;;) {
      const params: Record<string, string> = { "pagination.limit": "200", ...extra };
      if (key) params["pagination.key"] = key;
      const resp = await this.get(path, params, height);
      out.push(...(resp[field] ?? []));
      key = resp.pagination?.next_key ?? "";
      if (!key) return out;
    }
  }

  async height(): Promise<bigint> {
    const resp = await this.get("/cosmos/base/tendermint/v1beta1/blocks/latest");
    return BigInt(resp.block.header.height);
  }

  async withdrawalTimeoutBlocks(): Promise<bigint> {
    return BigInt((await this.get("/steemvm/steembridge/v1/params")).params.withdrawal_timeout_blocks);
  }

  async requestedWithdrawals(): Promise<Withdrawal[]> {
    return (await this.paginate("/steemvm/steembridge/v1/requested_withdrawals", "withdrawals")).map(toWithdrawal);
  }

  async withdrawal(id: bigint): Promise<Withdrawal | undefined> {
    try {
      return toWithdrawal((await this.get(`/steemvm/steembridge/v1/withdrawal/${id}`)).withdrawal);
    } catch (err) {
      if (err instanceof KeyNotFound) return undefined;
      throw err;
    }
  }

  private async ownerOfName(name: string, height?: bigint): Promise<string | undefined> {
    let resp: any;
    try {
      resp = await this.get(`/steemvm/steembridge/v1/name/${encodeURIComponent(name)}`, {}, height);
    } catch (err) {
      if (err instanceof KeyNotFound) return undefined;
      throw err;
    }
    try {
      return bytesOf(resp.name.address);
    } catch {
      return undefined;
    }
  }

  /** Bonded, unjailed validators whose moniker is an ACTIVE Steem name link
   * owned by the operator, at the given SVM height. */
  async validatorStakes(height: bigint): Promise<StakeEntry[]> {
    const out: StakeEntry[] = [];
    const vals = await this.paginate("/cosmos/staking/v1beta1/validators", "validators", { status: "BOND_STATUS_BONDED" }, height);
    for (const val of vals) {
      const tokens = BigInt(val.tokens || "0");
      if (val.jailed || tokens <= 0n) continue;
      const name = String(val.description?.moniker ?? "").trim();
      if (!name) continue;
      let op: string;
      try {
        op = bytesOf(val.operator_address);
      } catch {
        continue;
      }
      if ((await this.ownerOfName(name, height)) !== op) continue; // moniker is not an ACTIVE name link owned by this operator
      out.push({ account: name, tokens });
    }
    return out;
  }

  async selfSteemAccount(): Promise<string> {
    let resp: any;
    try {
      resp = await this.get(`/cosmos/staking/v1beta1/validators/${this.valoperAddress}`);
    } catch (err) {
      throw new Error(`this oracle's key is not a validator operator: ${err instanceof Error ? err.message : String(err)}`);
    }
    const name = String(resp.validator?.description?.moniker ?? "").trim();
    if ((await this.ownerOfName(name)) !== this.operatorHex) {
      throw new Error(`validator moniker ${name} is not an ACTIVE Steem name link owned by this operator`);
    }
    return name;
  }
}
