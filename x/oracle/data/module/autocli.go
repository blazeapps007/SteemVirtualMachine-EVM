package data

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"

	"steemvm/x/oracle/data/types"
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
					RpcMethod:      "ExchangeRate",
					Use:            "exchange-rate [pair]",
					Short:          "Query the finalized rate for a pair",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "pair"}},
				},
				{
					RpcMethod:      "ExchangeRates",
					Use:            "exchange-rates",
					Short:          "Query all finalized rates",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod:      "AggregatePrevote",
					Use:            "aggregate-prevote [validator]",
					Short:          "Query a validator's current prevote",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "validator"}},
				},
				{
					RpcMethod:      "AggregateVote",
					Use:            "aggregate-vote [validator]",
					Short:          "Query a validator's current revealed vote",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "validator"}},
				},
			},
		},
		Tx: &autocliv1.ServiceCommandDescriptor{
			Service:              types.Msg_serviceDesc.ServiceName,
			EnhanceCustomCommand: true,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod: "UpdateParams",
					Skip:      true, // skipped because authority gated
				},
				{
					RpcMethod:      "AggregateExchangeRatePrevote",
					Use:            "aggregate-exchange-rate-prevote [validator] [hash]",
					Short:          "Commit an exchange-rate prevote hash (bonded validators only)",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "validator"}, {ProtoField: "hash"}},
				},
				{
					RpcMethod:      "AggregateExchangeRateVote",
					Use:            "aggregate-exchange-rate-vote [validator] [salt] [exchange-rates]",
					Short:          "Reveal an exchange-rate vote (bonded validators only)",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "validator"}, {ProtoField: "salt"}, {ProtoField: "exchange_rates"}},
				},
			},
		},
	}
}
