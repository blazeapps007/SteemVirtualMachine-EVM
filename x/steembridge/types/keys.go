package types

import "cosmossdk.io/collections"

const (
	// ModuleName defines the module name
	ModuleName = "steembridge"

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// GovModuleName duplicates the gov module's name to avoid a dependency with x/gov.
	// It should be synced with the gov module's name if it is ever changed.
	// See: https://github.com/cosmos/cosmos-sdk/blob/v0.52.0-beta.2/x/gov/types/keys.go#L9
	GovModuleName = "gov"
)

// ParamsKey is the prefix to retrieve all Params
var ParamsKey = collections.NewPrefix("p_steembridge")

var (
	DepositKey      = collections.NewPrefix("deposit/value/")
	DepositCountKey = collections.NewPrefix("deposit/count/")
)

var (
	WithdrawalKey      = collections.NewPrefix("withdrawal/value/")
	WithdrawalCountKey = collections.NewPrefix("withdrawal/count/")
)

var (
	// DepositByTxidKey indexes deposit IDs by (txid, opIndex), the dedup key.
	DepositByTxidKey = collections.NewPrefix("deposit/by_txid/")
	// DepositConfirmedByKey indexes (txid, opIndex, validator) confirmations already recorded.
	DepositConfirmedByKey = collections.NewPrefix("deposit/confirmed_by/")
	// DepositByStatusKey indexes (status, depositID) for efficient status-filtered queries.
	DepositByStatusKey = collections.NewPrefix("deposit/by_status/")
	// WithdrawalByStatusKey indexes (status, withdrawalID) for efficient status-filtered queries.
	WithdrawalByStatusKey = collections.NewPrefix("withdrawal/by_status/")
	// TotalsKey holds the cumulative bridge mint/burn statistics.
	TotalsKey = collections.NewPrefix("stats/totals")
	// FreeDepositCounterKey holds the per-validator, per-block free-tx counter
	// used by the ante fee-exemption decorator's DoS cap.
	FreeDepositCounterKey = collections.NewPrefix("ante/free_deposit_counter/")
)

var (
	NameRegistrationKey      = collections.NewPrefix("name/registration/value/")
	NameRegistrationCountKey = collections.NewPrefix("name/registration/count/")
	// NameRegistrationByTxidKey indexes registration IDs by (txid, opIndex) —
	// a dedup namespace deliberately separate from the deposit one: the same
	// Steem op may legitimately be attested as both a deposit and a name
	// registration.
	NameRegistrationByTxidKey = collections.NewPrefix("name/registration/by_txid/")
	// NameRegistrationConfirmedByKey indexes (txid, opIndex, validator) attestations already recorded.
	NameRegistrationConfirmedByKey = collections.NewPrefix("name/registration/confirmed_by/")
	// NameRegistrationByStatusKey indexes (status, registrationID) for status-filtered queries and expiry.
	NameRegistrationByStatusKey = collections.NewPrefix("name/registration/by_status/")
	// NameRegistrationByAccountKey indexes (steemAccount, registrationID) — a
	// permanent history index over every registration ever created for a name.
	NameRegistrationByAccountKey = collections.NewPrefix("name/registration/by_account/")
	// NameRegistrationAwaitingByDestKey indexes (destination addr bytes, registrationID)
	// for registrations currently awaiting that destination's confirmation.
	NameRegistrationAwaitingByDestKey = collections.NewPrefix("name/registration/awaiting_by_dest/")
	// ActiveNameKey maps steemAccount -> NameRecord, the live name links.
	ActiveNameKey = collections.NewPrefix("name/active/by_name/")
	// ActiveNameByAddressKey indexes (addr bytes, steemAccount) for reverse lookups.
	ActiveNameByAddressKey = collections.NewPrefix("name/active/by_address/")
	// FreeNameConfirmCounterKey holds the per-confirmer, per-block free-tx
	// counter for fee-exempt MsgConfirmName transactions.
	FreeNameConfirmCounterKey = collections.NewPrefix("ante/free_confirm_counter/")
)
