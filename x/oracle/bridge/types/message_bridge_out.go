package types

// ValidateBasic performs stateless sanity checks on a bridge-out request.
// Statefulness (bridge_out_enabled) is checked in the keeper, which has
// store access this method doesn't.
func (msg *MsgBridgeOut) ValidateBasic() error {
	if err := ValidateWithdrawalDestination(msg.DestinationSteemAccount); err != nil {
		return err
	}
	if msg.AmountAsteem.IsNil() || !msg.AmountAsteem.IsPositive() {
		return ErrInvalidAmount.Wrap("amount must be positive")
	}
	if !msg.AmountAsteem.Mod(MillisteemToAsteemFactor).IsZero() {
		return ErrInvalidAmount.Wrap("amount must be a whole multiple of 10^15 asteem (one millisteem)")
	}
	return ValidateWithdrawalMemo(msg.Memo)
}

// ValidateWithdrawalDestination checks a bridge-out's Steem recipient. The
// gateway itself is refused: a payout to it would read as a new deposit, with a
// memo the user chose.
func ValidateWithdrawalDestination(account string) error {
	if err := ValidateSteemAccountName(account); err != nil {
		return err
	}
	if account == GatewayAccount {
		return ErrInvalidSteemAccount.Wrap("cannot bridge out to the gateway account")
	}
	return nil
}
