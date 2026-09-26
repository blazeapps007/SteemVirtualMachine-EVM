import { test } from "node:test";
import assert from "node:assert/strict";

import { validatorCanSubmit } from "../src/validatorGate";

// The jailed/not-bonded gate (oracle/PROTOCOL.md §10): same table as
// oracle/go/relayer/validatorgate_test.go and oracle/python/tests/test_validator_gate.py.
const cases: Array<[string, Parameters<typeof validatorCanSubmit>[0], boolean, string]> = [
  ["bonded, not jailed", { status: "BOND_STATUS_BONDED", jailed: false }, true, ""],
  ["jailed while still marked bonded", { status: "BOND_STATUS_BONDED", jailed: true }, false, "validator is jailed"],
  ["jailed and unbonding", { status: "BOND_STATUS_UNBONDING", jailed: true }, false, "validator is jailed"],
  ["unbonding, not jailed", { status: "BOND_STATUS_UNBONDING", jailed: false }, false, "validator is not bonded (BOND_STATUS_UNBONDING)"],
  ["unbonded, not jailed", { status: "BOND_STATUS_UNBONDED", jailed: false }, false, "validator is not bonded (BOND_STATUS_UNBONDED)"],
  ["not a validator", undefined, false, "not a validator"],
];

for (const [name, validator, ok, reason] of cases) {
  test(`validatorCanSubmit: ${name}`, () => {
    assert.deepEqual(validatorCanSubmit(validator), { ok, reason });
  });
}
