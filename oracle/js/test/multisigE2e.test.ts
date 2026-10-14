// Four independent signers against a fake Steem node that enforces the chain
// rules (mirrors oracle/go/multisig/e2e_test.go).
import { test } from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

import { parseMetadata } from "../src/multisig/ballot";
import { newAccount } from "../src/multisig/chain";
import { buildSignerConfig, defaultSignerConfig, Signer, SignerConfigError } from "../src/multisig/signer";
import { BANK, bankAuthority, FakeSteem, FakeSVM, keyAuthority, pub, quietLog, stakes, WEIGHTS, wif, withdrawal } from "./msFixtures";

class Network {
  node = new FakeSteem();
  svm = new FakeSVM();
  dir = fs.mkdtempSync(path.join(os.tmpdir(), "svm-ms-e2e-"));
  signers: Signer[] = [];

  constructor() {
    const auth = bankAuthority();
    this.node.accounts.set(BANK, newAccount(BANK, { owner: auth, active: auth.sorted(), posting: auth.sorted(), memoKey: pub("memo"), balance: "1000.000 STEEM", sbdBalance: "50.000 SBD" }));
    this.node.accounts.set("rcpt", newAccount("rcpt", { balance: "0.000 STEEM", sbdBalance: "0.000 SBD" }));
    for (const [n] of WEIGHTS) {
      this.node.accounts.set(n, newAccount(n, {
        owner: keyAuthority(pub(n + "/owner")), active: keyAuthority(pub(n)), posting: keyAuthority(pub(n + "/posting")), memoKey: pub(n),
        postingJsonMetadata: `{"profile":{"name":"${n} <validator> & co"}}`, balance: "0.000 STEEM", sbdBalance: "0.000 SBD",
      }));
    }
    this.svm.stakesAt.set(99000n, stakes({ alice: 45, bob: 30, carol: 15, dave: 10 }));
  }

  signer(name: string, activeWif: string): Signer {
    const cfg = defaultSignerConfig();
    cfg.activeWif = activeWif;
    cfg.stateDir = path.join(this.dir, name);
    cfg.rankDelayMs = 0.000001;
    cfg.policy.fallbackDelayMs = 3600_000; // only the designated member proposes here
    return new Signer(cfg, this.svm.as(name), quietLog, {}, this.node);
  }

  async start(): Promise<void> {
    for (const [n] of WEIGHTS) {
      const s = this.signer(n, wif(n));
      await s.init();
      this.signers.push(s);
    }
  }

  async rounds(k: number): Promise<void> {
    for (let i = 0; i < k; i++) {
      for (const s of this.signers) {
        await s.pass();
        this.node.advance(1);
      }
    }
  }

  close(): void {
    for (const s of this.signers) s.close();
  }
}

test("signers pay withdrawals exactly once", async () => {
  const net = new Network();
  await net.start();
  try {
    net.svm.all.set(6n, withdrawal(6, "rcpt", 12345, "STEEM", 90000));
    net.svm.all.set(9n, withdrawal(9, "rcpt", 2500, "SBD", 95000));
    net.svm.all.set(3n, withdrawal(3, "rcpt", 1000, "STEEM", 100000 + 14400 - 50400 - 1)); // inside the margin
    await net.rounds(3);
    const node = net.node;
    assert.deepEqual(node.rejected, []);
    assert.deepEqual(node.transfers.map((t) => [t.memo, t.to, t.amount]).sort(),
      [["svm-withdrawal 6", "rcpt", "12.345 STEEM"], ["svm-withdrawal 9", "rcpt", "2.500 SBD"]]);
    const bank = node.accounts.get(BANK)!;
    assert.deepEqual([bank.balance, bank.sbdBalance], ["987.655 STEEM", "47.500 SBD"]);

    await net.rounds(2);
    for (const id of [6n, 9n]) net.svm.all.set(id, { ...net.svm.all.get(id)!, status: "PROCESSED" });
    await net.rounds(2);
    assert.equal(node.transfers.length, 2);
    assert.deepEqual(node.rejected, []);
    for (const [n] of WEIGHTS) {
      assert.ok(node.accounts.get(n)!.postingJsonMetadata.startsWith(`{"profile":{"name":"${n} <validator> & co"},"svm_ms":`));
    }
    for (const s of net.signers) {
      for (const [id, v] of parseMetadata(node.accounts.get(s.me)!.postingJsonMetadata).ballot.votes) {
        assert.ok(v.a !== 1 || s.journal!.approvals[id], `${s.me} published an approval missing from its journal`);
      }
    }
  } finally {
    net.close();
  }
});

