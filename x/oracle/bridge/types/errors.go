package types

// DONTCOVER

import (
	goerrors "errors"

	"cosmossdk.io/errors"
)

// x/steembridge module sentinel errors
var (
	ErrInvalidSigner         = errors.Register(ModuleName, 1100, "expected gov account as only signer for proposal message")
	ErrBridgeDisabled        = errors.Register(ModuleName, 1101, "bridge is disabled")
	ErrBridgeOutDisabled     = errors.Register(ModuleName, 1102, "bridge out is disabled")
	ErrInvalidGatewayAccount = errors.Register(ModuleName, 1103, "gateway account does not match module params")
	ErrDepositAlreadyMinted  = errors.Register(ModuleName, 1104, "deposit already minted")
	ErrDepositMismatch       = errors.Register(ModuleName, 1105, "deposit submission does not match pending deposit")
	ErrNotBondedValidator    = errors.Register(ModuleName, 1106, "signer is not a currently bonded validator")
	ErrDuplicateConfirmation = errors.Register(ModuleName, 1107, "validator has already confirmed this deposit")
	ErrInvalidSteemAccount   = errors.Register(ModuleName, 1108, "invalid steem account name")
	ErrInvalidAmount         = errors.Register(ModuleName, 1109, "invalid amount")
	ErrTooManyFreeDeposits   = errors.Register(ModuleName, 1110, "too many free deposit confirmations submitted by this validator this block")

	ErrNameServiceDisabled             = errors.Register(ModuleName, 1111, "name service is disabled")
	ErrRegistrationAlreadyResolved     = errors.Register(ModuleName, 1112, "name registration already resolved")
	ErrRegistrationMismatch            = errors.Register(ModuleName, 1113, "registration submission does not match pending registration")
	ErrRegistrationNotFound            = errors.Register(ModuleName, 1114, "name registration not found")
	ErrRegistrationNotAwaiting         = errors.Register(ModuleName, 1115, "name registration is not awaiting confirmation")
	ErrConfirmerNotDestination         = errors.Register(ModuleName, 1116, "signer is not the registration's derived destination")
	ErrRegistrationBelowMinimum        = errors.Register(ModuleName, 1117, "registration amount below the name registration minimum")
	ErrTooManyFreeConfirmations        = errors.Register(ModuleName, 1118, "too many free name confirmations from this address this block")
	ErrWithdrawalNotFound              = errors.Register(ModuleName, 1119, "withdrawal not found")
	ErrWithdrawalAlreadyProcessed      = errors.Register(ModuleName, 1120, "withdrawal already processed")
	ErrDuplicateWithdrawalConfirmation = errors.Register(ModuleName, 1121, "validator has already attested this withdrawal payout")
	ErrWithdrawalPayoutMismatch        = errors.Register(ModuleName, 1122, "withdrawal payout attestation does not match the recorded payout facts")
	ErrWithdrawalAlreadyRefunded       = errors.Register(ModuleName, 1123, "withdrawal already refunded (timeout expired before payout was attested)")
)

// IsBenignAttestationError reports whether an acceptance error represents a
// harmless, redundant attestation from a bonded validator rather than a real
// rejection — namely a deposit/registration that another validator's
// attestation already resolved (typically in this same block), or a duplicate
// confirmation of a key this validator already confirmed. These are the
// natural outcome of independent validators racing to attest the same Steem
// transfer, so they are treated as benign no-op successes (not failed txs) and
// still qualify for the validator fee exemption.
//
// A fact MISMATCH is deliberately NOT benign: an honest relayer never produces
// one, and keeping mismatches fee-paying preserves the cost of poisoning a
// pending deposit with wrong facts.
func IsBenignAttestationError(err error) bool {
	if err == nil {
		return false
	}
	return goerrors.Is(err, ErrDepositAlreadyMinted) ||
		goerrors.Is(err, ErrRegistrationAlreadyResolved) ||
		goerrors.Is(err, ErrDuplicateConfirmation) ||
		goerrors.Is(err, ErrWithdrawalAlreadyProcessed) ||
		goerrors.Is(err, ErrWithdrawalAlreadyRefunded) ||
		goerrors.Is(err, ErrDuplicateWithdrawalConfirmation)
}
