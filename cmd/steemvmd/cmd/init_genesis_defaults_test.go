package cmd

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"steemvm/app"
)

func TestPatchFeemarketDefaultsSetsOneGweiFloor(t *testing.T) {
	appState := map[string]json.RawMessage{
		"feemarket": json.RawMessage(`{"params":{"no_base_fee":false,"elasticity_multiplier":2,` +
			`"base_fee":"1000000000.000000000000000000","min_gas_price":"0.000000000000000000"},"block_gas":"0"}`),
	}
	require.NoError(t, patchFeemarketDefaults(appState))

	var fm struct {
		Params   map[string]json.RawMessage `json:"params"`
		BlockGas string                     `json:"block_gas"`
	}
	require.NoError(t, json.Unmarshal(appState["feemarket"], &fm))
	require.JSONEq(t, `"1000000000.000000000000000000"`, string(fm.Params["min_gas_price"]))
	require.JSONEq(t, `"1000000000.000000000000000000"`, string(fm.Params["base_fee"]))
	require.JSONEq(t, `2`, string(fm.Params["elasticity_multiplier"]), "other params untouched")
	require.Equal(t, "0", fm.BlockGas, "non-param fields untouched")
}

func TestPatchEVMActiveStaticPrecompilesActivatesDefaults(t *testing.T) {
	// A fresh genesis gets every default precompile — including the (identity-
	// gated) staking precompile — without duplicating any the base template
	// already lists.
	appState := map[string]json.RawMessage{
		"evm": json.RawMessage(`{"params":{"evm_denom":"asteem","active_static_precompiles":["` +
			app.StakingPrecompileAddress + `"]}}`),
	}
	require.NoError(t, patchEVMActiveStaticPrecompiles(appState))

	var evm struct {
		Params struct {
			EVMDenom string   `json:"evm_denom"`
			Active   []string `json:"active_static_precompiles"`
		} `json:"params"`
	}
	require.NoError(t, json.Unmarshal(appState["evm"], &evm))
	require.Contains(t, evm.Params.Active, app.StakingPrecompileAddress)
	require.ElementsMatch(t, defaultActiveStaticPrecompiles, evm.Params.Active, "no duplicates, nothing missing")
	require.Equal(t, "asteem", evm.Params.EVMDenom)
}