test("signers sync the validator set", async () => {
  const net = new Network();
  await net.start();
  try {
    net.svm.height_ = 101000n;
    net.svm.stakesAt.set(100000n, stakes({ alice: 50, bob: 30, carol: 15, dave: 5 }));
    const want = bankAuthority([["alice", 5000], ["bob", 3000], ["carol", 1500], ["dave", 500]]);
    await net.rounds(3);
    const node = net.node;
    assert.deepEqual(node.rejected, []);
    const b = node.accounts.get(BANK)!;
    assert.ok(b.owner.equal(want) && b.active.equal(want) && b.posting.equal(want) && b.memoKey === pub("memo"));
    assert.deepEqual([b.owner.total(), b.owner.weightThreshold], [10000, 7500]);
    const before = node.history.get(BANK)!.length;
    await net.rounds(3);
    assert.equal(node.history.get(BANK)!.length, before);
    assert.deepEqual(node.rejected, []);
  } finally {
    net.close();
  }
});

test("dry run publishes nothing", async () => {
  const net = new Network();
  await net.start();
  try {
    net.svm.all.set(6n, withdrawal(6, "rcpt", 12345, "STEEM", 90000));
    for (const s of net.signers) s.cfg.dryRun = true;
    const before = [...net.node.accounts].map(([n, a]) => [n, a.postingJsonMetadata]);
    await net.rounds(3);
    assert.equal(net.node.seen.size, 0);
    assert.deepEqual([...net.node.accounts].map(([n, a]) => [n, a.postingJsonMetadata]), before);
    for (const s of net.signers) assert.deepEqual(s.journal!.approvals, {});
  } finally {
    net.close();
  }
});

test("key guards", async () => {
  const net = new Network();
  const owner = net.signer("alice-owner", wif("alice/owner"));
  owner.svm = net.svm.as("alice");
  await assert.rejects(owner.init(), /OWNER key/);
  owner.close();
  const foreign = net.signer("alice-foreign", wif("bob"));
  foreign.svm = net.svm.as("alice");
  await assert.rejects(foreign.init(), /does not satisfy/);
  foreign.close();
  const cfg = defaultSignerConfig();
  cfg.activeWif = wif("alice");
  for (const v of ["ORACLE_STEEM_OWNER_WIF", "ORACLE_STEEM_POSTING_WIF", "MASTER_PASSWORD", "OWNER"]) {
    assert.throws(() => new Signer(cfg, net.svm.as("alice"), quietLog, { [v]: "5Kxxxxxxxx" }, net.node), new RegExp(v));
  }
  for (const bad of ["", "not-a-wif", wif("alice").slice(0, 50)]) {
    const c = defaultSignerConfig();
    c.activeWif = bad;
    assert.throws(() => new Signer(c, net.svm.as("alice"), quietLog, {}, net.node), SignerConfigError);
  }
});

const hasFlock = spawnSync("flock", ["--version"]).status === 0;

test("one signer per state dir", { skip: !hasFlock && "flock(1) not installed" }, async () => {
  // Two signers on one state dir would overwrite each other's votes: the second must fail.
  const net = new Network();
  await net.start();
  try {
    const second = net.signer("alice", wif("alice"));
    await assert.rejects(second.init(), /already holds/);
    second.close();
  } finally {
    net.close();
  }
});

test("env config", () => {
  assert.equal(buildSignerConfig({}, "https://api.steemit.com", "/d"), undefined);
  const cfg = buildSignerConfig({
    ORACLE_MULTISIG_ENABLED: "true", ORACLE_STEEM_ACTIVE_WIF: " 5Kabc ", ORACLE_STEEM_RPCS: "http://a, http://b",
    ORACLE_MULTISIG_INTERVAL: "30s", ORACLE_MULTISIG_DRY_RUN: "1", ORACLE_MULTISIG_MAX_PER_TX_SBD: "500.000",
  }, "https://api.steemit.com", "/d")!;
  assert.deepEqual([cfg.activeWif, cfg.nodes, cfg.intervalMs, cfg.dryRun, cfg.propose, cfg.policy.maxPerTxMilli],
    ["5Kabc", ["http://a", "http://b"], 30_000, true, true, { SBD: 500000n }]);
  assert.deepEqual(buildSignerConfig({ ORACLE_MULTISIG_ENABLED: "true" }, "https://x", "/d")!.nodes, ["https://x"]);
  for (const env of [{ ORACLE_MULTISIG_ENABLED: "yes" }, { ORACLE_MULTISIG_ENABLED: "true", ORACLE_MULTISIG_THRESHOLD_PERCENT: "0" },
    { ORACLE_MULTISIG_ENABLED: "true", ORACLE_MULTISIG_MAX_PER_TX_STEEM: "5" }]) {
    assert.throws(() => buildSignerConfig(env, "https://x", "/d"), SignerConfigError);
  }
});
