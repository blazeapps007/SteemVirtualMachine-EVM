package keeper

import (
	"fmt"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/address"
	corestore "cosmossdk.io/core/store"
	"github.com/cosmos/cosmos-sdk/codec"

	"steemvm/x/steembridge/types"
)

type Keeper struct {
	storeService corestore.KVStoreService
	cdc          codec.Codec
	addressCodec address.Codec
	// Address capable of executing a MsgUpdateParams message.
	// Typically, this should be the x/gov module account.
	authority []byte

	Schema collections.Schema
	Params collections.Item[types.Params]

	bankKeeper    types.BankKeeper
	stakingKeeper types.StakingKeeper
	DepositSeq    collections.Sequence
	Deposit       collections.Map[uint64, types.Deposit]
	WithdrawalSeq collections.Sequence
	Withdrawal    collections.Map[uint64, types.Withdrawal]

	// DepositByTxid maps the (txid, opIndex) dedup key to the deposit ID.
	DepositByTxid collections.Map[collections.Pair[string, uint32], uint64]
	// DepositConfirmedBy records which validators have already confirmed a
	// given (txid, opIndex), independent of the embedded audit list on the
	// Deposit record itself, so a duplicate-confirmation check is O(1).
	DepositConfirmedBy collections.KeySet[collections.Triple[string, uint32, []byte]]
	// DepositByStatus / WithdrawalByStatus index (status, id) so status-filtered
	// queries (PendingDeposits, MintedDeposits, PendingWithdrawals) and the
	// expiry sweep are ordered prefix scans instead of full-table filters.
	DepositByStatus    collections.KeySet[collections.Pair[int32, uint64]]
	WithdrawalByStatus collections.KeySet[collections.Pair[int32, uint64]]

	// Totals holds cumulative mint/burn statistics for the BridgeStatistics query.
	Totals collections.Item[types.BridgeTotals]

	// FreeDepositCounter backs the ante handler's per-validator, per-block
	// cap on fee-exempt validator attestations (MsgSubmitSteemDeposit and
	// MsgSubmitNameRegistration share this single budget).
	FreeDepositCounter collections.Map[[]byte, types.FreeDepositCounter]

	NameRegistrationSeq collections.Sequence
	NameRegistration    collections.Map[uint64, types.NameRegistration]
	// NameRegistrationByTxid maps the (txid, opIndex) dedup key to the
	// registration ID. This namespace is separate from DepositByTxid: the
	// same Steem op may be attested as both a deposit and a registration.
	NameRegistrationByTxid collections.Map[collections.Pair[string, uint32], uint64]
	// NameRegistrationConfirmedBy records which validators have already
	// attested a given (txid, opIndex) registration key.
	NameRegistrationConfirmedBy collections.KeySet[collections.Triple[string, uint32, []byte]]
	// NameRegistrationByStatus indexes (status, id) for status queries and expiry.
	NameRegistrationByStatus collections.KeySet[collections.Pair[int32, uint64]]
	// NameRegistrationByAccount indexes (steemAccount, id) — permanent history.
	NameRegistrationByAccount collections.KeySet[collections.Pair[string, uint64]]
	// NameRegistrationAwaitingByDest indexes (destination addr, id) for
	// registrations currently awaiting that destination's MsgConfirmName.
	NameRegistrationAwaitingByDest collections.KeySet[collections.Pair[[]byte, uint64]]
	// ActiveName maps steemAccount -> the live NameRecord link.
	ActiveName collections.Map[string, types.NameRecord]
	// ActiveNameByAddress indexes (addr, steemAccount) for reverse lookups.
	ActiveNameByAddress collections.KeySet[collections.Pair[[]byte, string]]
	// FreeNameConfirmCounter backs the ante handler's per-confirmer, per-block
	// cap on fee-exempt MsgConfirmName transactions.
	FreeNameConfirmCounter collections.Map[[]byte, types.FreeDepositCounter]
}

