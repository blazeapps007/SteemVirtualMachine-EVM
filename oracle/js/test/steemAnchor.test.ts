// The chain's Steem anchor (relayer_start_block + relayer_start_block_id in
// genesis): oracles verify their Steem node against it and never scan before
// it. Same cases as oracle/go/relayer/anchor_test.go and
// oracle/python/tests/test_steem_anchor.py.
import { test } from "node:test";
import assert from "node:assert/strict";

import { anchoredCursor, SteemAnchor, SteemAnchorMismatch } from "../src/steemAnchor";

const ANCHOR = 110435764;
const ANCHOR_ID = "06951db4" + "ab".repeat(16);

function fakeSteem(id: string) {
  const s = { calls: 0, blockId: async (num: number) => { assert.equal(num, ANCHOR); s.calls++; return id; } };
  return s;
}

test("anchoredCursor never sits below the anchor", () => {
  assert.equal(anchoredCursor(500, 0), 500);
  assert.equal(anchoredCursor(100, ANCHOR), ANCHOR - 1);
  assert.equal(anchoredCursor(ANCHOR - 1, ANCHOR), ANCHOR - 1);
  assert.equal(anchoredCursor(ANCHOR + 9, ANCHOR), ANCHOR + 9);
});

test("the anchor is verified once per process", async () => {
  const steem = fakeSteem(ANCHOR_ID);
  const a = new SteemAnchor();
  await a.verify(steem, ANCHOR, ANCHOR_ID);
  await a.verify(steem, ANCHOR, ANCHOR_ID);
  assert.equal(steem.calls, 1);
  await new SteemAnchor().verify(steem, 0, "");
  assert.equal(steem.calls, 1, "no recorded ID: nothing to verify");
});

test("a mismatched anchor is refused and never cached", async () => {
  const steem = fakeSteem("06951db4" + "cd".repeat(16));
  const a = new SteemAnchor();
  await assert.rejects(a.verify(steem, ANCHOR, ANCHOR_ID), SteemAnchorMismatch);
  await assert.rejects(a.verify(steem, ANCHOR, ANCHOR_ID), SteemAnchorMismatch);
});
