package types

// ValidateBasic performs stateless sanity checks on a name confirmation.
// There is nothing to validate beyond the signer address (checked by the
// SDK): registration_id 0 is a valid id, since the registration sequence
// starts at 0. All real checks are stateful and live in the keeper's
// ValidateNameConfirmationAcceptance.
func (msg *MsgConfirmName) ValidateBasic() error {
	return nil
}
