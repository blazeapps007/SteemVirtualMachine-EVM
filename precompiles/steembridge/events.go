package steembridge

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"

	cmn "github.com/cosmos/evm/precompiles/common"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

const (
	// EventTypeNameConfirmed defines the event type for a confirmed name link.
	EventTypeNameConfirmed = "NameConfirmed"
	// EventTypeBridgeOutRequested defines the event type for a bridge-out burn.
	EventTypeBridgeOutRequested = "BridgeOutRequested"
)

// EmitNameConfirmedEvent emits the NameConfirmed event.
func (p Precompile) EmitNameConfirmedEvent(
	ctx sdk.Context,
	stateDB vm.StateDB,
	confirmer common.Address,
	registrationID uint64,
	steemAccount string,
) error {
	event := p.Events[EventTypeNameConfirmed]
	topics := make([]common.Hash, 2)

	// The first topic is always the signature of the event
	topics[0] = event.ID

	var err error
	topics[1], err = cmn.MakeTopic(confirmer)
	if err != nil {
		return err
	}

	data, err := event.Inputs.NonIndexed().Pack(registrationID, steemAccount)
	if err != nil {
		return err
	}

	stateDB.AddLog(&ethtypes.Log{
		Address:     p.Address(),
		Topics:      topics,
		Data:        data,
		BlockNumber: uint64(ctx.BlockHeight()), //nolint:gosec // G115
	})

	return nil
}

// EmitBridgeOutRequestedEvent emits the BridgeOutRequested event.
func (p Precompile) EmitBridgeOutRequestedEvent(
	ctx sdk.Context,
	stateDB vm.StateDB,
	sender common.Address,
	destinationSteemAccount string,
	amountAsteem *big.Int,
	memo string,
	withdrawalID uint64,
) error {
	event := p.Events[EventTypeBridgeOutRequested]
	topics := make([]common.Hash, 2)

	// The first topic is always the signature of the event
	topics[0] = event.ID

	var err error
	topics[1], err = cmn.MakeTopic(sender)
	if err != nil {
		return err
	}

	data, err := event.Inputs.NonIndexed().Pack(destinationSteemAccount, amountAsteem, memo, withdrawalID)
	if err != nil {
		return err
	}

	stateDB.AddLog(&ethtypes.Log{
		Address:     p.Address(),
		Topics:      topics,
		Data:        data,
		BlockNumber: uint64(ctx.BlockHeight()), //nolint:gosec // G115
	})

	return nil
}
