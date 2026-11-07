package types

// ValidateBasic bounds the observed payout facts. Whether they match the
// withdrawal is checked in the keeper, which has store access this method
// doesn't.
func (msg *MsgAttestWithdrawalPayout) ValidateBasic() error {
	if err := ValidateSteemAccountName(msg.DestinationSteemAccount); err != nil {
		return err
	}
	return ValidateMemo(msg.Memo)
}
