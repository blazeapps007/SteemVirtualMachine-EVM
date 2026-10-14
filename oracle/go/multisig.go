package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"cosmossdk.io/log/v2"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cosmos/cosmos-sdk/client"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"steemvm/oracle/go/multisig"
	"steemvm/oracle/go/steemtx"
)

// startMultisig starts the automated svm.bank signer in its own goroutine when
// ORACLE_MULTISIG_ENABLED is true. A misconfiguration (a forbidden owner or
// posting key variable, a bad active key, a failed conformance selftest) is
// returned so the oracle refuses to start, rather than running half-armed.
//
// Environment (shared by the Go, Python and JS oracles; oracle/PROTOCOL.md):
//
//	ORACLE_MULTISIG_ENABLED              "true" to run the signer (default false)
//	ORACLE_STEEM_ACTIVE_WIF              the validator's Steem ACTIVE key — the only Steem secret
//	ORACLE_STEEM_RPCS                    comma-separated Steem nodes, own node first (default ORACLE_STEEM_RPC)
//	ORACLE_MULTISIG_INTERVAL             pass interval (default 10s)
//	ORACLE_MULTISIG_THRESHOLD_PERCENT    svm.bank threshold, percent of 10000 (default 75)
//	ORACLE_MULTISIG_REWEIGHT_TOLERANCE   weight points before a reweight is proposed (default 50)
//	ORACLE_MULTISIG_PAYOUT_MARGIN_BLOCKS never pay this close to the SVM refund (default 14400)
//	ORACLE_MULTISIG_PROPOSE / _EXECUTE   "false" to only vote (default true)
//	ORACLE_MULTISIG_DRY_RUN              "true" to log decisions without publishing (default false)
//	ORACLE_MULTISIG_MAX_PER_TX_STEEM/SBD optional per-payout cap, e.g. "500.000" (default none)
func startMultisig(ctx context.Context, logger log.Logger, clientCtx client.Context, operator sdk.AccAddress, nodeRPC, steemRPC, stateDir string) error {
	enabled, err := envBool("ORACLE_MULTISIG_ENABLED", false)
	if err != nil || !enabled {
		return err
	}

	cfg := multisig.SignerConfig{
		Policy:    multisig.DefaultConfig(),
		ActiveWIF: strings.TrimSpace(os.Getenv("ORACLE_STEEM_ACTIVE_WIF")),
		StateDir:  stateDir,
	}
	for _, n := range strings.Split(envOr("ORACLE_STEEM_RPCS", steemRPC), ",") {
		if n = strings.TrimSpace(n); n != "" {
			cfg.Nodes = append(cfg.Nodes, n)
		}
	}
	if d, ok, err := envDuration("ORACLE_MULTISIG_INTERVAL"); err != nil {
		return err
	} else if ok {
		cfg.Interval = d
	}
	if n, ok, err := envUint("ORACLE_MULTISIG_THRESHOLD_PERCENT"); err != nil {
		return err
	} else if ok {
		if n == 0 || n > 100 {
			return fmt.Errorf("ORACLE_MULTISIG_THRESHOLD_PERCENT must be 1..100")
		}
		cfg.Policy.ThresholdPercent = uint32(n)
	}
	if n, ok, err := envUint("ORACLE_MULTISIG_REWEIGHT_TOLERANCE"); err != nil {
		return err
	} else if ok {
		if n > 10000 {
			return fmt.Errorf("ORACLE_MULTISIG_REWEIGHT_TOLERANCE must be 0..10000")
		}
		cfg.Policy.ReweightTolerance = uint16(n)
	}
	if n, ok, err := envUint("ORACLE_MULTISIG_PAYOUT_MARGIN_BLOCKS"); err != nil {
		return err
	} else if ok {
		cfg.Policy.PayoutMarginBlocks = n
	}
	if cfg.Propose, err = envBool("ORACLE_MULTISIG_PROPOSE", true); err != nil {
		return err
	}
	if cfg.Execute, err = envBool("ORACLE_MULTISIG_EXECUTE", true); err != nil {
		return err
	}
	if cfg.DryRun, err = envBool("ORACLE_MULTISIG_DRY_RUN", false); err != nil {
		return err
	}
	for _, sym := range []string{"STEEM", "SBD"} {
		v := strings.TrimSpace(os.Getenv("ORACLE_MULTISIG_MAX_PER_TX_" + sym))
		if v == "" {
			continue
		}
		a, err := steemtx.ParseAsset(v + " " + sym)
		if err != nil {
			return fmt.Errorf("ORACLE_MULTISIG_MAX_PER_TX_%s: %w", sym, err)
		}
		if cfg.Policy.MaxPerTxMilli == nil {
			cfg.Policy.MaxPerTxMilli = map[string]uint64{}
		}
		cfg.Policy.MaxPerTxMilli[sym] = uint64(a.Amount)
	}

	rpcClient, err := rpchttp.New(nodeRPC, "/websocket")
	if err != nil {
		return fmt.Errorf("multisig: node RPC client: %w", err)
	}
	svm := multisig.QuerySVM{ClientCtx: clientCtx.WithClient(rpcClient), Operator: sdk.ValAddress(operator)}

	signer, err := multisig.NewSigner(cfg, svm, logger.With("module", "svm-bank-signer"))
	if err != nil {
		return err
	}
	logger.Info("svm.bank multisig signer enabled", "nodes", len(cfg.Nodes), "interval", cfg.Interval,
		"threshold_percent", cfg.Policy.ThresholdPercent, "dry_run", cfg.DryRun)
	go func() {
		if err := signer.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("svm.bank signer stopped", "err", err)
		}
	}()
	return nil
}

func envBool(key string, def bool) (bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false: %w", key, err)
	}
	return b, nil
}