func NewKeeper(
	storeService corestore.KVStoreService,
	cdc codec.Codec,
	addressCodec address.Codec,
	authority []byte,

	bankKeeper types.BankKeeper,
	stakingKeeper types.StakingKeeper,
) Keeper {
	if _, err := addressCodec.BytesToString(authority); err != nil {
		panic(fmt.Sprintf("invalid authority address %s: %s", authority, err))
	}

	sb := collections.NewSchemaBuilder(storeService)

	k := Keeper{
		storeService: storeService,
		cdc:          cdc,
		addressCodec: addressCodec,
		authority:    authority,

		bankKeeper:    bankKeeper,
		stakingKeeper: stakingKeeper,
		Params:        collections.NewItem(sb, types.ParamsKey, "params", codec.CollValue[types.Params](cdc)),
		Deposit:       collections.NewMap(sb, types.DepositKey, "deposit", collections.Uint64Key, codec.CollValue[types.Deposit](cdc)),
		DepositSeq:    collections.NewSequence(sb, types.DepositCountKey, "depositSequence"),
		Withdrawal:    collections.NewMap(sb, types.WithdrawalKey, "withdrawal", collections.Uint64Key, codec.CollValue[types.Withdrawal](cdc)),
		WithdrawalSeq: collections.NewSequence(sb, types.WithdrawalCountKey, "withdrawalSequence"),

		DepositByTxid: collections.NewMap(
			sb, types.DepositByTxidKey, "deposit_by_txid",
			collections.PairKeyCodec(collections.StringKey, collections.Uint32Key),
			collections.Uint64Value,
		),
		DepositConfirmedBy: collections.NewKeySet(
			sb, types.DepositConfirmedByKey, "deposit_confirmed_by",
			collections.TripleKeyCodec(collections.StringKey, collections.Uint32Key, collections.BytesKey),
		),
		DepositByStatus: collections.NewKeySet(
			sb, types.DepositByStatusKey, "deposit_by_status",
			collections.PairKeyCodec(collections.Int32Key, collections.Uint64Key),
		),
		WithdrawalByStatus: collections.NewKeySet(
			sb, types.WithdrawalByStatusKey, "withdrawal_by_status",
			collections.PairKeyCodec(collections.Int32Key, collections.Uint64Key),
		),
		Totals: collections.NewItem(sb, types.TotalsKey, "totals", codec.CollValue[types.BridgeTotals](cdc)),
		FreeDepositCounter: collections.NewMap(
			sb, types.FreeDepositCounterKey, "free_deposit_counter",
			collections.BytesKey, codec.CollValue[types.FreeDepositCounter](cdc),
		),

		NameRegistration:    collections.NewMap(sb, types.NameRegistrationKey, "name_registration", collections.Uint64Key, codec.CollValue[types.NameRegistration](cdc)),
		NameRegistrationSeq: collections.NewSequence(sb, types.NameRegistrationCountKey, "nameRegistrationSequence"),
		NameRegistrationByTxid: collections.NewMap(
			sb, types.NameRegistrationByTxidKey, "name_registration_by_txid",
			collections.PairKeyCodec(collections.StringKey, collections.Uint32Key),
			collections.Uint64Value,
		),
		NameRegistrationConfirmedBy: collections.NewKeySet(
			sb, types.NameRegistrationConfirmedByKey, "name_registration_confirmed_by",
			collections.TripleKeyCodec(collections.StringKey, collections.Uint32Key, collections.BytesKey),
		),
		NameRegistrationByStatus: collections.NewKeySet(
			sb, types.NameRegistrationByStatusKey, "name_registration_by_status",
			collections.PairKeyCodec(collections.Int32Key, collections.Uint64Key),
		),
		NameRegistrationByAccount: collections.NewKeySet(
			sb, types.NameRegistrationByAccountKey, "name_registration_by_account",
			collections.PairKeyCodec(collections.StringKey, collections.Uint64Key),
		),
		NameRegistrationAwaitingByDest: collections.NewKeySet(
			sb, types.NameRegistrationAwaitingByDestKey, "name_registration_awaiting_by_dest",
			collections.PairKeyCodec(collections.BytesKey, collections.Uint64Key),
		),
		ActiveName: collections.NewMap(
			sb, types.ActiveNameKey, "active_name",
			collections.StringKey, codec.CollValue[types.NameRecord](cdc),
		),
		ActiveNameByAddress: collections.NewKeySet(
			sb, types.ActiveNameByAddressKey, "active_name_by_address",
			collections.PairKeyCodec(collections.BytesKey, collections.StringKey),
		),
		FreeNameConfirmCounter: collections.NewMap(
			sb, types.FreeNameConfirmCounterKey, "free_name_confirm_counter",
			collections.BytesKey, codec.CollValue[types.FreeDepositCounter](cdc),
		),
	}

	schema, err := sb.Build()
	if err != nil {
		panic(err)
	}
	k.Schema = schema

	return k
}

// GetAuthority returns the module's authority.
func (k Keeper) GetAuthority() []byte {
	return k.authority
}
