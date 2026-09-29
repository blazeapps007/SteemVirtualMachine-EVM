package app

import (
	"context"

	"cosmossdk.io/core/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
	cmn "github.com/cosmos/evm/precompiles/common"
	stakingprecompile "github.com/cosmos/evm/precompiles/staking"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/core/vm"
)

// stakingIdentityPrecompile is cosmos/evm's staking precompile (0x…0800) with
// the Steem validator-identity gate added.
//
// Upstream's createValidator/editValidator call x/staking's msg server
// directly, and EVM txs never pass through the Cosmos ante chain, so on its own
// the precompile let any EVM account register an anonymous validator or strip
// a validator's identity. This wrapper runs the exact same check the ante gate
// and the ICA router apply (checkValidatorIdentityMsg) before those two
// methods, and changes nothing else: delegate/undelegate/redelegate/queries go
// straight to upstream.
//
// Run is upstream's own Run with the check inserted: RunNativeAction(Execute),
// so journaling, gas and balance handling are identical, and the check reads
// the same state context the message will execute against.
type stakingIdentityPrecompile struct {
	*stakingprecompile.Precompile
	identity  validatorIdentityChecker
	bondDenom func(ctx context.Context) (string, error)
	// addrCdc must match what upstream constructed the precompile with (its own
	// field is private). It only renders the delegator address; the validator
	// address and description the gate reads don't depend on it.
	addrCdc address.Codec
}

func newStakingIdentityPrecompile(
	inner *stakingprecompile.Precompile,
	identity validatorIdentityChecker,
	bondDenom func(ctx context.Context) (string, error),
	addrCdc address.Codec,
) stakingIdentityPrecompile {
	return stakingIdentityPrecompile{Precompile: inner, identity: identity, bondDenom: bondDenom, addrCdc: addrCdc}
}

func (p stakingIdentityPrecompile) Run(evm *vm.EVM, contract *vm.Contract, readonly bool) ([]byte, error) {
	return p.RunNativeAction(evm, contract, func(ctx sdk.Context) ([]byte, error) {
		// Decode exactly as Execute will. If this fails, Execute fails the same
		// way, so fall through and let it report its own error.
		method, args, err := cmn.SetupABI(p.ABI, contract, readonly, p.IsTransaction)
		if err == nil {
			if err := p.checkValidatorIdentity(ctx, method, args); err != nil {
				return nil, err
			}
		}
		return p.Execute(ctx, evm.StateDB, contract, readonly)
	})
}

// checkValidatorIdentity builds the same x/staking message upstream's Execute
// will build from these args and runs the shared identity check on it. Any
// method other than createValidator/editValidator passes untouched.
func (p stakingIdentityPrecompile) checkValidatorIdentity(ctx sdk.Context, method *abi.Method, args []interface{}) error {
	var msg sdk.Msg
	switch method.Name {
	case stakingprecompile.CreateValidatorMethod:
		denom, err := p.bondDenom(ctx)
		if err != nil {
			return err
		}
		m, _, err := stakingprecompile.NewMsgCreateValidator(args, denom, p.addrCdc)
		if err != nil {
			return nil // malformed args: Execute rejects them with the same error
		}
		msg = m
	case stakingprecompile.EditValidatorMethod:
		m, _, err := stakingprecompile.NewMsgEditValidator(args)
		if err != nil {
			return nil
		}
		msg = m
	default:
		return nil
	}
	return checkValidatorIdentityMsg(ctx, p.identity, msg)
}
