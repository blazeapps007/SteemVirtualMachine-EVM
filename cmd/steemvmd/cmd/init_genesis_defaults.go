package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/server"

	oracledataprecompile "steemvm/precompiles/oracledata"
	steembridgeprecompile "steemvm/precompiles/steembridge"
)

// defaultActiveStaticPrecompiles is the full set of static EVM precompiles
// this chain activates at genesis: every standard precompile cosmos/evm
// ships (addresses confirmed from the vendored cosmos/evm@v0.7.1 packages'
// own README.md/*.sol interface files, not guessed) plus this repo's two
// custom ones. Core EVM protocol precompiles (ecrecover, sha256, modexp,
// etc.) are NOT listed here — cosmos/evm's IsAvailableStaticPrecompile
// (x/vm/keeper/static_precompiles.go) always allows those regardless of
// this param, so they need no genesis entry.
var defaultActiveStaticPrecompiles = []string{
	"0x0000000000000000000000000000000000000100", // p256
	"0x0000000000000000000000000000000000000400", // bech32
	"0x0000000000000000000000000000000000000800", // staking
	"0x0000000000000000000000000000000000000801", // distribution
	"0x0000000000000000000000000000000000000802", // ics20
	"0x0000000000000000000000000000000000000804", // bank
	"0x0000000000000000000000000000000000000805", // gov
	"0x0000000000000000000000000000000000000806", // slashing
	"0x0000000000000000000000000000000000000807", // ics02
	steembridgeprecompile.PrecompileAddress,      // this chain's steembridge precompile
	oracledataprecompile.PrecompileAddress,       // this chain's oracledata precompile
}

// wrapInitCmdWithChainDefaults makes `steemvmd init`'s output genesis.json
// launch-ready on its own, so the only steps left for a validator are
// account funding and gentx. Without this, a bare `init` produces a generic
// empty-chain template that
// requires several manual jq patches before it will even boot: this session
// hit boot panics/CLI validation errors from exactly the gaps patched here
// (missing asteem denom metadata, steembridge left disabled, EVM precompiles
// left inactive) more than once because those patches are easy to skip or
// run inconsistently by hand.
//
// x/oracle/bridge's own DefaultBridgeEnabled/DefaultBridgeOutEnabled/
// DefaultNameServiceEnabled stay false at the module level deliberately
// ("must be enabled via governance" per params.go) — this wrapper does not
// touch that. It only changes what the steemvmd CLI's init command writes to
// disk for a fresh chain launch, which is a separate, narrower decision than
// the module's library-level default.
func wrapInitCmdWithChainDefaults(initCmd *cobra.Command) {
	originalRunE := initCmd.RunE
	initCmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := originalRunE(cmd, args); err != nil {
			return err
		}
		return applyChainDefaultsToGenesis(cmd)
	}
}

func applyChainDefaultsToGenesis(cmd *cobra.Command) error {
	clientCtx := client.GetClientContextFromCmd(cmd)
	serverCtx := server.GetServerContextFromCmd(cmd)
	config := serverCtx.Config
	config.SetRoot(clientCtx.HomeDir)
	genFile := config.GenesisFile()

	raw, err := os.ReadFile(genFile)
	if err != nil {
		return fmt.Errorf("applying chain defaults to genesis: %w", err)
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("applying chain defaults to genesis: %w", err)
	}

	var appState map[string]json.RawMessage
	if err := json.Unmarshal(doc["app_state"], &appState); err != nil {
		return fmt.Errorf("applying chain defaults to genesis: %w", err)
	}

	if err := patchBankDenomMetadata(appState); err != nil {
		return fmt.Errorf("applying chain defaults to genesis (bank): %w", err)
	}
	if err := patchSteembridgeDefaults(appState); err != nil {
		return fmt.Errorf("applying chain defaults to genesis (steembridge): %w", err)
	}
	if err := patchEVMActiveStaticPrecompiles(appState); err != nil {
		return fmt.Errorf("applying chain defaults to genesis (evm): %w", err)
	}
	if err := patchMintDefaults(appState); err != nil {
		return fmt.Errorf("applying chain defaults to genesis (mint): %w", err)
	}

	newAppState, err := json.Marshal(appState)
	if err != nil {
		return err
	}
	doc["app_state"] = newAppState

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(genFile, out, 0o644)
}

// denomMetadataEntry mirrors banktypes.Metadata's JSON shape closely enough
// for this narrow patch (plain scalars/arrays only, no Any types involved,
// so plain encoding/json is safe here unlike the rest of genesis.json).
type denomMetadataEntry struct {
	Description string `json:"description"`
	DenomUnits  []struct {
		Denom    string   `json:"denom"`
		Exponent int      `json:"exponent"`
		Aliases  []string `json:"aliases"`
	} `json:"denom_units"`
	Base    string `json:"base"`
	Display string `json:"display"`
	Name    string `json:"name"`
	Symbol  string `json:"symbol"`
	URI     string `json:"uri"`
	URIHash string `json:"uri_hash"`
}

