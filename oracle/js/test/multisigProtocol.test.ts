// Ballot metadata, the shared live ballot vectors, view tallies, the journal,
// memos and history paging.
import { test } from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

import { Ballot, encodeMetadata, InvalidMetadata, MalformedBallot, MAX_BALLOT_BYTES, parseMetadata } from "../src/multisig/ballot";
import { HistoryIndex, newAccount, paidMemoId, parseWithdrawalMemo, withdrawalMemo } from "../src/multisig/chain";
import { ApprovalJournal } from "../src/multisig/journal";
import * as tx from "../src/multisig/steemTx";
import { loadVectors } from "../src/multisig/vectors";
import { buildView } from "../src/multisig/view";
import { BANK, bankAuthority, FakeSteem, Fixture, keyAuthority, MIN, NOW, pub, transferOp, ts } from "./msFixtures";

const PAY = transferOp("rcpt", "1.000 STEEM", "svm-withdrawal 1");
const tmp = () => fs.mkdtempSync(path.join(os.tmpdir(), "svm-ms-"));

test("ballot vectors from mainnet", () => {
  for (const c of loadVectors().ballots) {
    const auth = tx.Authority.fromJSON(c.bank_authority);
    const bank = newAccount(BANK, { owner: auth, active: auth, posting: auth });
    const members = new Map();
    const ballots = new Map();
    for (const m of c.members) {
      members.set(m.name, newAccount(m.name, { active: keyAuthority(m.active_key) }));
      ballots.set(m.name, parseMetadata(m.posting_json_metadata).ballot);
    }
    const v = buildView(tx.parseTime(c.now), undefined, bank, members, ballots, new HistoryIndex());
    assert.equal(v.proposals.length, c.expected.length);
    for (const want of c.expected) {
      const p = v.proposals.find((q) => q.id === want.id)!;
      const valid = [...p.votes].filter(([, vi]) => vi.approve && vi.valid).map(([n]) => n).sort();
      assert.deepEqual([p.creator, p.approve, p.deny, p.status, valid], [want.creator, want.approve, want.deny, want.status, want.valid_approvals]);
    }
  }
});

test("metadata round trip preserves other keys", () => {
  const f = new Fixture();
  const id = f.propose("alice", "Withdrawal 1: 1.000 STEEM to @rcpt", [PAY]);
  f.vote("alice", id, true);
  const profile = '{"profile":{"name":"Alice <3","about":"a & b","z":1,"a":[1, 2]}}';
  const { raw, ballot } = parseMetadata(profile);
  ballot.props.set(id, f.stored(id));
  ballot.votes.set(id, f.ballots.get("alice")!.votes.get(id)!);
  const out = encodeMetadata(raw, ballot);
  assert.ok(out.startsWith('{"profile":{"name":"Alice <3","about":"a & b","z":1,"a":[1, 2]},"svm_ms":{'), out);
  const again = parseMetadata(out);
  assert.equal(again.raw.get("profile"), raw.get("profile"));
  assert.equal(again.ballot.props.get(id)!.tx.id(), id);
  assert.deepEqual(again.ballot.votes.get(id), ballot.votes.get(id));
  assert.equal(encodeMetadata(again.raw, again.ballot), out);
});

test("invalid metadata is never overwritten", () => {
  for (const bad of ["[1,2]", "{bad", '"string"', "42", '{"a":1} x', '{"a":1,}', '{"a":tru}']) {
    assert.throws(() => parseMetadata(bad), InvalidMetadata, bad);
  }
  for (const ok of ["", "  ", "{}", "null"]) {
    const { raw, ballot } = parseMetadata(ok);
    assert.equal(raw.size + ballot.props.size + ballot.votes.size, 0);
  }
  assert.throws(() => parseMetadata('{"profile":{},"svm_ms":"x"}'), MalformedBallot);
  assert.throws(() => parseMetadata('{"svm_ms":{"props":[],"votes":{}}}'), MalformedBallot);
  const { ballot } = parseMetadata('{"svm_ms":{"props":{"x":{"t":"t","c":"c","tx":{"ref_block_num":1,"ref_block_prefix":1,'
    + '"expiration":"2026-01-01T00:00:00","operations":[["vote",{}]],"extensions":[]}}},"votes":{}}}');
  assert.equal(ballot.props.size, 0);
});

