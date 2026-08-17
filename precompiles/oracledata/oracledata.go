package oracledata

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"

	_ "embed"

	cmn "github.com/cosmos/evm/precompiles/common"

	"cosmossdk.io/log/v2"
	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"

	sdk "github.com/cosmos/cosmos-sdk/types"

	oracledatakeeper "steemvm/x/oracle/data/keeper"
)

// PrecompileAddress is the fixed EVM address of the oracle price-feed precompile.
// 0x0902 sits clear of cosmos/evm's own precompiles (0x0100 p256, 0x0400 bech32,
// 0x0800-0x0806 module precompiles), the steembridge precompile (0x0900), and the
// SBD ERC20 dynamic precompile (0x0901).
const PrecompileAddress = "0x0000000000000000000000000000000000000902"

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

// Precompile is the read-only EVM extension over x/oracle/data. It exposes the
// finalized, validator-attested exchange rates to EVM consumers. Every method is
// a query — there are no state-changing methods, so it takes no msg server and no
// bank keeper (nothing to replay into the stateDB).
type Precompile struct {
	cmn.Precompile

	abi.ABI
	keeper oracledatakeeper.Keeper
}

// NewPrecompile creates a new oracledata Precompile instance as a
// PrecompiledContract interface.
func NewPrecompile(keeper oracledatakeeper.Keeper) *Precompile {
	return &Precompile{
		Precompile: cmn.Precompile{
			KvGasConfig:          storetypes.KVGasConfig(),
			TransientKVGasConfig: storetypes.TransientGasConfig(),
			ContractAddress:      common.HexToAddress(PrecompileAddress),
		},
		ABI:    ABI,
		keeper: keeper,
	}
}

// Name returns the precompile's name, used to identify it in the EVM.
func (p Precompile) Name() string {
	return "oracledata"
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

func (p Precompile) Execute(ctx sdk.Context, _ vm.StateDB, contract *vm.Contract, readOnly bool) ([]byte, error) {
	method, args, err := cmn.SetupABI(p.ABI, contract, readOnly, p.IsTransaction)
	if err != nil {
		return nil, err
	}

	var bz []byte

	switch method.Name {
	case GetPriceMethod:
		bz, err = p.GetPrice(ctx, method, contract, args)
	case GetPricesMethod:
		bz, err = p.GetPrices(ctx, method, contract, args)
	default:
		return nil, fmt.Errorf(cmn.ErrUnknownMethod, method.Name)
	}

	return bz, err
}

// IsTransaction reports whether the method changes state. The oracle price
// precompile is read-only, so every method is a query.
func (Precompile) IsTransaction(*abi.Method) bool {
	return false
}

// Logger returns a precompile-specific logger.
func (p Precompile) Logger(ctx sdk.Context) log.Logger {
	return ctx.Logger().With("evm extension", "oracledata")
}
