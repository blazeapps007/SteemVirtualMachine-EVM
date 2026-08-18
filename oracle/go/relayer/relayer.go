package relayer

import (
	"context"
	"fmt"
	"strings"
	"time"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"

	"github.com/cosmos/cosmos-sdk/client"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	oracledatatypes "steemvm/x/oracle/data/types"
	"steemvm/x/oracle/bridge/types"
)

// Run drives the Steem relayer loop until ctx is canceled. It is a pure
// library entry point: the standalone oracle binary supplies a fully-built
// clientCtx (codec + in-memory keyring holding cfg.KeyName), the SteemVM node
// RPC endpoint to broadcast/query against, the relayer Config, and a directory
// for the scan-cursor state file. Every cycle failure is logged and retried;
// Run only returns on ctx cancellation or a fatal setup error.
//
// priceSource, if non-nil, activates the price feeder (x/oracle/data) on the
// same poll loop: gasPrices must be set too (price-feed txs are NOT
// fee-exempt — see oracle/PROTOCOL.md §3). A nil priceSource idles the
// feeder entirely, same as today's default.
func Run(ctx context.Context, logger cycleLogger, clientCtx client.Context, nodeRPC string, cfg Config, stateDir string, priceSource PriceSource, gasPrices string) error {
	// Resolve the signing key from the supplied keyring.
	if clientCtx.Keyring == nil {
		return fmt.Errorf("no keyring available on client context")
	}
	record, err := clientCtx.Keyring.Key(cfg.KeyName)
	if err != nil {
		return fmt.Errorf("key %q not found in keyring: %w", cfg.KeyName, err)
	}
	signerAddr, err := record.GetAddress()
	if err != nil {
		return fmt.Errorf("cannot read key address: %w", err)
	}
	valoperAddr := sdk.ValAddress(signerAddr).String()

	// Connect to the SteemVM node RPC (broadcast + ABCI queries route through it).
	rpcClient, err := rpchttp.New(nodeRPC, "/websocket")
	if err != nil {
		return fmt.Errorf("cannot create node RPC client for %q: %w", nodeRPC, err)
	}
	clientCtx = clientCtx.
		WithClient(rpcClient).
		WithFromName(cfg.KeyName).
		WithFromAddress(signerAddr).
		WithBroadcastMode("sync")

	// Wait until the node is up and synced, then learn the chain-id.
	chainID := waitForLocalNode(ctx, logger, rpcClient)
	if chainID == "" {
		return ctx.Err() // ctx canceled
	}
	clientCtx = clientCtx.WithChainID(chainID)

	steem := NewSteemClient(cfg.SteemRPCURL)
	bridgeQuery := types.NewQueryClient(clientCtx)
	stakingQuery := stakingtypes.NewQueryClient(clientCtx)
	oracledataQuery := oracledatatypes.NewQueryClient(clientCtx)

	state, err := LoadState(stateDir)
	if err != nil {
		return fmt.Errorf("cannot load state from %q: %w", stateDir, err)
	}

	feeder := Feeder{Validator: signerAddr.String(), Source: priceSource}
	// lastHandledPeriod guards against re-firing Step (and re-paying gas) more
	// than once per vote period across ticks; 0 is a safe "never handled"
	// sentinel in practice (a chain's genesis-era period 0 sees at most a
	// handful of redundant prevotes, harmless).
	var lastHandledPeriod uint64

	logger.Info("steem oracle started",
		"steem_rpc", cfg.SteemRPCURL, "node_rpc", nodeRPC, "chain_id", chainID,
		"signer", signerAddr.String(), "valoper", valoperAddr,
		"poll_interval", cfg.PollInterval.String(), "last_scanned_block", state.LastScannedBlock,
		"price_feeder_enabled", priceSource != nil)

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	notBondedLogged := false

	for {
		select {
		case <-ctx.Done():
			logger.Info("steem oracle stopped")
			return nil
		case <-ticker.C:
		}

		if err := runCycle(ctx, logger, cfg, clientCtx, steem, bridgeQuery, stakingQuery, signerAddr, valoperAddr, stateDir, &state, &notBondedLogged); err != nil {
			logger.Error("steem oracle cycle failed", "err", err)
		}

		if priceSource != nil {
			if err := runPriceFeederCycle(ctx, logger, clientCtx, oracledataQuery, feeder, gasPrices, stateDir, &lastHandledPeriod); err != nil {
				logger.Error("price feeder cycle failed", "err", err)
			}
		}
	}
}