test("ballot prune", () => {
  const H = 3600_000;
  const b = new Ballot();
  b.props.set("oldp", { t: "", tx: new tx.Transaction(0, 0, ts(NOW - 73 * H)), c: "" });
  b.props.set("recentp", { t: "", tx: new tx.Transaction(0, 0, ts(NOW - 71 * H)), c: "" });
  b.votes.set("oldv", { a: 0, s: "", x: ts(NOW - 73 * H) });
  b.votes.set("recentv", { a: 0, s: "", x: ts(NOW - 71 * H) });
  b.votes.set("livev", { a: 1, s: "sig", x: ts(NOW + H) });
  b.prune(NOW);
  assert.deepEqual([...b.props.keys()], ["recentp"]);
  assert.deepEqual([...b.votes.keys()].sort(), ["livev", "recentv"]);

  const big = new Ballot();
  const sig = "ab".repeat(65);
  for (let i = 0; i < 400; i++) big.votes.set(String(i).padStart(40, "0"), { a: 1, s: sig, x: ts(NOW - (i + 1) * MIN) });
  for (let i = 0; i < 20; i++) big.votes.set("live" + String(i).padStart(36, "0"), { a: 1, s: sig, x: ts(NOW + H) });
  big.prune(NOW);
  assert.ok(big.encodedSize() <= MAX_BALLOT_BYTES);
  for (let i = 0; i < 20; i++) assert.ok(big.votes.has("live" + String(i).padStart(36, "0")));
  assert.ok(big.votes.has("0".repeat(40)) && !big.votes.has(String(399).padStart(40, "0")));
});

test("view statuses and tallies", () => {
  const f = new Fixture();
  const open = f.propose("alice", "open", [PAY]);
  for (const m of ["alice", "carol", "dave"]) f.vote(m, open, true);
  const ready = f.propose("alice", "ready", [PAY], 57 * MIN);
  f.vote("alice", ready, true);
  f.vote("bob", ready, true);
  const margin = f.propose("bob", "deny at the margin", [PAY], 56 * MIN);
  f.vote("carol", margin, false);
  f.vote("dave", margin, false);
  const rejected = f.propose("bob", "rejected", [PAY], 55 * MIN);
  f.vote("bob", rejected, false);
  const executed = f.propose("carol", "executed", [PAY], 54 * MIN);
  f.hist.executed[executed] = f.props.time;
  let v = f.view();
  assert.deepEqual(Object.fromEntries(v.proposals.map((p) => [p.id, p.status])),
    { [open]: "open", [ready]: "ready", [margin]: "open", [rejected]: "rejected", [executed]: "executed" });
  assert.equal(f.find(v, open).approve, 7000);
  assert.equal(f.find(v, margin).deny, 2500);
  f.nowMs = NOW + 57 * MIN;
  v = f.view();
  assert.deepEqual([f.find(v, open).status, f.find(v, ready).status, f.find(v, executed).status], ["open", "expired", "executed"]);
});

test("view verification", () => {
  const f = new Fixture();
  const good = f.propose("alice", "good", [PAY]);
  f.vote("alice", good, true);
  f.vote("bob", good, true, "dave");
  const forged = f.propose("alice", "forged", [PAY], 50 * MIN);
  f.ballots.get("alice")!.props.set("0".repeat(40), f.stored(forged));
  f.ballots.get("alice")!.props.delete(forged);
  f.propose("mallory", "outsider", [PAY], 49 * MIN);
  f.propose("alice", "other account", [transferOp("rcpt", "1.000 STEEM", "x", "alice")], 48 * MIN);
  const v = f.view();
  assert.deepEqual(v.proposals.map((p) => p.id), [good]);
  const p = v.proposals[0];
  assert.ok(p.votes.get("alice")!.valid && !p.votes.get("bob")!.valid);
  assert.deepEqual([p.approve, p.status], [4500, "open"]);
});

test("minimal signatures", () => {
  const cases: Array<[string[], string[] | undefined]> = [
    [["alice", "bob"], ["alice", "bob"]],
    [["alice", "bob", "carol", "dave"], ["alice", "bob"]],
    [["dave", "carol", "alice"], undefined],
    [["dave", "bob", "alice"], ["alice", "bob"]],
    [["bob", "carol", "dave"], undefined],
  ];
  for (const [approve, want] of cases) {
    const f = new Fixture();
    const id = f.propose("alice", "p", [PAY]);
    for (const m of approve) f.vote(m, id, true);
    const v = f.view();
    const p = f.find(v, id);
    const sigs = v.minimalSignatures(p);
    if (!want) assert.equal(sigs, undefined);
    else assert.deepEqual(sigs!.map((s) => tx.recoverSigner(p.digest, s)), want.map(pub));
  }
});

