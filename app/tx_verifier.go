package app

import (
	"github.com/cosmos/cosmos-sdk/baseapp"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

var _ baseapp.ProposalTxVerifier = &NoCheckProposalTxVerifier{}

// NoCheckProposalTxVerifier is used at proposal-preparation time with the
// Krakatoa EVM mempool (see configureEVMMempool in app/evm.go). BaseApp's
// default PrepareProposalVerifyTx re-runs the full ante-handler chain in
// CheckTx mode for every tx; with Krakatoa, every tx selected for the
// proposal has already been ante-validated when it entered the mempool, so
// that re-verification is redundant. This mirrors cosmos/evm's reference
// evmd/tx_verifier.go verbatim.
type NoCheckProposalTxVerifier struct {
	*baseapp.BaseApp
}

// NewNoCheckProposalTxVerifier wraps the app's BaseApp so PrepareProposal
// only encodes txs (to bytes) instead of fully re-validating them.
func NewNoCheckProposalTxVerifier(b *baseapp.BaseApp) *NoCheckProposalTxVerifier {
	return &NoCheckProposalTxVerifier{BaseApp: b}
}

// PrepareProposalVerifyTx overrides the typical tx verification done in
// BaseApp's PrepareProposalHandler. We only verify that the tx can be
// encoded to bytes, since callers guarantee every tx offered here is already
// valid (Krakatoa ante-validates on mempool insertion).
func (txv *NoCheckProposalTxVerifier) PrepareProposalVerifyTx(tx sdk.Tx) ([]byte, error) {
	return txv.TxEncode(tx)
}
