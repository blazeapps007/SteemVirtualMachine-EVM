// The signer's policy: payouts, duplicate proposals, validator-set syncs and
// intents (mirrors oracle/go/multisig/policy_test.go).
import { test } from "node:test";
import assert from "node:assert/strict";

import { ApprovalJournal } from "../src/multisig/journal";
import * as tx from "../src/multisig/steemTx";
import { BANK, bankAuthority, Fixture, MIN, NOW, pub, stakes, transferOp, ts, withdrawal } from "./msFixtures";

const EXACT = transferOp("rcpt", "12.345 STEEM", "svm-withdrawal 7");
const EARLIER = ts(NOW - MIN);

// Withdrawal 7: refund at 90000 + 50400 = 140400, so payable up to 126000.
function payoutFixture(): Fixture {
  const f = new Fixture();
  f.withdrawal(7, "rcpt", 12345, "STEEM", 90000);
  return f;
}

const setStatus = (f: Fixture, status: string) => f.svm.all.set(7n, { ...f.svm.all.get(7n)!, status });

const payoutCases: Array<[string, tx.Operation[], ((f: Fixture) => void) | undefined, string]> = [
  ["exact payout", [EXACT], undefined, "approve"],
  ["last allowed height", [EXACT], (f) => { f.svm.height_ = 126000n; }, "approve"],
  ["one block late", [EXACT], (f) => { f.svm.height_ = 126001n; }, "deny"],
  ["leading zero memo", [transferOp("rcpt", "12.345 STEEM", "svm-withdrawal 07")], undefined, "deny"],
  ["guide's old memo", [transferOp("rcpt", "12.345 STEEM", "svm-withdraw 7")], undefined, "deny"],
  ["memo trailing space", [transferOp("rcpt", "12.345 STEEM", "svm-withdrawal 7 ")], undefined, "deny"],
  ["wrong recipient", [transferOp("rcpt2", "12.345 STEEM", "svm-withdrawal 7")], undefined, "deny"],
  ["one millisteem more", [transferOp("rcpt", "12.346 STEEM", "svm-withdrawal 7")], undefined, "deny"],
  ["one millisteem less", [transferOp("rcpt", "12.344 STEEM", "svm-withdrawal 7")], undefined, "deny"],
  ["wrong asset", [transferOp("rcpt", "12.345 SBD", "svm-withdrawal 7")], undefined, "deny"],
  ["two operations", [EXACT, transferOp("rcpt", "0.001 STEEM", "x")], undefined, "deny"],
  ["already paid", [EXACT], (f) => { f.hist.paid["7"] = "abc"; }, "deny"],
  ["processed", [EXACT], (f) => setStatus(f, "PROCESSED"), "deny"],
  ["refunded", [EXACT], (f) => setStatus(f, "REFUNDED"), "deny"],
  ["not seen yet", [EXACT], (f) => { f.svm.all.delete(7n); }, "abstain"],
  ["read fails", [EXACT], (f) => { setStatus(f, "PROCESSED"); f.svm.err = new Error("refused"); }, "abstain"],
  ["treasury op", [{ kind: "transfer_to_vesting", from: BANK, to: BANK, amount: "1.000 STEEM" }], undefined, "abstain"],
];

for (const [name, ops, setup, want] of payoutCases) {
  test(`review payout: ${name}`, async () => {
    const f = payoutFixture();
    setup?.(f);
    const id = f.propose("alice", "payout", ops);
    const [d, reason] = await f.review(f.policy("bob"), id);
    assert.equal(d, want, reason);
  });
}

test("payout cap", async () => {
  const f = payoutFixture();
  const id = f.propose("alice", "payout", [EXACT]);
  const pol = f.policy("bob");
  pol.cfg.maxPerTxMilli = { STEEM: 12344n };
  assert.equal((await f.review(pol, id))[0], "abstain");
  pol.cfg.maxPerTxMilli = { STEEM: 12345n };
  assert.equal((await f.review(pol, id))[0], "approve");
});

test("only the earliest correct payout", async () => {
  const f = payoutFixture();
  const a = f.propose("alice", "A", [EXACT], 58 * MIN, EARLIER);
  const b = f.propose("bob", "B", [EXACT], 57 * MIN);
  const pol = f.policy("carol");
  assert.equal((await f.review(pol, a))[0], "approve");
  assert.equal((await f.review(pol, b))[0], "abstain");
});

test("an earlier wrong payout does not block", async () => {
  const f = payoutFixture();
  f.propose("alice", "wrong", [transferOp("rcpt", "99.999 STEEM", "svm-withdrawal 7")], 58 * MIN, EARLIER);
  const b = f.propose("bob", "B", [EXACT], 57 * MIN);
  assert.equal((await f.review(f.policy("carol"), b))[0], "approve");
});