// runPriceFeederCycle checks whether the chain has entered a new vote period
// since the last handled one and, if so, runs one Feeder.Step: reveals the
// prior period's commit (if still in the reveal window) and/or commits a
// fresh prevote, broadcasting whatever messages result and persisting the
// new commit state. A no-op (same period as last time) costs nothing.
func runPriceFeederCycle(
	ctx context.Context,
	logger cycleLogger,
	clientCtx client.Context,
	oracledataQuery oracledatatypes.QueryClient,
	feeder Feeder,
	gasPrices string,
	stateDir string,
	lastHandledPeriod *uint64,
) error {
	paramsResp, err := oracledataQuery.Params(ctx, &oracledatatypes.QueryParamsRequest{})
	if err != nil {
		return fmt.Errorf("querying oracledata params: %w", err)
	}
	votePeriod := paramsResp.Params.VotePeriod
	if votePeriod == 0 {
		return fmt.Errorf("oracledata vote period is zero")
	}

	status, err := clientCtx.Client.Status(ctx)
	if err != nil {
		return fmt.Errorf("querying node status: %w", err)
	}
	height := uint64(status.SyncInfo.LatestBlockHeight) //nolint:gosec // block heights never approach int64 overflow
	period := height / votePeriod

	if period == *lastHandledPeriod {
		return nil // already acted this period; wait for the next boundary
	}

	prevState, err := LoadFeederState(stateDir)
	if err != nil {
		return fmt.Errorf("cannot load price feeder state from %q: %w", stateDir, err)
	}

	msgs, newState, err := feeder.Step(period, paramsResp.Params.Whitelist, prevState)
	if err != nil {
		// A price-fetch error still lets msgs (a pending reveal, if any)
		// through — only the fresh-prevote half of Step failed.
		logger.Error("price feeder: fetching prices failed", "err", err)
	}
	if len(msgs) == 0 {
		*lastHandledPeriod = period
		return nil
	}

	txHash, err := BroadcastPriceFeedMsgs(ctx, clientCtx, clientCtx.FromName, msgs, gasPrices)
	if err != nil {
		return fmt.Errorf("broadcasting price feed messages: %w", err)
	}
	logger.Info("price feeder broadcast", "period", period, "count", len(msgs), "tx", txHash)

	if err := SaveFeederState(stateDir, newState); err != nil {
		return fmt.Errorf("saving price feeder state: %w", err)
	}
	*lastHandledPeriod = period
	return nil
}

type cycleLogger interface {
	Info(msg string, keyVals ...any)
	Error(msg string, keyVals ...any)
	Debug(msg string, keyVals ...any)
}

