// Node pool, Steem RPC failover and the block-scan history fallback (ports
// oracle/go/nodepool, relayer/steem_failover_test.go and multisig/chain_test.go).
import { test } from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import type { AddressInfo } from "node:net";

import { NodePool, parseNodes } from "../src/nodePool";
import { SteemClient } from "../src/steemClient";
import { BLOCK_SCAN_BATCH, blockLookback, HistoryIndex, PAYOUT_LINK_ID, payoutLinkJson } from "../src/multisig/chain";
import * as tx from "../src/multisig/steemTx";
import { BANK, NOW, ts } from "./msFixtures";

test("parseNodes", () => {
  assert.deepEqual(parseNodes(" https://a/ , https://b", "https://a,,https://c "), ["https://a", "https://b", "https://c"]);
});

test("round-robin spreads calls", () => {
  const p = new NodePool("roundRobin", "a,b,c");
  assert.deepEqual(Array.from({ length: 6 }, () => p.order()[0]), ["a", "b", "c", "a", "b", "c"]);
});

test("sticky prefers the first healthy node; cooldown grows and resets", () => {
  let now = 1_000_000;
  const p = new NodePool("sticky", "own,b,c", () => now);
  assert.equal(p.order()[0], "own");
  assert.equal(p.fail("own"), 30_000);
  assert.deepEqual(p.order(), ["b", "c", "own"]);
  now += 31_000;
  assert.equal(p.order()[0], "own");
  assert.deepEqual(Array.from({ length: 6 }, () => p.fail("own")), [60_000, 120_000, 240_000, 480_000, 600_000, 600_000]);
  p.ok("own");
  assert.equal(p.fail("own"), 30_000);
});

test("all benched: soonest to recover first", () => {
  const p = new NodePool("roundRobin", "a,b", () => 1000);
  p.fail("a");
  p.fail("a");
  p.fail("b");
  assert.deepEqual(p.order(), ["b", "a"]);
});

// ---------------------------------------------------------------------------
// fake nodes
// ---------------------------------------------------------------------------

type Handler = (req: { method: string; params: any[] }) => { status?: number; body: string };

async function serve(t: { after: (fn: () => void) => void }, handler: Handler): Promise<{ url: string; calls: () => number }> {
  let calls = 0;
  const srv = http.createServer((req, res) => {
    let data = "";
    req.on("data", (c) => (data += c));
    req.on("end", () => {
      calls++;
      const out = handler(JSON.parse(data));
      res.writeHead(out.status ?? 200, { "content-type": "application/json" });
      res.end(out.body);
    });
  });
  await new Promise<void>((r) => srv.listen(0, "127.0.0.1", r));
  t.after(() => srv.close());
  return { url: `http://127.0.0.1:${(srv.address() as AddressInfo).port}`, calls: () => calls };
}

const ok = (result: unknown) => ({ body: JSON.stringify({ jsonrpc: "2.0", id: 1, result }) });
const html: Handler = () => ({ status: 429, body: "<html><body>429 Too Many Requests</body></html>" });
const rpcError = (message: string): Handler => () => ({ body: JSON.stringify({ jsonrpc: "2.0", id: 1, error: { code: -32603, message } }) });

function blockServer(head: number, txs: Record<number, any[][]> = {}): Handler {
  return (req) => {
    if (req.method !== "condenser_api.get_block") return rpcError("Internal Error")(req);
    const num = Number(req.params[0]);
    if (num > head) return ok(null);
    const blockTxs = txs[num] ?? [];
    return ok({
      timestamp: ts(NOW),
      transaction_ids: blockTxs.map((_, i) => num.toString(16).padStart(8, "0") + String(i).padStart(32, "0")),
      transactions: blockTxs.map((operations) => ({ operations })),
    });
  };
}

test("fetchBlocks spreads calls over nodes", async (t) => {
  const [a, b, c] = await Promise.all([serve(t, blockServer(1000)), serve(t, blockServer(1000)), serve(t, blockServer(1000))]);
  const client = new SteemClient([a.url, b.url, c.url]);
  assert.equal((await client.fetchBlocks(1, 90)).length, 90);
  assert.deepEqual([a.calls(), b.calls(), c.calls()], [30, 30, 30]);
});

test("fetchBlocks skips blocked and lagging nodes", async (t) => {
  const blocked = await serve(t, html);
  const lagging = await serve(t, blockServer(10));
  const good = await serve(t, blockServer(1000));
  const client = new SteemClient(`${blocked.url},${lagging.url},${good.url}`);
  assert.equal((await client.fetchBlocks(1, 50)).length, 50);
  assert.equal(blocked.calls(), 1);
  assert.ok(lagging.calls() <= 12);
});

