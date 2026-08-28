package steembridge

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"

	_ "embed"

	cmn "github.com/cosmos/evm/precompiles/common"

	"cosmossdk.io/core/address"
	"cosmossdk.io/log/v2"
	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"

	sdk "github.com/cosmos/cosmos-sdk/types"

	steembridgekeeper "steemvm/x/oracle/bridge/keeper"
	steembridgetypes "steemvm/x/oracle/bridge/types"
)

// PrecompileAddress is the fixed EVM address of the steembridge precompile.
// 0x0900 sits clear of cosmos/evm's own precompiles (0x0100 p256, 0x0400
// bech32, 0x0800-0x0806 module precompiles).
const PrecompileAddress = "0x0000000000000000000000000000000000000900"

var _ vm.PrecompiledContract = &Precompile{}

var (
	// Embed abi json file to the executable binary. Needed when importing as dependency.
	//
	//go:embed abi.json
	f   []byte
	ABI abi.ABI
)

func init() {
	// abi.json is a standard Hardhat-artifact JSON object (contractName,
	// sourceName, abi, bytecode, ...), not a bare ABI array — extract the
	// "abi" field before handing it to go-ethereum's abi.JSON, which only
	// accepts the bare array form. cosmos/evm's own cmn.LoadABI helper did
	// this transparently pre-v0.7.0; it was removed upstream, so this repo
	// now does the unwrap itself.
	var artifact struct {
		ABI json.RawMessage `json:"abi"`
	}
	if err := json.Unmarshal(f, &artifact); err != nil {
		panic(err)
	}

	var err error
	ABI, err = abi.JSON(bytes.NewReader(artifact.ABI))
	if err != nil {
		panic(err)
	}
}

// Precompile defines the precompiled contract for the x/steembridge module.
type Precompile struct {
	cmn.Precompile

	abi.ABI
	keeper    steembridgekeeper.Keeper
	msgServer steembridgetypes.MsgServer
	addrCodec address.Codec
}

// NewPrecompile creates a new steembridge Precompile instance as a
// PrecompiledContract interface. The bank keeper is only used by the shared
// balance handler, which replays bank events (bridgeOut's burn) into the EVM
// stateDB so the caller's native balance stays consistent.
//
// This means BridgeOut debits the caller's balance through the same shared
// cmn.BalanceHandler -> StateDB.SubBalance path as every other
// balance-touching precompile (staking, distribution, bank, ics20, gov) — the
// exact code path that had a wraparound-on-underflow bug fixed in cosmos/evm
// v0.7.2 (already used to drain balances on other chains via a crafted
// delegate call). Worth knowing if this precompile is ever extended.
func NewPrecompile(
	keeper steembridgekeeper.Keeper,
	msgServer steembridgetypes.MsgServer,
	bankKeeper cmn.BankKeeper,
	addrCodec address.Codec,
) *Precompile {
	return &Precompile{
		Precompile: cmn.Precompile{
			KvGasConfig:           storetypes.KVGasConfig(),
			TransientKVGasConfig:  storetypes.TransientGasConfig(),
			ContractAddress:       common.HexToAddress(PrecompileAddress),
			BalanceHandlerFactory: cmn.NewBalanceHandlerFactory(bankKeeper),
		},
		ABI:       ABI,
		keeper:    keeper,
		msgServer: msgServer,
		addrCodec: addrCodec,
	}
}

// Name returns the precompile's name, used to identify it in the EVM.
func (p Precompile) Name() string {
	return "steembridge"
}

// RequiredGas calculates the precompiled contract's base gas rate.
func (p Precompile) RequiredGas(input []byte) uint64 {
	// NOTE: This check avoid panicking when trying to decode the method ID
	if len(input) < 4 {
		return 0
	}
	methodID := input[:4]

	method, err := p.MethodById(methodID)
	if err != nil {
		// This should never happen since this method is going to fail during Run
		return 0
	}

	return p.Precompile.RequiredGas(input, p.IsTransaction(method))
}

func (p Precompile) Run(evm *vm.EVM, contract *vm.Contract, readonly bool) ([]byte, error) {
	return p.RunNativeAction(evm, contract, func(ctx sdk.Context) ([]byte, error) {
		return p.Execute(ctx, evm.StateDB, contract, readonly)
	})
}

func (p Precompile) Execute(ctx sdk.Context, stateDB vm.StateDB, contract *vm.Contract, readOnly bool) ([]byte, error) {
	method, args, err := cmn.SetupABI(p.ABI, contract, readOnly, p.IsTransaction)
	if err != nil {
		return nil, err
	}

	var bz []byte

	switch method.Name {
	// steembridge transactions
	case ConfirmNameMethod:
		bz, err = p.ConfirmName(ctx, method, stateDB, contract, args)
	case BridgeOutMethod:
		bz, err = p.BridgeOut(ctx, method, stateDB, contract, args)
	// steembridge queries
	case ResolveNameMethod:
		bz, err = p.ResolveName(ctx, method, contract, args)
	case NamesOfMethod:
		bz, err = p.NamesOf(ctx, method, contract, args)
	case AwaitingRegistrationIdsMethod:
		bz, err = p.AwaitingRegistrationIds(ctx, method, contract, args)
	default:
		return nil, fmt.Errorf(cmn.ErrUnknownMethod, method.Name)
	}

	return bz, err
}

// IsTransaction checks if the given method name corresponds to a transaction or query.
//
// Available steembridge transactions are:
// - ConfirmName
// - BridgeOut
func (Precompile) IsTransaction(method *abi.Method) bool {
	switch method.Name {
	case ConfirmNameMethod, BridgeOutMethod:
		return true
	default:
		return false
	}
}

// Logger returns a precompile-specific logger.
func (p Precompile) Logger(ctx sdk.Context) log.Logger {
	return ctx.Logger().With("evm extension", "steembridge")
}
