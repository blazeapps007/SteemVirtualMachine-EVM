package steembridge

import (
	"math/rand"

	"github.com/cosmos/cosmos-sdk/types/module"
	simtypes "github.com/cosmos/cosmos-sdk/types/simulation"
	"github.com/cosmos/cosmos-sdk/x/simulation"

	steembridgesimulation "steemvm/x/steembridge/simulation"
	"steemvm/x/steembridge/types"
)

// GenerateGenesisState creates a randomized GenState of the module.
func (AppModule) GenerateGenesisState(simState *module.SimulationState) {
	accs := make([]string, len(simState.Accounts))
	for i, acc := range simState.Accounts {
		accs[i] = acc.Address.String()
	}
	steembridgeGenesis := types.GenesisState{
		Params: types.DefaultParams(),
	}
	simState.GenState[types.ModuleName] = simState.Cdc.MustMarshalJSON(&steembridgeGenesis)
}

// RegisterStoreDecoder registers a decoder.
func (am AppModule) RegisterStoreDecoder(_ simtypes.StoreDecoderRegistry) {}

// WeightedOperations returns the all the gov module operations with their respective weights.
func (am AppModule) WeightedOperations(simState module.SimulationState) []simtypes.WeightedOperation {
	operations := make([]simtypes.WeightedOperation, 0)
	const (
		opWeightMsgSubmitSteemDeposit          = "op_weight_msg_steembridge"
		defaultWeightMsgSubmitSteemDeposit int = 100
	)

	var weightMsgSubmitSteemDeposit int
	simState.AppParams.GetOrGenerate(opWeightMsgSubmitSteemDeposit, &weightMsgSubmitSteemDeposit, nil,
		func(_ *rand.Rand) {
			weightMsgSubmitSteemDeposit = defaultWeightMsgSubmitSteemDeposit
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgSubmitSteemDeposit,
		steembridgesimulation.SimulateMsgSubmitSteemDeposit(am.authKeeper, am.bankKeeper, am.keeper, simState.TxConfig),
	))
	const (
		opWeightMsgBridgeOut          = "op_weight_msg_steembridge"
		defaultWeightMsgBridgeOut int = 100
	)

	var weightMsgBridgeOut int
	simState.AppParams.GetOrGenerate(opWeightMsgBridgeOut, &weightMsgBridgeOut, nil,
		func(_ *rand.Rand) {
			weightMsgBridgeOut = defaultWeightMsgBridgeOut
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgBridgeOut,
		steembridgesimulation.SimulateMsgBridgeOut(am.authKeeper, am.bankKeeper, am.keeper, simState.TxConfig),
	))

	return operations
}

// ProposalMsgs returns msgs used for governance proposals for simulations.
func (am AppModule) ProposalMsgs(simState module.SimulationState) []simtypes.WeightedProposalMsg {
	return []simtypes.WeightedProposalMsg{}
}