func patchBankDenomMetadata(appState map[string]json.RawMessage) error {
	var bank struct {
		Params        json.RawMessage      `json:"params"`
		Balances      json.RawMessage      `json:"balances"`
		Supply        json.RawMessage      `json:"supply"`
		DenomMetadata []denomMetadataEntry `json:"denom_metadata"`
		SendEnabled   json.RawMessage      `json:"send_enabled"`
	}
	if err := json.Unmarshal(appState["bank"], &bank); err != nil {
		return err
	}

	have := make(map[string]bool, len(bank.DenomMetadata))
	for _, m := range bank.DenomMetadata {
		have[m.Base] = true
	}

	if !have["asteem"] {
		bank.DenomMetadata = append(bank.DenomMetadata, denomMetadataEntry{
			Description: "The native staking and gas token of SteemVM, bridged 1:1 from Steem mainchain STEEM.",
			DenomUnits: []struct {
				Denom    string   `json:"denom"`
				Exponent int      `json:"exponent"`
				Aliases  []string `json:"aliases"`
			}{
				{Denom: "asteem", Exponent: 0, Aliases: []string{"attosteem"}},
				{Denom: "steem", Exponent: 18, Aliases: []string{}},
			},
			Base: "asteem", Display: "steem", Name: "Steem", Symbol: "STEEM",
		})
	}
	if !have["asbd"] {
		bank.DenomMetadata = append(bank.DenomMetadata, denomMetadataEntry{
			Description: "Bridged SBD",
			DenomUnits: []struct {
				Denom    string   `json:"denom"`
				Exponent int      `json:"exponent"`
				Aliases  []string `json:"aliases"`
			}{
				{Denom: "asbd", Exponent: 0, Aliases: []string{"attosbd"}},
				{Denom: "sbd", Exponent: 18, Aliases: []string{}},
			},
			Base: "asbd", Display: "sbd", Name: "Steem Backed Dollar", Symbol: "SBD",
		})
	}

	patched, err := json.Marshal(bank)
	if err != nil {
		return err
	}
	appState["bank"] = patched
	return nil
}

func patchSteembridgeDefaults(appState map[string]json.RawMessage) error {
	raw, ok := appState["steembridge"]
	if !ok {
		return nil
	}
	var bridge map[string]json.RawMessage
	if err := json.Unmarshal(raw, &bridge); err != nil {
		return err
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(bridge["params"], &params); err != nil {
		return err
	}

	trueJSON := json.RawMessage("true")
	params["bridge_enabled"] = trueJSON
	params["bridge_out_enabled"] = trueJSON
	params["name_service_enabled"] = trueJSON

	patchedParams, err := json.Marshal(params)
	if err != nil {
		return err
	}
	bridge["params"] = patchedParams

	patched, err := json.Marshal(bridge)
	if err != nil {
		return err
	}
	appState["steembridge"] = patched
	return nil
}

// zeroDec is the JSON string form of a zero-valued LegacyDec (18 fixed
// decimals, matching genesis.json's plain-JSON Dec encoding — never the
// trimmed "0" form).
const zeroDec = "0.000000000000000000"

// patchMintDefaults zeroes out x/mint: this chain is bridged 1:1 from Steem
// mainchain STEEM, there is no inflationary issuance by design (see
// readme.md/CLAUDE.md), but `steemvmd init` otherwise inherits cosmos-sdk's
// plain default mint genesis (13%/20%/7% inflation targeting 67% bonded) —
// nothing previously zeroed this out here, so every fresh chain silently
// launched with real, active inflation despite the documented design.
// Zeroes both the params (the floor/ceiling/rate-of-change BeginBlocker
// clamps to every block) and the initial minter state (so it never reads
// nonzero even for the first block, before BeginBlocker's own clamp runs).
func patchMintDefaults(appState map[string]json.RawMessage) error {
	raw, ok := appState["mint"]
	if !ok {
		return nil
	}
	var mint map[string]json.RawMessage
	if err := json.Unmarshal(raw, &mint); err != nil {
		return err
	}

	zeroJSON := json.RawMessage(`"` + zeroDec + `"`)

	var params map[string]json.RawMessage
	if err := json.Unmarshal(mint["params"], &params); err != nil {
		return err
	}
	params["inflation_rate_change"] = zeroJSON
	params["inflation_max"] = zeroJSON
	params["inflation_min"] = zeroJSON
	patchedParams, err := json.Marshal(params)
	if err != nil {
		return err
	}
	mint["params"] = patchedParams

	var minter map[string]json.RawMessage
	if err := json.Unmarshal(mint["minter"], &minter); err != nil {
		return err
	}
	minter["inflation"] = zeroJSON
	minter["annual_provisions"] = zeroJSON
	patchedMinter, err := json.Marshal(minter)
	if err != nil {
		return err
	}
	mint["minter"] = patchedMinter

	patched, err := json.Marshal(mint)
	if err != nil {
		return err
	}
	appState["mint"] = patched
	return nil
}

func patchEVMActiveStaticPrecompiles(appState map[string]json.RawMessage) error {
	raw, ok := appState["evm"]
	if !ok {
		return nil
	}
	var evm map[string]json.RawMessage
	if err := json.Unmarshal(raw, &evm); err != nil {
		return err
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(evm["params"], &params); err != nil {
		return err
	}

	var active []string
	_ = json.Unmarshal(params["active_static_precompiles"], &active)

	have := make(map[string]bool, len(active))
	for _, a := range active {
		have[a] = true
	}
	for _, addr := range defaultActiveStaticPrecompiles {
		if !have[addr] {
			active = append(active, addr)
		}
	}

	patchedActive, err := json.Marshal(active)
	if err != nil {
		return err
	}
	params["active_static_precompiles"] = patchedActive

	patchedParams, err := json.Marshal(params)
	if err != nil {
		return err
	}
	evm["params"] = patchedParams

	patched, err := json.Marshal(evm)
	if err != nil {
		return err
	}
	appState["evm"] = patched
	return nil
}
