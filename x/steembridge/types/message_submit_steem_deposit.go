package types

import (
	"regexp"

	errortypes "github.com/cosmos/cosmos-sdk/types/errors"
)

// steemTxidRegex matches Steem's lowercase hex transaction id format.
var steemTxidRegex = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ValidateBasic performs stateless sanity checks on the raw facts a bridge
// client submits, rejecting obviously malformed submissions before they
// reach any state access. It intentionally does NOT check anything stateful
// (bridge enabled, gateway match, dedup) — that's ValidateDepositAcceptance's
// job in the keeper, which needs store access this method doesn't have.
func (msg *MsgSubmitSteemDeposit) ValidateBasic() error {
	if !steemTxidRegex.MatchString(msg.Txid) {
		return errortypes.ErrInvalidRequest.Wrapf("txid %q must be 40 lowercase hex characters", msg.Txid)
	}
	if err := ValidateSteemAccountName(msg.SteemSender); err != nil {
		return err
	}
	if err := ValidateSteemAccountName(msg.GatewayAccount); err != nil {
		return err
	}
	if msg.AmountMillisteem == 0 {
		return ErrInvalidAmount.Wrap("amount millisteem must be positive")
	}
	return nil
}
