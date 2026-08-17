// main.ts — entrypoint for the standalone JS/TS SteemVM bridge oracle.
// Mirrors oracle/go/main.go: watches Steem for gateway transfers and,
// when its configured key belongs to a bonded validator, broadcasts the
// matching bridge-deposit / withdrawal-payout / name-registration
// attestations, plus (when ORACLE_GAS_PRICES is set) the commit-reveal
// price feed. Configuration is entirely from environment variables — see
// oracle/.env.example.

import { loadConfig, loadSigner } from "./config";
import { buildRegistry } from "./broadcast";
import { CompositePriceSource, type PriceSource } from "./priceFeeder";
import { CMCClient } from "./priceSources/cmc";
import { SteemPriceSource } from "./priceSources/steemPrices";
import { SteemClient } from "./steemClient";
import { run, type CycleLogger } from "./relayer";

function makeLogger(): CycleLogger {
  const withPrefix = (level: string) => (msg: string, meta?: Record<string, unknown>) => {
    const suffix = meta ? " " + Object.entries(meta).map(([k, v]) => `${k}=${JSON.stringify(v)}`).join(" ") : "";
    // eslint-disable-next-line no-console
    console.log(`${new Date().toISOString()} ${level} module=steem-oracle ${msg}${suffix}`);
  };
  return { info: withPrefix("INFO"), error: withPrefix("ERROR"), debug: withPrefix("DEBUG") };
}

async function main(): Promise<void> {
  const logger = makeLogger();
  const cfg = loadConfig();
  const signer = loadSigner();
  const registry = buildRegistry();

  logger.info("oracle key loaded", {
    address: signer.address,
    node_rest: cfg.nodeRestUrl,
    steem_rpc: cfg.steemRpcUrl,
  });

  let priceSource: PriceSource | undefined;
  if (cfg.gasPrices) {
    const cmc = cfg.cmcApiKey ? new CMCClient(cfg.cmcApiKey, cfg.cmcBaseUrl || undefined) : undefined;
    if (!cmc) {
      logger.info("price feeder: ORACLE_CMC_API_KEY not set, STEEM/USD and SBD/USD will be skipped");
    }
    const steemPriceSource = new SteemPriceSource(new SteemClient(cfg.steemRpcUrl));
    priceSource = new CompositePriceSource(cmc, steemPriceSource);
    logger.info("price feeder enabled", { gas_prices: cfg.gasPrices });
  } else {
    logger.info("price feeder disabled: set ORACLE_GAS_PRICES to activate (price-feed txs are not fee-exempt)");
  }

  const controller = new AbortController();
  const stop = () => controller.abort();
  process.once("SIGINT", stop);
  process.once("SIGTERM", stop);

  await run({
    logger,
    cfg,
    signer,
    registry,
    stateDir: cfg.stateDir,
    priceSource,
    abortSignal: controller.signal,
  });
}

main().catch((err) => {
  // eslint-disable-next-line no-console
  console.error(`oracle exited: ${err instanceof Error ? err.stack || err.message : String(err)}`);
  process.exit(1);
});
