package relayer

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/server"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"steemvm/x/steembridge/types"
)

// Start runs the in-process Steem relayer until ctx is canceled. It is
// spawned as a goroutine by the start command and MUST never crash the node:
// every failure path logs and retries. It returns immediately when the
// [steem-relayer] app.toml section is not configured.
func Start(ctx context.Context, svrCtx *server.Context, clientCtx client.Context) {
	logger := svrCtx.Logger.With("module", "steem-relayer")

	cfg := ReadFromAppOpts(svrCtx.Viper)
	if !cfg.Enabled() {
		logger.Info("steem relayer disabled (set [steem-relayer] steem-rpc-url and key-name in app.toml to enable)")
		return
	}

	// Resolve the signing key from the node's keyring.
	if clientCtx.Keyring == nil {
		logger.Error("steem relayer: no keyring available on client context")
		return
	}
	record, err := clientCtx.Keyring.Key(cfg.KeyName)
	if err != nil {
		logger.Error("steem relayer: key not found in keyring", "key", cfg.KeyName, "err", err)
		return
	}
	signerAddr, err := record.GetAddress()
	if err != nil {
		logger.Error("steem relayer: cannot read key address", "err", err)
		return
	}
	valoperAddr := sdk.ValAddress(signerAddr).String()

	// Connect to the local node RPC.
	rpcClient, err := rpchttp.New(svrCtx.Config.RPC.ListenAddress, "/websocket")
	if err != nil {
		logger.Error("steem relayer: cannot create local RPC client", "err", err)
		return
	}
	clientCtx = clientCtx.
		WithClient(rpcClient).
		WithFromName(cfg.KeyName).
		WithFromAddress(signerAddr).
		WithBroadcastMode("sync")

	// Wait until the local node is up and synced, then learn the chain-id.
	chainID := waitForLocalNode(ctx, logger, rpcClient)
	if chainID == "" {
		return // ctx canceled
	}
	clientCtx = clientCtx.WithChainID(chainID)

	steem := NewSteemClient(cfg.SteemRPCURL)
	bridgeQuery := types.NewQueryClient(clientCtx)
	stakingQuery := stakingtypes.NewQueryClient(clientCtx)
	stateDir := filepath.Join(svrCtx.Config.RootDir, "data")

	state, err := LoadState(stateDir)
	if err != nil {
		logger.Error("steem relayer: cannot load state", "err", err)
		return
	}

	logger.Info("steem relayer started",
		"steem_rpc", cfg.SteemRPCURL, "key", cfg.KeyName, "signer", signerAddr.String(),
		"poll_interval", cfg.PollInterval.String(), "last_scanned_block", state.LastScannedBlock)

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	notBondedLogged := false

	for {
		select {
		case <-ctx.Done():
			logger.Info("steem relayer stopped")
			return
		case <-ticker.C:
		}

		if err := runCycle(ctx, logger, cfg, clientCtx, steem, bridgeQuery, stakingQuery, signerAddr, valoperAddr, stateDir, &state, &notBondedLogged); err != nil {
			logger.Error("steem relayer cycle failed", "err", err)
		}
	}
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
	gateway := params.Params.GatewayAccount
	if gateway == "" || (!params.Params.BridgeEnabled && !params.Params.NameServiceEnabled) {
		logger.Debug("steem relayer idle: bridge and name service disabled or no gateway configured")
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
		transfers := ExtractGatewayTransfers(nb.Num, nb.Block, gateway)
		blockMsgs := make([]sdk.Msg, 0, len(transfers))
		for _, transfer := range transfers {
			intent := RouteMemo(transfer.Memo)
			if intent == IntentRegister && !params.Params.NameServiceEnabled {
				continue
			}
			if intent == IntentDeposit && !params.Params.BridgeEnabled {
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
