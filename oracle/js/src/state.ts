// state.ts — JSON state-file persistence for the two independent duty
// cursors, matching oracle/go/relayer/state.go and pricefeeder_state.go
// field-for-field so a validator can switch client language mid-operation
// without losing progress (see oracle/PROTOCOL.md §8: shared JSON schema).
// Both files are written atomically (temp file + rename) so a crash
// mid-write never leaves a truncated/corrupt cursor behind.

import { promises as fs } from "node:fs";
import * as path from "node:path";

/** The Steem scan cursor. Lives outside consensus state — each validator
 * tracks its own scanning progress independently. */
export interface RelayerState {
  last_scanned_block: number;
}

export const STATE_FILE_NAME = "steem_relayer_state.json";

/** The price feeder's pending commit (cross-cycle memory needed to reveal
 * what was previously committed — see priceFeeder.ts). Kept in a separate
 * file from the scan cursor so the two duties' failure domains stay
 * independent. */
export interface FeederState {
  prevote_period: number;
  salt: string;
  exchange_rates: string;
}

export const PRICE_FEEDER_STATE_FILE_NAME = "price_feeder_state.json";

const EMPTY_RELAYER_STATE: RelayerState = { last_scanned_block: 0 };
const EMPTY_FEEDER_STATE: FeederState = { prevote_period: 0, salt: "", exchange_rates: "" };

async function loadJson<T>(dir: string, fileName: string, empty: T): Promise<T> {
  const target = path.join(dir, fileName);
  let raw: string;
  try {
    raw = await fs.readFile(target, "utf8");
  } catch (err) {
    if (isNotFound(err)) {
      return { ...empty };
    }
    throw err;
  }
  try {
    return JSON.parse(raw) as T;
  } catch (err) {
    throw new Error(`corrupt state file ${target}: ${(err as Error).message}`);
  }
}

async function saveJson(dir: string, fileName: string, value: unknown): Promise<void> {
  const target = path.join(dir, fileName);
  const tmp = target + ".tmp";
  await fs.writeFile(tmp, JSON.stringify(value), { mode: 0o600 });
  await fs.rename(tmp, target);
}

/** Reads the relayer scan cursor from dir/steem_relayer_state.json. A
 * missing file returns the zero state (first run), not an error. */
export function loadState(dir: string): Promise<RelayerState> {
  return loadJson(dir, STATE_FILE_NAME, EMPTY_RELAYER_STATE);
}

/** Atomically persists the relayer scan cursor. */
export function saveState(dir: string, state: RelayerState): Promise<void> {
  return saveJson(dir, STATE_FILE_NAME, state);
}

/** Reads the price feeder's pending commit from
 * dir/price_feeder_state.json. A missing file returns the zero state
 * (first run, or a feeder that has never committed a prevote). */
export function loadFeederState(dir: string): Promise<FeederState> {
  return loadJson(dir, PRICE_FEEDER_STATE_FILE_NAME, EMPTY_FEEDER_STATE);
}

/** Atomically persists the price feeder's pending commit. */
export function saveFeederState(dir: string, state: FeederState): Promise<void> {
  return saveJson(dir, PRICE_FEEDER_STATE_FILE_NAME, state);
}

function isNotFound(err: unknown): boolean {
  return typeof err === "object" && err !== null && (err as NodeJS.ErrnoException).code === "ENOENT";
}