// runCycle performs one poll: check feature flags and bonded status, scan
// new irreversible Steem blocks, attest gateway transfers, advance cursor.
func runCycle(
	ctx context.Context,
	logger cycleLogger,
	cfg Config,
	clientCtx client.Context,
	steem *SteemClient,
	bridgeQuery types.QueryClient,
	stakingQuery stakingtypes.QueryClient,
	signerAddr sdk.AccAddress,
	valoperAddr string,
	stateDir string,
	state *State,
	notBondedLogged *bool,
) error {
	params, err := bridgeQuery.Params(ctx, &types.QueryParamsRequest{})
	if err != nil {
		return err
	}
	// GatewayAccount is a hardcoded chain constant, never read from params
	// (see x/oracle/bridge/types.GatewayAccount) — this closes off the class
	// of mistake where a stale/wrong configured value silently diverges from
	// what the chain actually enforces.
	gateway := types.GatewayAccount
	if !params.Params.BridgeEnabled && !params.Params.NameServiceEnabled {
		logger.Debug("steem relayer idle: bridge and name service disabled")
		return nil
	}

	// Only bonded validators' attestations count (and only theirs are
	// fee-exempt) — idle quietly otherwise.
	valResp, err := stakingQuery.Validator(ctx, &stakingtypes.QueryValidatorRequest{ValidatorAddr: valoperAddr})
	if err != nil || !valResp.Validator.IsBonded() {
		if !*notBondedLogged {
			logger.Info("steem relayer idle: key is not a bonded validator", "valoper", valoperAddr)
			*notBondedLogged = true
		}
		return nil
	}
	*notBondedLogged = false

	lib, err := steem.LastIrreversibleBlock()
	if err != nil {
		return err
	}

	// First run: establish the cursor. Precedence: the node's local
	// start-block override, then the chain-wide relayer_start_block param
	// (the shared anchor recorded at launch so every validator scans the
	// same range regardless of when it first starts), then the current LIB.
	if state.LastScannedBlock == 0 {
		state.LastScannedBlock = initialCursor(cfg.StartBlock, params.Params.RelayerStartBlock, lib)
		logger.Info("steem relayer cursor initialized",
			"cursor", state.LastScannedBlock,
			"source", cursorSource(cfg.StartBlock, params.Params.RelayerStartBlock))
		return SaveState(stateDir, *state)
	}

	from := state.LastScannedBlock + 1
	to := min(lib, state.LastScannedBlock+cfg.MaxBlocksPerPoll)
	if from > to {
		return nil
	}

	blocks, err := steem.FetchBlocks(from, to)
	if err != nil {
		return err
	}

	// Accumulate attestations across blocks, keeping block boundaries so the
	// cursor only advances past fully-included blocks. One tx per cycle.
	var msgs []sdk.Msg
	lastFullBlock := state.LastScannedBlock
	for _, nb := range blocks {
		transfers := ExtractGatewayTransfers(nb.Num, nb.Block, gateway, cfg.SteemSymbol, cfg.SbdSymbol)
		blockMsgs := make([]sdk.Msg, 0, len(transfers))
		for _, transfer := range transfers {
			intent := RouteMemo(transfer.Memo)
			if intent == IntentRegister && !params.Params.NameServiceEnabled {
				continue
			}
			if intent == IntentDeposit && !params.Params.BridgeEnabled {
				continue
			}
			// Only attest transfers whose memo resolves to a supported
			// destination (a steem1.../0x address, optionally svm-deposit /
			// svm-register prefixed) — the exact in-consensus parser. Transfers
			// with an unparseable memo (a human note, a wrong address, etc.) are
			// ignored: we don't broadcast attestations, and no on-chain record
			// is created for transfers the bridge cannot act on.
			if _, _, ok := types.DeriveDestination(transfer.Memo); !ok {
				continue
			}
			attested, err := alreadyAttested(ctx, bridgeQuery, transfer, intent, valoperAddr)
			if err != nil {
				return err
			}
			if attested {
				continue
			}
			blockMsgs = append(blockMsgs, BuildMsg(transfer, intent, signerAddr, gateway))
		}
		// Withdrawal payouts: gateway→user transfers memoed "svm-withdrawal <id>".
		// Optimistic — every payout seen is attested once (the cursor scans each
		// block exactly once); the chain benign-no-ops duplicates and finalizes the
		// withdrawal REQUESTED→PROCESSED at 2/3, so no per-validator dedup query is
		// needed here. Gated on bridge-out being enabled (the v0.0.3 payout path).
		if params.Params.BridgeOutEnabled {
			for _, payout := range ExtractGatewayPayouts(nb.Num, nb.Block, gateway) {
				blockMsgs = append(blockMsgs, &types.MsgAttestWithdrawalPayout{
					Validator:      signerAddr.String(),
					WithdrawalId:   payout.WithdrawalID,
					SteemTxid:      payout.Txid,
					OpIndex:        payout.OpIndex,
					SteemBlock:     payout.SteemBlock,
					SteemTimestamp: payout.SteemTimestamp,
				})
			}
		}
		if len(msgs)+len(blockMsgs) > MaxMsgsPerTx {
			break // stop at a block boundary; the rest is picked up next cycle
		}
		msgs = append(msgs, blockMsgs...)
		lastFullBlock = nb.Num
	}

	if len(msgs) > 0 {
		txHash, err := BroadcastAttestations(ctx, clientCtx, cfg.KeyName, msgs)
		if err != nil {
			return err // cursor untouched; the same blocks are rescanned next cycle
		}
		logger.Info("steem relayer attested transfers",
			"count", len(msgs), "blocks", lastFullBlock-state.LastScannedBlock, "tx", txHash)
	}

	if lastFullBlock != state.LastScannedBlock {
		state.LastScannedBlock = lastFullBlock
		return SaveState(stateDir, *state)
	}
	return nil
}

