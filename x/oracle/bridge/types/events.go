package types

// Event types emitted by the steembridge module.
const (
	EventTypeDepositCreated              = "deposit_created"
	EventTypeDepositConfirmed            = "deposit_confirmed"
	EventTypeDepositConfirmationMismatch = "deposit_confirmation_mismatch"
	EventTypeDepositMinted               = "deposit_minted"
	EventTypeDepositUnclaimable          = "deposit_unclaimable"
	EventTypeDepositExpired              = "deposit_expired"
	EventTypeWithdrawalCreated           = "withdrawal_created"
	EventTypeWithdrawalBurned            = "withdrawal_burned"
	EventTypeWithdrawalConfirmed         = "withdrawal_confirmed"
	EventTypeWithdrawalPayoutMismatch    = "withdrawal_payout_mismatch"
	EventTypeWithdrawalProcessed         = "withdrawal_processed"
	// EventTypeWithdrawalRefunded is emitted by the EndBlock timeout sweep
	// (Keeper.RefundExpiredWithdrawals) — no validator attestation involved.
	EventTypeWithdrawalRefunded = "withdrawal_refunded"
	// EventTypeWithdrawalPayoutAttestedAfterRefund fires when a
	// MsgAttestWithdrawalPayout arrives for a withdrawal already REFUNDED —
	// a benign no-op for the tx, but a loud audit signal that the payout
	// apparently did land on Steem after all, just after the timeout refund
	// already fired (the double-spend risk the timeout-refund design
	// deliberately accepts).
	EventTypeWithdrawalPayoutAttestedAfterRefund = "withdrawal_payout_attested_after_refund"
	EventTypeParametersUpdated                   = "parameters_updated"

	EventTypeNameRegistrationCreated     = "name_registration_created"
	EventTypeNameRegistrationConfirmed   = "name_registration_confirmed"
	EventTypeNameRegistrationMismatch    = "name_registration_mismatch"
	EventTypeNameRegistrationAwaiting    = "name_registration_awaiting"
	EventTypeNameRegistrationUnclaimable = "name_registration_unclaimable"
	EventTypeNameRegistrationExpired     = "name_registration_expired"
	EventTypeNameLinked                  = "name_linked"
	EventTypeNameSuperseded              = "name_superseded"
)

// Event attribute keys emitted by the steembridge module.
const (
	AttributeKeyDepositID       = "deposit_id"
	AttributeKeyWithdrawalID    = "withdrawal_id"
	AttributeKeyTxid            = "txid"
	AttributeKeyOpIndex         = "op_index"
	AttributeKeyValidator       = "validator"
	AttributeKeyConfirmedRatio  = "confirmed_ratio"
	AttributeKeyDestination     = "destination"
	AttributeKeyDestinationType = "destination_type"
	AttributeKeyAmount          = "amount"
	AttributeKeySteemTxid       = "steem_txid"
	AttributeKeyStoredTxid      = "stored_steem_txid"
	AttributeKeySubmittedTxid   = "submitted_steem_txid"

	AttributeKeyStoredSteemBlock    = "stored_steem_block"
	AttributeKeySubmittedSteemBlock = "submitted_steem_block"
	AttributeKeyStoredTimestamp     = "stored_steem_timestamp"
	AttributeKeySubmittedTimestamp  = "submitted_steem_timestamp"
	AttributeKeyStoredSender        = "stored_steem_sender"
	AttributeKeySubmittedSender     = "submitted_steem_sender"
	AttributeKeyStoredAmount        = "stored_amount_millisteem"
	AttributeKeySubmittedAmount     = "submitted_amount_millisteem"
	AttributeKeyStoredGateway       = "stored_gateway_account"
	AttributeKeySubmittedGateway    = "submitted_gateway_account"
	AttributeKeyStoredMemo          = "stored_memo"
	AttributeKeySubmittedMemo       = "submitted_memo"

	AttributeKeyRegistrationID    = "registration_id"
	AttributeKeySteemAccount      = "steem_account"
	AttributeKeyAddress           = "address"
	AttributeKeyOldRegistrationID = "old_registration_id"
	AttributeKeyOldAddress        = "old_address"
	AttributeKeyStoredAccount     = "stored_steem_account"
	AttributeKeySubmittedAccount  = "submitted_steem_account"
)
