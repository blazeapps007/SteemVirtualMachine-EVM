// Withdrawal payouts as the bridge relay reads them: linked by a custom_json,
// never by the memo (mirrors oracle/go/relayer/relayer_test.go).
import assert from "node:assert/strict";
import { test } from "node:test";

import { BridgeAsset } from "../src/proto/steemvm/steembridge/v1/asset";
import { comparePayout } from "../src/relayer";
import { extractGatewayPayouts, extractGatewayTransfers, parsePayoutLink, PAYOUT_LINK_ID, type Payout } from "../src/steemClient";

const linkBody = { required_auths: ["gw"], required_posting_auths: [] as string[], id: PAYOUT_LINK_ID, json: '{"withdrawal_id":"7"}' };
const LINK = ["custom_json", linkBody];
const transfer = (from: string, to: string, amount = "1.000 STEEM", memo = "") => ["transfer", { from, to, amount, memo }];

test("payout link parsing", () => {
  assert.equal(parsePayoutLink(LINK, "gw"), 7n);
  for (const json of ['{"withdrawal_id":"07"}', '{"withdrawal_id":7}', '{"withdrawal_id":"+7"}', '{ "withdrawal_id":"7"}',
    '{"withdrawal_id":"7","x":1}', '{"withdrawal_id":"18446744073709551616"}', "svm-withdrawal 7"]) {
    assert.equal(parsePayoutLink(["custom_json", { ...linkBody, json }], "gw"), undefined, json);
  }
  assert.equal(parsePayoutLink(["custom_json", { ...linkBody, id: "other" }], "gw"), undefined);
  assert.equal(parsePayoutLink(["custom_json", { ...linkBody, required_auths: ["alice"] }], "gw"), undefined);
  assert.equal(parsePayoutLink(["custom_json", { ...linkBody, required_auths: [], required_posting_auths: ["gw"] }], "gw"), undefined);
  assert.equal(parsePayoutLink(["custom_json", { ...linkBody, required_auths: ["gw", "alice"] }], "gw"), undefined);
  assert.equal(parsePayoutLink(["custom_json", { ...linkBody, required_posting_auths: ["alice"] }], "gw"), undefined);
  assert.equal(parsePayoutLink(transfer("gw", "a", "1.000 STEEM", "svm-withdrawal 7"), "gw"), undefined);
});

test("only a gateway transfer followed by its link, alone in the tx, is a payout", () => {
  const txid = "b".repeat(40);
  const block: any = {
    timestamp: "2026-07-14T14:00:00",
    transactions: [
      { transaction_id: txid, operations: [transfer("gw", "alice", "10.000 STEEM", "exchange-tag-42"), LINK] },
      { transaction_id: "c", operations: [transfer("gw", "bob", "1.000 STEEM", "svm-withdrawal 8")] },
      { transaction_id: "d", operations: [transfer("someone", "carol"), LINK] },
      { transaction_id: "e", operations: [LINK, transfer("gw", "dan")] },
      { transaction_id: "f", operations: [transfer("gw", "erin"), LINK, ["vote", { voter: "a" }]] },
      { transaction_id: "g", operations: [transfer("gw", "alice", "1.000 VESTS"), LINK] },
    ],
  };
  assert.deepEqual(extractGatewayPayouts(500, block, "gw", "STEEM", "SBD"), [{
    withdrawalId: 7n, txid, opIndex: 0, steemBlock: 500, steemTimestamp: "2026-07-14T14:00:00",
    destination: "alice", memo: "exchange-tag-42", amountMillisteem: 10000n, asset: BridgeAsset.BRIDGE_ASSET_STEEM,
  }]);
});

test("a gateway transfer to itself is not a deposit", () => {
  const block: any = { timestamp: "t", transactions: [{ transaction_id: "a", operations: [
    transfer("gw", "gw", "1.000 STEEM", "svm-deposit 0x1111111111111111111111111111111111111111"),
  ] }] };
  assert.deepEqual(extractGatewayTransfers(1, block, "gw", "STEEM", ""), []);
});

test("comparing a payout with its withdrawal", () => {
  const w = { destination_steem_account: "alice", memo: "tag", amount_millisteem: "1000", asset: "BRIDGE_ASSET_STEEM" };
  const good: Payout = {
    withdrawalId: 7n, txid: "t", opIndex: 0, steemBlock: 1, steemTimestamp: "t", destination: "alice", memo: "tag",
    amountMillisteem: 1000n, asset: BridgeAsset.BRIDGE_ASSET_STEEM,
  };
  assert.equal(comparePayout(good, w), undefined);
  for (const [name, change] of Object.entries({
    recipient: { destination: "mallory" }, memo: { memo: "" }, amount: { amountMillisteem: 999n }, asset: { asset: BridgeAsset.BRIDGE_ASSET_SBD },
  })) {
    assert.ok(comparePayout({ ...good, ...change } as Payout, w)!.includes(name), name);
  }
});
