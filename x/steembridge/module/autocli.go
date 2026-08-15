package steembridge

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"

	"steemvm/x/steembridge/types"
)

// AutoCLIOptions implements the autocli.HasAutoCLIConfig interface.
func (am AppModule) AutoCLIOptions() *autocliv1.ModuleOptions {
	return &autocliv1.ModuleOptions{
		Query: &autocliv1.ServiceCommandDescriptor{
			Service: types.Query_serviceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod: "Params",
					Use:       "params",
					Short:     "Shows the parameters of the module",
				},
				{
					RpcMethod: "ListDeposit",
					Use:       "list-deposit",
					Short:     "List all deposit",
				},
				{
					RpcMethod:      "GetDeposit",
					Use:            "get-deposit [id]",
					Short:          "Gets a deposit by id",
					Alias:          []string{"show-deposit"},
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "id"}},
				},
				{
					RpcMethod: "ListWithdrawal",
					Use:       "list-withdrawal",
					Short:     "List all withdrawal",
				},
				{
					RpcMethod:      "GetWithdrawal",
					Use:            "get-withdrawal [id]",
					Short:          "Gets a withdrawal by id",
					Alias:          []string{"show-withdrawal"},
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "id"}},
				},
				{
					RpcMethod:      "PendingDeposits",
					Use:            "pending-deposits ",
					Short:          "Query pending-deposits",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod:      "MintedDeposits",
					Use:            "minted-deposits ",
					Short:          "Query minted-deposits",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod:      "RequestedWithdrawals",
					Use:            "requested-withdrawals",
					Short:          "Query bridge-out request records (still to pay)",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod:      "ProcessedWithdrawals",
					Use:            "processed-withdrawals",
					Short:          "Query withdrawals paid out on Steem (PROCESSED)",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod:      "DepositByTxid",
					Use:            "deposit-by-txid [txid] [op-index]",
					Short:          "Query deposit-by-txid",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "txid"}, {ProtoField: "op_index"}},
				},
				{
					RpcMethod:      "BridgeStatistics",
					Use:            "bridge-statistics ",
					Short:          "Query bridge-statistics",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod:      "ResolveName",
					Use:            "resolve-name [steem-account]",
					Short:          "Resolve an active Steem account name link to its address",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "steem_account"}},
				},
				{
					RpcMethod:      "NamesByAddress",
					Use:            "names-by-address [address]",
					Short:          "List active name links owned by an address",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "address"}},
				},
				{
					RpcMethod:      "NameRegistration",
					Use:            "name-registration [id]",
					Short:          "Get a name registration by id",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "id"}},
				},
				{
					RpcMethod:      "NameRegistrationByTxid",
					Use:            "name-registration-by-txid [txid] [op-index]",
					Short:          "Get a name registration by its Steem (txid, op-index) key",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "txid"}, {ProtoField: "op_index"}},
				},
				{
					RpcMethod:      "NameRegistrationsByAccount",
					Use:            "name-registrations-by-account [steem-account]",
					Short:          "List all registrations ever submitted for a Steem account",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "steem_account"}},
				},
				{
					RpcMethod:      "PendingNameRegistrations",
					Use:            "pending-name-registrations",
					Short:          "List registrations still accumulating validator attestations",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod:      "AwaitingNameRegistrations",
					Use:            "awaiting-name-registrations",
					Short:          "List registrations awaiting the destination's confirmation",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod:      "AwaitingNameRegistrationsByDestination",
					Use:            "awaiting-name-registrations-by-destination [address]",
					Short:          "List registrations a given address can confirm",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "address"}},
				},
				{
					RpcMethod:      "ValidatorIdentity",
					Use:            "validator-identity [validator-address]",
					Short:          "Show the Steem identity (username + owner/active/posting keys) embedded in a validator's Description",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "validator_address"}},
				},
			},
		},
		Tx: &autocliv1.ServiceCommandDescriptor{
			Service:              types.Msg_serviceDesc.ServiceName,
			EnhanceCustomCommand: true, // only required if you want to use the custom command
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod: "UpdateParams",
					Skip:      true, // skipped because authority gated
				},
				{
					RpcMethod:      "AttestDeposit",
					Use:            "attest-deposit [txid] [op-index] [steem-block] [steem-timestamp] [steem-sender] [gateway-account] [amount-millisteem] [memo] [asset]",
					Short:          "Attest a Steem gateway deposit (bonded validators only)",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "txid"}, {ProtoField: "op_index"}, {ProtoField: "steem_block"}, {ProtoField: "steem_timestamp"}, {ProtoField: "steem_sender"}, {ProtoField: "gateway_account"}, {ProtoField: "amount_millisteem"}, {ProtoField: "memo"}, {ProtoField: "asset"}},
				},
				{
					RpcMethod:      "BridgeOut",
					Use:            "bridge-out [destination-steem-account] [amount-asteem] [memo] [asset]",
					Short:          "Send a bridge-out tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "destination_steem_account"}, {ProtoField: "amount_asteem"}, {ProtoField: "memo"}, {ProtoField: "asset"}},
				},
				{
					RpcMethod:      "AttestWithdrawalPayout",
					Use:            "attest-withdrawal-payout [withdrawal-id] [steem-txid] [op-index] [steem-block] [steem-timestamp]",
					Short:          "Attest a Steem bridge-out payout (bonded validators only)",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "withdrawal_id"}, {ProtoField: "steem_txid"}, {ProtoField: "op_index"}, {ProtoField: "steem_block"}, {ProtoField: "steem_timestamp"}},
				},
				{
					RpcMethod:      "SubmitNameRegistration",
					Use:            "submit-name-registration [txid] [op-index] [steem-block] [steem-timestamp] [steem-account] [gateway-account] [amount-millisteem] [memo]",
					Short:          "Attest a Steem name-registration transfer (bonded validators only)",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "txid"}, {ProtoField: "op_index"}, {ProtoField: "steem_block"}, {ProtoField: "steem_timestamp"}, {ProtoField: "steem_account"}, {ProtoField: "gateway_account"}, {ProtoField: "amount_millisteem"}, {ProtoField: "memo"}},
				},
				{
					RpcMethod:      "ConfirmName",
					Use:            "confirm-name [registration-id]",
					Short:          "Confirm a name registration as its derived destination address",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "registration_id"}},
				},
			},
		},
	}
}