// initialCursor picks the first-run scan cursor (the last "already scanned"
// block, i.e. scanning begins at cursor+1): a local app.toml start-block
// override wins, then the chain's relayer_start_block param, then Steem's
// current last irreversible block.
func initialCursor(localStart, chainStart, lib uint64) uint64 {
	switch {
	case localStart > 0:
		return localStart - 1
	case chainStart > 0:
		return chainStart - 1
	default:
		return lib
	}
}

func cursorSource(localStart, chainStart uint64) string {
	switch {
	case localStart > 0:
		return "app.toml start-block override"
	case chainStart > 0:
		return "on-chain relayer_start_block param"
	default:
		return "current steem last irreversible block"
	}
}

// alreadyAttested reports whether this validator has already confirmed the
// transfer's (txid, opIndex) key, or the record is past the point where new
// attestations are accepted. Keeps rescans (lost state file, retried cycles)
// idempotent — a duplicate message would disqualify the whole zero-fee tx.
func alreadyAttested(ctx context.Context, q types.QueryClient, t Transfer, intent Intent, valoperAddr string) (bool, error) {
	switch intent {
	case IntentRegister:
		resp, err := q.NameRegistrationByTxid(ctx, &types.QueryNameRegistrationByTxidRequest{Txid: t.Txid, OpIndex: t.OpIndex})
		if err != nil {
			return false, ignoreNotFound(err)
		}
		if resp.Registration.Status != types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_PENDING {
			return true, nil
		}
		return hasConfirmation(resp.Registration.ValidatorConfirmations, valoperAddr), nil
	default:
		resp, err := q.DepositByTxid(ctx, &types.QueryDepositByTxidRequest{Txid: t.Txid, OpIndex: t.OpIndex})
		if err != nil {
			return false, ignoreNotFound(err)
		}
		if resp.Deposit.Status != types.DepositStatus_DEPOSIT_STATUS_PENDING {
			return true, nil
		}
		return hasConfirmation(resp.Deposit.ValidatorConfirmations, valoperAddr), nil
	}
}

func hasConfirmation(confirmations []*types.Confirmation, valoperAddr string) bool {
	for _, c := range confirmations {
		if c.ValidatorAddress == valoperAddr {
			return true
		}
	}
	return false
}

// ignoreNotFound maps "key not found" query errors to nil (meaning: not yet
// attested); anything else is a real error that aborts the cycle. ABCI query
// errors arrive as flattened strings, so this matches on the message.
func ignoreNotFound(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "NotFound") {
		return nil
	}
	return err
}

// waitForLocalNode blocks until the local node RPC responds and is done
// catching up, returning the chain-id (empty when ctx is canceled).
func waitForLocalNode(ctx context.Context, logger cycleLogger, rpcClient *rpchttp.HTTP) string {
	for {
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(2 * time.Second):
		}
		status, err := rpcClient.Status(ctx)
		if err != nil {
			continue // node still booting
		}
		if status.SyncInfo.CatchingUp {
			logger.Debug("steem relayer waiting: node is catching up")
			continue
		}
		return status.NodeInfo.Network
	}
}