test("my approval of another blocks", async () => {
  const f = payoutFixture();
  const a = f.propose("alice", "A", [EXACT], 58 * MIN, EARLIER);
  const b = f.propose("bob", "B", [EXACT], 57 * MIN);
  assert.equal((await f.review(f.policy("carol"), a, new Map([[b, { a: 1, s: "", x: "" }]])))[0], "abstain");
});

test("an approval of a rejected unexpired proposal still blocks", async () => {
  const f = payoutFixture();
  const a = f.propose("alice", "A", [EXACT], 58 * MIN, EARLIER);
  f.vote("bob", a, false);
  const b = f.propose("bob", "B", [EXACT], 57 * MIN);
  assert.equal(f.find(f.view(), a).status, "rejected");
  assert.equal((await f.review(f.policy("carol"), b, new Map([[a, { a: 1, s: "", x: "" }]])))[0], "abstain");
});

test("an expired approval no longer blocks", async () => {
  const f = payoutFixture();
  const a = f.propose("alice", "A", [EXACT], MIN, EARLIER);
  const b = f.propose("bob", "B", [EXACT], 57 * MIN);
  f.nowMs = NOW + MIN;
  assert.equal(f.find(f.view(), a).status, "expired");
  assert.equal((await f.review(f.policy("carol"), b, new Map([[a, { a: 1, s: "", x: "" }]])))[0], "approve");
});

test("the journal blocks even when the view lags", async () => {
  const f = payoutFixture();
  const b = f.propose("bob", "B", [EXACT], 57 * MIN);
  const pol = f.policy("carol");
  pol.journal = new ApprovalJournal({ unseen: { subject: "transfer:svm-withdrawal 7", expiration: ts(NOW + MIN) } });
  assert.equal((await f.review(pol, b))[0], "abstain");
  pol.journal.approvals.unseen.expiration = ts(NOW);
  assert.equal((await f.review(pol, b))[0], "approve");
  pol.journal.record(b, f.stored(b).tx);
  assert.equal((await f.review(pol, b))[0], "approve");
});

function reweightFixture(): [Fixture, tx.Authority] {
  const f = new Fixture();
  f.svm.stakesAt.set(99000n, stakes({ alice: 50, bob: 30, carol: 15, dave: 5 }));
  return [f, bankAuthority([["alice", 5000], ["bob", 3000], ["carol", 1500], ["dave", 500]])];
}

const updateOp = (f: Fixture, owner?: tx.Authority, active?: tx.Authority, posting?: tx.Authority, memoKey?: string): tx.Operation =>
  ({ kind: "account_update", account: BANK, owner, active, posting, memoKey: memoKey ?? f.bank.memoKey, jsonMetadata: "" });

const setCases: Array<[string, string, string, boolean, string]> = [
  ["exact set", "Validator set @svm:99000", "want", false, "approve"],
  ["one point off", "Validator set @svm:99000", "off", false, "deny"],
  ["owner only", "Validator set @svm:99000", "ownerOnly", false, "deny"],
  ["memo key changed", "Validator set @svm:99000", "memo", false, "deny"],
  ["no snapshot", "Validator set", "want", false, "deny"],
  ["not a checkpoint", "Validator set @svm:99500", "want", false, "deny"],
  ["too old", "Validator set @svm:96000", "want", false, "deny"],
  ["ahead of this node", "Validator set @svm:101000", "want", false, "abstain"],
  ["height pruned", "Validator set @svm:98000", "want", false, "abstain"],
  ["non-ascii digits", "Validator set @svm:٩٩٠٠٠", "want", false, "deny"],
  ["huge snapshot", "Validator set @svm:99999999999999999999000", "want", false, "deny"],
  ["already current", "Validator set @svm:99000", "want", true, "deny"],
];

for (const [name, title, which, current, want] of setCases) {
  test(`review validator set: ${name}`, async () => {
    const [f, desired] = reweightFixture();
    const off = bankAuthority([["alice", 5001], ["bob", 2999], ["carol", 1500], ["dave", 500]]);
    if (current) f.bank.owner = f.bank.active = f.bank.posting = desired;
    const op = {
      want: updateOp(f, desired, desired, desired), off: updateOp(f, off, off, off), ownerOnly: updateOp(f, desired),
      memo: updateOp(f, desired, desired, desired, pub("attacker")),
    }[which]!;
    const id = f.propose("alice", title, [op]);
    const [d, reason] = await f.review(f.policy("bob"), id);
    assert.equal(d, want, reason);
  });
}