test("journal", () => {
  const dir = tmp();
  const j = ApprovalJournal.load(dir);
  const f = new Fixture();
  const a = f.propose("alice", "A", [PAY]);
  j.record(a, f.stored(a).tx);
  j.save(dir);
  const j2 = ApprovalJournal.load(dir);
  assert.equal(j2.approvals[a].subject, "transfer:svm-withdrawal 1");
  assert.ok(j2.conflicts("transfer:svm-withdrawal 1", "other", NOW));
  assert.ok(!j2.conflicts("transfer:svm-withdrawal 1", a, NOW));
  assert.ok(!j2.conflicts("transfer:svm-withdrawal 2", "other", NOW));
  assert.ok(!j2.conflicts("transfer:svm-withdrawal 1", "other", NOW + 58 * MIN));
  j2.prune(NOW + 58 * MIN + 72 * 3600_000 + 1000);
  assert.deepEqual(j2.approvals, {});
  fs.writeFileSync(path.join(dir, "svm_bank_approvals.json"), "{corrupt");
  assert.throws(() => ApprovalJournal.load(dir));
  // The file the Go and Python clients write.
  fs.writeFileSync(path.join(dir, "svm_bank_approvals.json"),
    `{"approvals":{"abc":{"subject":"transfer:svm-withdrawal 3","expiration":"${ts(NOW + 5 * MIN)}"}}}`);
  assert.ok(ApprovalJournal.load(dir).conflicts("transfer:svm-withdrawal 3", "x", NOW));
});

test("withdrawal memos", () => {
  for (const [memo, id] of [["svm-withdrawal 0", 0n], ["svm-withdrawal 7", 7n], ["svm-withdrawal 18446744073709551615", 18446744073709551615n]] as const) {
    assert.equal(parseWithdrawalMemo(memo), id);
    assert.equal(withdrawalMemo(id), memo);
  }
  for (const memo of ["svm-withdrawal 07", "svm-withdrawal +7", "svm-withdrawal 7 ", " svm-withdrawal 7", "svm-withdrawal  7", "svm-withdrawal",
    "svm-withdrawal ", "svm-withdraw 7", "SVM-WITHDRAWAL 7", "svm-withdrawal 0x7", "svm-withdrawal 18446744073709551616", "svm-withdrawal -1",
    "svm-withdrawal 7\n", "svm-withdrawal ٧"]) {
    assert.equal(parseWithdrawalMemo(memo), undefined, memo);
  }
  assert.equal(paidMemoId("  svm-withdrawal   9 "), 9n);
  assert.equal(paidMemoId("svm-withdrawal x"), undefined);
});

test("history apply and sync paging", async () => {
  const node = new FakeSteem();
  for (let i = 0; i < 250; i++) {
    node.addHistory(BANK, `trx${String(i).padStart(3, "0")}`, ts(NOW), "transfer", { from: BANK, to: "rcpt", amount: "1.000 STEEM", memo: withdrawalMemo(BigInt(i)) });
  }
  node.addHistory(BANK, "dep", ts(NOW), "transfer", { from: "user", to: BANK, amount: "1.000 STEEM", memo: "svm-withdrawal 999" });
  node.addHistory(BANK, "own", ts(NOW), "account_update", { account: BANK, owner: bankAuthority().toJSON() });
  const h = new HistoryIndex();
  await h.sync(node, BANK, 110000000, NOW);
  assert.equal(h.nextSeq, 252);
  assert.equal(Object.keys(h.paid).length, 250);
  assert.equal(h.paid["249"], "trx249");
  assert.ok(!h.isPaid(999n));
  assert.equal(h.lastOwnerChange, ts(NOW));
  node.addHistory(BANK, "trx250", ts(NOW), "transfer", { from: BANK, to: "rcpt", amount: "1.000 STEEM", memo: "svm-withdrawal 250" });
  node.starts = [];
  await h.sync(node, BANK, 110000001, NOW);
  assert.equal(h.nextSeq as number, 253);
  assert.ok(h.isPaid(250n));
  // Steady state: one request, keyed by the head block (no stale cached window).
  assert.deepEqual(node.starts, [110000001]);
  const empty = new HistoryIndex();
  await empty.sync(node, "nobody", 110000001, NOW);
  assert.equal(empty.nextSeq, 0);
});
