package relayer

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/client"
	clienttx "github.com/cosmos/cosmos-sdk/client/tx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
)

const (
	// gasPerMsg is a generous flat gas budget per attestation message. Gas
	// is metered but never charged: qualifying attestations are fee-exempt,
	// and non-qualifying zero-fee txs are rejected at CheckTx at no cost.
	gasPerMsg = 400_000
	// gasBase covers tx overhead beyond the per-message cost.
	gasBase = 200_000
	// MaxMsgsPerTx caps attestations per transaction, comfortably under the
	// module's 100-per-validator-per-block free-tx budget.
	MaxMsgsPerTx = 50
	// priceFeedGasPerMsg is the flat gas budget per price-feed message
	// (prevote or vote). Unlike bridge attestations these are NOT fee-exempt
	// (see oracle/PROTOCOL.md §3), so this directly determines the fee paid.
	priceFeedGasPerMsg = 250_000
)

// BroadcastAttestations signs the messages with the relayer key and
// broadcasts a single zero-fee transaction to the local node, returning the
// tx hash. The account sequence is fetched fresh per call (the relayer sends
// at most one tx per poll cycle).
func BroadcastAttestations(ctx context.Context, clientCtx client.Context, keyName string, msgs []sdk.Msg) (string, error) {
	if len(msgs) == 0 {
		return "", nil
	}
	if len(msgs) > MaxMsgsPerTx {
		return "", fmt.Errorf("too many messages in one tx: %d > %d", len(msgs), MaxMsgsPerTx)
	}
	return broadcastTx(ctx, clientCtx, keyName, msgs, gasBase+gasPerMsg*uint64(len(msgs)), "") //nolint:gosec // bounded by MaxMsgsPerTx
}

// BroadcastPriceFeedMsgs signs and broadcasts price-feed messages (a reveal,
// a fresh prevote, or both). Unlike bridge attestations these are NOT
// fee-exempt: the signer's account must hold real asteem and gasPrices must
// be a non-empty Cosmos SDK DecCoins string (e.g. "1000000000asteem") — see
// oracle/PROTOCOL.md §3.
func BroadcastPriceFeedMsgs(ctx context.Context, clientCtx client.Context, keyName string, msgs []sdk.Msg, gasPrices string) (string, error) {
	if len(msgs) == 0 {
		return "", nil
	}
	if gasPrices == "" {
		return "", fmt.Errorf("price feed txs are not fee-exempt: ORACLE_GAS_PRICES must be set")
	}
	return broadcastTx(ctx, clientCtx, keyName, msgs, priceFeedGasPerMsg*uint64(len(msgs)), gasPrices) //nolint:gosec // len(msgs) is always 1-2
}

// broadcastTx signs and broadcasts msgs, optionally paying gasPrices (empty
// means zero-fee, valid only for fee-exempt attestation messages — see
// BroadcastAttestations vs. BroadcastPriceFeedMsgs above).
func broadcastTx(ctx context.Context, clientCtx client.Context, keyName string, msgs []sdk.Msg, gas uint64, gasPrices string) (string, error) {
	txf := clienttx.Factory{}.
		WithTxConfig(clientCtx.TxConfig).
		WithKeybase(clientCtx.Keyring).
		WithAccountRetriever(clientCtx.AccountRetriever).
		WithChainID(clientCtx.ChainID).
		WithSignMode(signing.SignMode_SIGN_MODE_DIRECT).
		WithGas(gas)
	if gasPrices != "" {
		txf = txf.WithGasPrices(gasPrices)
	}

	// Fetch account number + sequence for the signer.
	txf, err := txf.Prepare(clientCtx)
	if err != nil {
		return "", fmt.Errorf("preparing tx factory: %w", err)
	}

	builder, err := txf.BuildUnsignedTx(msgs...)
	if err != nil {
		return "", fmt.Errorf("building tx: %w", err)
	}

	if err := clienttx.Sign(ctx, txf, keyName, builder, true); err != nil {
		return "", fmt.Errorf("signing tx: %w", err)
	}

	txBytes, err := clientCtx.TxConfig.TxEncoder()(builder.GetTx())
	if err != nil {
		return "", fmt.Errorf("encoding tx: %w", err)
	}
	// Computed locally (matches how cosmos-sdk itself derives res.TxHash) so
	// it's available even when BroadcastTxSync errors below without ever
	// returning a *ResultBroadcastTx.
	computedTxHash := fmt.Sprintf("%X", cmttypes.Tx(txBytes).Hash())

	res, err := clientCtx.BroadcastTxSync(txBytes)
	if err != nil {
		// "tx already seen" means the mempool already has this EXACT tx —
		// which happens when a prior attempt's waitForDelivery below timed
		// out (still pending, not failed) and the caller retried: the
		// account sequence hasn't advanced (only increments on inclusion,
		// not on entering the mempool), so the rebuilt tx is byte-identical
		// to the one still in flight. Without this, that's a permanent
		// stall — every retry rebuilds the identical tx, gets rejected as a
		// duplicate, and the cursor never advances. Treat it as "the
		// broadcast already happened" and just wait for the SAME hash.
		if strings.Contains(err.Error(), "already seen") {
			if werr := waitForDelivery(ctx, clientCtx, computedTxHash); werr != nil {
				return computedTxHash, werr
			}
			return computedTxHash, nil
		}
		return "", fmt.Errorf("broadcasting tx: %w", err)
	}
	if res.Code != 0 {
		return res.TxHash, fmt.Errorf("tx rejected (code %d): %s", res.Code, res.RawLog)
	}

	// Sync broadcast only proves the tx passed CheckTx. The scan cursor must
	// not advance past blocks whose attestations were never actually
	// executed, so wait until the tx lands in a block and succeeded. On
	// timeout or delivery failure the caller retries the same Steem blocks —
	// idempotent thanks to the already-attested pre-filter.
	if err := waitForDelivery(ctx, clientCtx, res.TxHash); err != nil {
		return res.TxHash, err
	}
	return res.TxHash, nil
}

// waitForDelivery polls the local node until the tx is found in a block with
// code 0, erroring on delivery failure or timeout.
func waitForDelivery(ctx context.Context, clientCtx client.Context, txHash string) error {
	hashBytes, err := hex.DecodeString(txHash)
	if err != nil {
		return fmt.Errorf("invalid tx hash %q: %w", txHash, err)
	}

	const (
		pollEvery = 2 * time.Second
		// ~6s blocks means 45s (~7-8 blocks) had little margin for ordinary
		// network/mempool jitter before every such hiccup fed directly into
		// the "already seen" retry loop above. 90s gives real headroom.
		maxWait = 90 * time.Second
	)
	deadline := time.Now().Add(maxWait)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollEvery):
		}

		res, err := clientCtx.Client.Tx(ctx, hashBytes, false)
		if err == nil && res != nil {
			if res.TxResult.Code != 0 {
				return fmt.Errorf("attestation tx %s failed in block %d (code %d): %s",
					txHash, res.Height, res.TxResult.Code, res.TxResult.Log)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("attestation tx %s not observed in a block within %s", txHash, maxWait)
		}
	}
}
