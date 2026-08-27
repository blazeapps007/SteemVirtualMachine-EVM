package types

// ValidateBasic performs stateless sanity checks on a bridge-out request.
// Statefulness (bridge_out_enabled) is checked in the keeper, which has
// store access this method doesn't.
func (msg *MsgBridgeOut) ValidateBasic() error {
	if err := ValidateSteemAccountName(msg.DestinationSteemAccount); err != nil {
		return err
	}
	if msg.AmountAsteem.IsNil() || !msg.AmountAsteem.IsPositive() {
		return ErrInvalidAmount.Wrap("amount must be positive")
	}
	if !msg.AmountAsteem.Mod(MillisteemToAsteemFactor).IsZero() {
		return ErrInvalidAmount.Wrap("amount must be a whole multiple of 10^15 asteem (one millisteem)")
	}
	return nil
}