test("desired authority is exact", async () => {
  const [f, desired] = reweightFixture();
  assert.ok((await f.policy("bob").desiredAt(99000n)).equal(desired));
});

test("payout proposer rotation", async () => {
  // Withdrawal 6: 6 mod 4 = 2, so carol is designated; dave after 120s, alice after 240s.
  const f = new Fixture();
  f.withdrawal(6, "rcpt", 1000, "STEEM", 90000);
  const t0 = Date.now();
  for (const [me, after, want] of [["carol", 0, 1], ["dave", 0, 0], ["dave", 119, 0], ["dave", 120, 1], ["alice", 120, 0], ["alice", 240, 1], ["mallory", 3600, 0]] as const) {
    const [got, err] = await f.policy(me).intents(f.view(), await f.state(), new Map([["w:6", t0]]), t0 + after * 1000);
    assert.equal(err, undefined);
    assert.equal(got.length, want, `${me} after ${after}s`);
  }
  const seen = new Map<string, number>();
  const [got] = await f.policy("carol").intents(f.view(), await f.state(), seen, t0);
  assert.deepEqual(got[0].ops, [{ kind: "transfer", from: BANK, to: "rcpt", amount: "1.000 STEEM", memo: "svm-withdrawal 6" }]);
  const id = f.propose("carol", got[0].title, got[0].ops);
  assert.equal((await f.review(f.policy("alice"), id))[0], "approve");
  seen.set("w:6", t0 - 3600_000);
  const [again] = await f.policy("dave").intents(f.view(), await f.state(), seen, t0);
  assert.equal(again.length, 0);
  assert.ok(!seen.has("w:6"));
});

for (const name of ["balance", "margin", "paid", "cap", "journal"]) {
  test(`payout intent skips: ${name}`, async () => {
    const f = new Fixture();
    f.withdrawal(6, "rcpt", 1000, "STEEM", 90000);
    f.svm.stakesAt.set(125000n, f.svm.stakesAt.get(99000n)!);
    const pol = f.policy("carol");
    if (name === "balance") f.bank.balance = "0.999 STEEM";
    else if (name === "margin") f.svm.height_ = 126001n;
    else if (name === "paid") f.hist.paid["6"] = "abc";
    else if (name === "cap") pol.cfg.maxPerTxMilli = { STEEM: 999n };
    else pol.journal = new ApprovalJournal({ x: { subject: "transfer:svm-withdrawal 6", expiration: ts(NOW + MIN) } });
    const [got, err] = await pol.intents(f.view(), await f.state(), new Map(), Date.now());
    assert.equal(err, undefined);
    assert.equal(got.length, 0);
  });
}

test("validator set intent", async () => {
  // Checkpoint(100000) = 99000; 99 mod 4 = 3, so dave is designated.
  let [f, desired] = reweightFixture();
  const now = Date.now();
  const [got, err] = await f.policy("dave").intents(f.view(), await f.state(), new Map(), now);
  assert.equal(err, undefined);
  assert.deepEqual(got.map((i) => i.key), ["v:99000"]);
  assert.equal(got[0].title, "Validator set @svm:99000");
  const u = got[0].ops[0] as tx.AccountUpdate;
  assert.ok(u.owner!.equal(desired) && u.active!.equal(desired) && u.posting!.equal(desired));
  assert.equal(u.memoKey, f.bank.memoKey);
  const id = f.propose("dave", got[0].title, got[0].ops);
  assert.equal((await f.review(f.policy("alice"), id))[0], "approve");

  [f] = reweightFixture();
  f.hist.lastOwnerChange = ts(NOW - 59 * MIN);
  assert.equal((await f.policy("dave").intents(f.view(), await f.state(), new Map(), now))[0].length, 0);
  f.hist.lastOwnerChange = ts(NOW - 61 * MIN);
  assert.equal((await f.policy("dave").intents(f.view(), await f.state(), new Map(), now))[0].length, 1);

  f = new Fixture();
  assert.equal((await f.policy("dave").intents(f.view(), await f.state(), new Map(), now))[0].length, 0);

  // A stake read failure keeps the payouts and reports the error.
  f = new Fixture();
  f.svm.all.set(5n, withdrawal(5, "rcpt", 1000, "STEEM", 90000)); // 5 mod 4 = 1: bob
  f.svm.stakesAt.delete(99000n);
  const [kept, e2] = await f.policy("bob").intents(f.view(), await f.state(), new Map(), now);
  assert.deepEqual(kept.map((i) => i.key), ["w:5"]);
  assert.ok(e2);
});
