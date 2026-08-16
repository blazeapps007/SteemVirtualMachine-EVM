package types

import (
	errortypes "github.com/cosmos/cosmos-sdk/types/errors"
)

// ValidateBasic performs stateless sanity checks on the raw facts a validator
// submits for a name registration, rejecting obviously malformed submissions
// before they reach any state access. Stateful checks (name service enabled,
// gateway match, minimum amount, dedup) are ValidateNameRegistrationAcceptance's
// job in the keeper.
func (msg *MsgSubmitNameRegistration) ValidateBasic() error {
	if !steemTxidRegex.MatchString(msg.Txid) {
		return errortypes.ErrInvalidRequest.Wrapf("txid %q must be 40 lowercase hex characters", msg.Txid)
	}
	if err := ValidateSteemAccountName(msg.SteemAccount); err != nil {
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