test("fetchBlocks fails when no node has the block", async (t) => {
  const [a, b] = await Promise.all([serve(t, blockServer(5)), serve(t, blockServer(5))]);
  await assert.rejects(new SteemClient([a.url, b.url]).fetchBlocks(5, 6), /all 2 Steem nodes failed.*no data for irreversible block 6/);
});

test("single node: HTML error message", async (t) => {
  const a = await serve(t, html);
  await assert.rejects(new SteemClient(a.url).lastIrreversibleBlock(), /HTML page instead of JSON \(HTTP 429\)/);
});

// ---------------------------------------------------------------------------
// svm.bank signer RPC + history
// ---------------------------------------------------------------------------

const link = (wid: bigint, auth = BANK) =>
  ({ required_auths: [auth], required_posting_auths: [], id: PAYOUT_LINK_ID, json: payoutLinkJson(wid) });

function historyServer(entries: any[]): Handler {
  return (req) => {
    const [, start, limit] = req.params as [string, number, number];
    const top = Math.min(start, entries.length - 1);
    const page = [];
    for (let seq = Math.max(0, top - limit); seq <= top; seq++) page.push([seq, entries[seq]]);
    return ok(page);
  };
}

for (const [name, bad] of [
  ["html", html],
  ["internal error", rpcError("Internal Error")],
  ["bad cast", rpcError("Bad Cast:Invalid cast from object_type to Array")],
  ["empty", () => ok([])],
] as Array<[string, Handler]>) {
  test(`history sync fails over a bad node: ${name}`, async (t) => {
    const entries = [
      { trx_id: "0".repeat(39) + "1", timestamp: "2026-01-01T00:00:00", op: ["account_create", { new_account_name: BANK }] },
      { trx_id: "trx000", timestamp: ts(NOW), op: ["custom_json", link(7n)] },
    ];
    const badNode = await serve(t, bad);
    const good = await serve(t, historyServer(entries));
    const rpc = new tx.RPC([badNode.url, good.url]);
    const h = new HistoryIndex();
    await h.sync(rpc, BANK, 110000000, NOW);
    assert.ok(h.isPaid(7n));
    await h.sync(rpc, BANK, 110000001, NOW);
    assert.equal(badNode.calls(), 1, "a benched node is not asked again");
  });
}

test("a chain rejection is not retried on another node", async (t) => {
  const a = await serve(t, rpcError("missing required active authority"));
  const b = await serve(t, rpcError("x"));
  await assert.rejects(new tx.RPC([a.url, b.url]).call("condenser_api.broadcast_transaction_synchronous", [{}]), tx.ChainError);
  assert.equal(b.calls(), 0);
});

test("scanBlocks rebuilds history from blocks", async (t) => {
  const payout = [["transfer", { from: BANK, to: "rcpt", amount: "1.000 STEEM", memo: "" }], ["custom_json", link(42n)]];
  const txs: Record<number, any[][]> = {
    950: [payout],
    960: [[["account_update", { account: BANK, owner: { weight_threshold: 1 } }]]],
    970: [[["transfer", { from: "alice", to: "bob", amount: "1.000 STEEM", memo: "svm.bank" }]]],
    980: [[["custom_json", link(43n, "mallory")]]], // not signed by svm.bank: not a payout
  };
  const [a, b] = await Promise.all([serve(t, blockServer(1000, txs)), serve(t, blockServer(1000, txs))]);
  const rpc = new tx.RPC([a.url, b.url], undefined, true);
  const h = new HistoryIndex();
  assert.equal(await h.scanBlocks(rpc, BANK, 1000, 300, NOW), false);
  assert.equal(h.scannedBlock, 700 + BLOCK_SCAN_BATCH - 1);
  assert.equal(await h.scanBlocks(rpc, BANK, 1000, 300, NOW), true);
  assert.equal(h.scannedBlock, 1000);
  const paidTrx = (950).toString(16).padStart(8, "0") + "0".repeat(32);
  assert.deepEqual(h.paid, { "42": paidTrx });
  assert.ok(h.executed[paidTrx]);
  assert.equal(h.lastOwnerChange, ts(NOW));
  assert.ok(Math.min(a.calls(), b.calls()) >= 100, "round-robin spreads the blocks");
  // A block no node has yet stops the scan there; progress is kept.
  await assert.rejects(h.scanBlocks(rpc, BANK, 1002, 300, NOW), /no block 1001/);
  assert.equal(h.scannedBlock, 1000);
});

test("blockLookback", () => {
  assert.equal(blockLookback(50400), Math.floor((50400 * 10 + 3600) / 3));
  assert.equal(blockLookback(1), Math.floor((96 * 3600 + 3600) / 3));
});
