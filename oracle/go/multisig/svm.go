package multisig

import "context"

// Withdrawal is the SVM-side bridge-out record a payout must match.
type Withdrawal struct {
	ID          uint64
	Destination string // Steem account
	AmountMilli uint64 // NET payout (gross minus the bridge fee), millisteem/millisbd
	Symbol      string // "STEEM" or "SBD"
	CreatedAt   uint64 // SVM height
	Status      string // "REQUESTED", "PROCESSED", "REFUNDED"
}

// SVM is what the signer reads from the SteemVM chain. Every policy decision
// is checked against it independently; nothing a proposer claims is trusted.
type SVM interface {
	// Height is the latest SVM block height.
	Height(ctx context.Context) (int64, error)
	// WithdrawalTimeoutBlocks is the bridge's automatic-refund window.
	WithdrawalTimeoutBlocks(ctx context.Context) (uint64, error)
	// RequestedWithdrawals lists every withdrawal still awaiting payout.
	RequestedWithdrawals(ctx context.Context) ([]Withdrawal, error)
	// Withdrawal looks one up by ID (any status).
	Withdrawal(ctx context.Context, id uint64) (Withdrawal, bool, error)
	// ValidatorStakes returns, at the given SVM height, every bonded and
	// unjailed validator whose moniker is an ACTIVE Steem name link owned by
	// the operator, as (Steem account, bonded tokens).
	ValidatorStakes(ctx context.Context, height int64) ([]StakeEntry, error)
	// SelfSteemAccount is this oracle's own validator's verified Steem account.
	SelfSteemAccount(ctx context.Context) (string, error)
}
