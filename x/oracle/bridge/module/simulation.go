package steembridge

import (
	"math/rand"

	"github.com/cosmos/cosmos-sdk/types/module"
	simtypes "github.com/cosmos/cosmos-sdk/types/simulation"
	"github.com/cosmos/cosmos-sdk/x/simulation"

	steembridgesimulation "steemvm/x/oracle/bridge/simulation"
	"steemvm/x/oracle/bridge/types"
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
		opWeightMsgAttestDeposit          = "op_weight_msg_steembridge"
		defaultWeightMsgAttestDeposit int = 100
	)

	var weightMsgAttestDeposit int
	simState.AppParams.GetOrGenerate(opWeightMsgAttestDeposit, &weightMsgAttestDeposit, nil,
		func(_ *rand.Rand) {
			weightMsgAttestDeposit = defaultWeightMsgAttestDeposit
		},
	)
	operations = append(operations, simulation.NewWeightedOperation(
		weightMsgAttestDeposit,
		steembridgesimulation.SimulateMsgAttestDeposit(am.authKeeper, am.bankKeeper, am.keeper, simState.TxConfig),
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
