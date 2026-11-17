// Spreads the oracle's Steem RPC calls over several nodes and benches a node
// that misbehaves, so one validator IP never hammers a single public node into
// rate-limiting or blacklisting it, and one broken node never stops the
// oracle. Ports oracle/go/nodepool.
//
// A node is benched after any failed call (transport error, an HTML/non-JSON
// reply, a JSON-RPC error such as "Internal Error" or "Bad Cast", or an answer
// that fails the caller's check) for a cooldown that doubles with each
// consecutive failure (30s, 1m, 2m … capped at 10m) and resets on success.
// Benched nodes are still tried last, so a call goes out even when every node
// is benched.

/** "roundRobin": each call starts at the next node (spreads load).
 * "sticky": each call starts at the first healthy node (consistent view). */
export type PoolMode = "roundRobin" | "sticky";

const BASE_COOLDOWN_MS = 30_000;
const MAX_COOLDOWN_MS = 600_000;

/** Splits each argument on commas, trims, drops a trailing '/', and drops
 * empty and repeated entries, keeping first-seen order. */
export function parseNodes(...lists: Array<string | string[] | undefined | null>): string[] {
  const out: string[] = [];
  for (const item of lists) {
    if (!item) continue;
    const parts = typeof item === "string" ? item.split(",") : item.flatMap((x) => String(x).split(","));
    for (let n of parts) {
      n = n.trim().replace(/\/+$/, "");
      if (n && !out.includes(n)) out.push(n);
    }
  }
  return out;
}

export class NodePool {
  readonly nodes: string[];
  private next = 0;
  private readonly fails = new Map<string, number>();
  private readonly until = new Map<string, number>();

  constructor(
    readonly mode: PoolMode,
    nodes: string | string[],
    private readonly now: () => number = Date.now,
  ) {
    this.nodes = parseNodes(nodes);
  }

  /** The nodes to try for one call: healthy nodes first (from the round-robin
   * cursor, or from the first node when sticky), then benched nodes,
   * soonest-to-recover first. */
  order(): string[] {
    const n = this.nodes.length;
    if (n === 0) return [];
    let start = 0;
    if (this.mode === "roundRobin") {
      start = this.next % n;
      this.next = (this.next + 1) % n;
    }
    const now = this.now();
    const rotated = Array.from({ length: n }, (_, i) => this.nodes[(start + i) % n]);
    const benchedUntil = (x: string) => this.until.get(x) ?? 0;
    const healthy = rotated.filter((x) => benchedUntil(x) <= now);
    const benched = rotated.filter((x) => benchedUntil(x) > now).sort((a, b) => benchedUntil(a) - benchedUntil(b));
    return [...healthy, ...benched];
  }

  /** Benches node; returns the cooldown in ms. */
  fail(node: string): number {
    const f = (this.fails.get(node) ?? 0) + 1;
    this.fails.set(node, f);
    const d = Math.min(BASE_COOLDOWN_MS * 2 ** Math.min(f - 1, 10), MAX_COOLDOWN_MS);
    this.until.set(node, this.now() + d);
    return d;
  }

  ok(node: string): void {
    this.fails.delete(node);
    this.until.delete(node);
  }
}
