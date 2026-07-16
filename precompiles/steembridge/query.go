package steembridge

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"

	cmn "github.com/cosmos/evm/precompiles/common"

	"cosmossdk.io/collections"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

const (
	// ResolveNameMethod defines the ABI method name for the resolveName query.
	ResolveNameMethod = "resolveName"
	// NamesOfMethod defines the ABI method name for the namesOf query.
	NamesOfMethod = "namesOf"
	// AwaitingRegistrationIdsMethod defines the ABI method name for the
	// awaitingRegistrationIds query.
	AwaitingRegistrationIdsMethod = "awaitingRegistrationIds"
)

// ResolveName resolves an active Steem account name link. A name without an
// active link deliberately returns zero values instead of reverting (the ENS
// resolver convention): no real link can ever map to address(0), so callers
// probe with a simple `addr != address(0)` check and no try/catch.
func (p Precompile) ResolveName(
	ctx sdk.Context,
	method *abi.Method,
	_ *vm.Contract,
	args []interface{},
) ([]byte, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf(cmn.ErrInvalidNumberOfArgs, 1, len(args))
	}

	steemAccount, ok := args[0].(string)
	if !ok {
		return nil, fmt.Errorf("invalid steem account")
	}

	record, err := p.keeper.ActiveName.Get(ctx, steemAccount)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return method.Outputs.Pack(common.Address{}, uint64(0), uint64(0))
		}
		return nil, err
	}

	addrBytes, err := p.addrCodec.StringToBytes(record.Address)
	if err != nil {
		return nil, err
	}

	return method.Outputs.Pack(common.BytesToAddress(addrBytes), record.RegistrationId, record.LinkedAt)
}

// NamesOf lists the Steem account names actively linked to an address.
func (p Precompile) NamesOf(
	ctx sdk.Context,
	method *abi.Method,
	_ *vm.Contract,
	args []interface{},
) ([]byte, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf(cmn.ErrInvalidNumberOfArgs, 1, len(args))
	}

	owner, ok := args[0].(common.Address)
	if !ok {
		return nil, fmt.Errorf("invalid owner address")
	}

	// Initialize non-nil so an empty result packs as an empty array.
	names := []string{}
	err := p.keeper.ActiveNameByAddress.Walk(
		ctx,
		collections.NewPrefixedPairRange[[]byte, string](owner.Bytes()),
		func(key collections.Pair[[]byte, string]) (bool, error) {
			names = append(names, key.K2())
			return false, nil
		},
	)
	if err != nil {
		return nil, err
	}

	return method.Outputs.Pack(names)
}

// AwaitingRegistrationIds lists the registration ids currently awaiting
// confirmation by a destination address.
func (p Precompile) AwaitingRegistrationIds(
	ctx sdk.Context,
	method *abi.Method,
	_ *vm.Contract,
	args []interface{},
) ([]byte, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf(cmn.ErrInvalidNumberOfArgs, 1, len(args))
	}

	destination, ok := args[0].(common.Address)
	if !ok {
		return nil, fmt.Errorf("invalid destination address")
	}

	ids := []uint64{}
	err := p.keeper.NameRegistrationAwaitingByDest.Walk(
		ctx,
		collections.NewPrefixedPairRange[[]byte, uint64](destination.Bytes()),
		func(key collections.Pair[[]byte, uint64]) (bool, error) {
			ids = append(ids, key.K2())
			return false, nil
		},
	)
	if err != nil {
		return nil, err
	}

	return method.Outputs.Pack(ids)
}
