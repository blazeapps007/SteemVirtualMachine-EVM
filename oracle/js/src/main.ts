// main.ts — entrypoint for the standalone JS/TS SteemVM bridge oracle.
// Mirrors oracle/go/main.go: watches Steem for gateway transfers and,
// when its configured key belongs to a bonded validator, broadcasts the
// matching bridge-deposit / withdrawal-payout / name-registration
// attestations, plus the commit-reveal price feed (ORACLE_GAS_PRICES
// defaults on, see config.ts). Configuration is entirely from environment
// variables — see oracle/.env.example.

import { loadConfig, loadSigner } from "./config";
import { buildRegistry } from "./broadcast";
import { CompositePriceSource, type ExternalPriceClient, type PriceSource } from "./priceFeeder";
import { CMCClient } from "./priceSources/cmc";
import { CoinGeckoClient } from "./priceSources/coingecko";
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
    // cfg.priceSource picks which external client prices STEEM/USD_External
    // + SBD/USD_External -- "cmc" (default, for existing operators whose
    // .env predates this option) or "coingecko". Unlike CMC's key,
    // CoinGecko's is optional: its public /simple/price endpoint works
    // keyless, just at a lower rate limit.
    let external: ExternalPriceClient | undefined;
    if (cfg.priceSource === "cmc") {
      external = cfg.cmcApiKey ? new CMCClient(cfg.cmcApiKey, cfg.cmcBaseUrl || undefined) : undefined;
      if (!external) {
        logger.info("price feeder: ORACLE_CMC_API_KEY not set, STEEM/USD_External and SBD/USD_External will be skipped");
      }
    } else if (cfg.priceSource === "coingecko") {
      if (!cfg.coingeckoApiKey) {
        logger.info("price feeder: ORACLE_COINGECKO_API_KEY not set, using CoinGecko's public rate limit");
      }
      external = new CoinGeckoClient(cfg.coingeckoApiKey, cfg.coingeckoBaseUrl || undefined);
    } else {
      throw new Error(`unknown ORACLE_PRICE_SOURCE "${cfg.priceSource}": must be "cmc" or "coingecko"`);
    }
    const steemPriceSource = new SteemPriceSource(new SteemClient(cfg.steemRpcUrl));
    priceSource = new CompositePriceSource(external, steemPriceSource);
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
