// Package stakinggate enforces the Steem validator identity rule in one place:
// a gated x/staking MsgServer. A validator can't be created or edited on any
// route without passing through it:
//   - every Cosmos message (a direct tx, authz MsgExec, ICA, governance) is
//     dispatched by the baseapp router, which holds only the gated server
//     (see module);
//   - the EVM staking precompile calls its MsgServer directly, so it is built
//     with the gated server too (see NewPrecompile).
//
// It is its own package so its init runs before package app builds its module
// config: Go initializes app's package-level variables before app's own init
// functions, and that config captures whichever staking provider is registered
// at that moment.
package stakinggate

import (
	"context"
	"errors"

	stakingmodulev1 "cosmossdk.io/api/cosmos/staking/module/v1"
	"cosmossdk.io/core/address"
	"cosmossdk.io/depinject/appconfig"
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	sdkmodule "github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/x/staking"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	cmn "github.com/cosmos/evm/precompiles/common"
	stakingprecompile "github.com/cosmos/evm/precompiles/staking"
	gogogrpc "github.com/cosmos/gogoproto/grpc"
	"google.golang.org/grpc"
)

func init() {
	// Replace x/staking's provider, the wrapping pattern documented on
	// appconfig.RegisterModule. This package imports x/staking, so x/staking's
	// own registration has already run and is the one replaced.
	appconfig.RegisterModule(
		&stakingmodulev1.Module{},
		appconfig.Provide(ProvideModule, ProvideGate),
		appconfig.Invoke(staking.InvokeSetStakingHooks),
	)
}

// IdentityChecker is the slice of the steembridge keeper the gate needs.
type IdentityChecker interface {
	ValidateValidatorCreationEligibility(ctx context.Context, operatorValBytes []byte, moniker, details string) error
	ValidateValidatorEdit(ctx context.Context, operatorValBytes []byte, editMoniker, editDetails string) error
}

// Gate holds the identity checker. The steembridge keeper depends on the staking
// keeper, so it can't be injected into the staking provider; it is bound right
// after dependency injection (see app.New). Until then every create and edit is
// refused, so a wiring mistake fails closed.
type Gate struct {
	identity IdentityChecker
}

// ProvideGate gives the staking provider and the app the same gate.
func ProvideGate() *Gate { return &Gate{} }

// Bind sets the identity checker.
func (g *Gate) Bind(identity IdentityChecker) { g.identity = identity }

func (g *Gate) checkCreate(ctx context.Context, msg *stakingtypes.MsgCreateValidator) error {
	if g.identity == nil {
		return errors.New("validator identity gate is not bound")
	}
	valAddr, err := sdk.ValAddressFromBech32(msg.ValidatorAddress)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid validator address: %s", err)
	}
	if err := g.identity.ValidateValidatorCreationEligibility(ctx, valAddr.Bytes(), msg.Description.Moniker, msg.Description.Details); err != nil {
		return errorsmod.Wrap(sdkerrors.ErrUnauthorized, err.Error())
	}
	return nil
}

func (g *Gate) checkEdit(ctx context.Context, msg *stakingtypes.MsgEditValidator) error {
	if g.identity == nil {
		return errors.New("validator identity gate is not bound")
	}
	valAddr, err := sdk.ValAddressFromBech32(msg.ValidatorAddress)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid validator address: %s", err)
	}
	if err := g.identity.ValidateValidatorEdit(ctx, valAddr.Bytes(), msg.Description.Moniker, msg.Description.Details); err != nil {
		return errorsmod.Wrap(sdkerrors.ErrUnauthorized, err.Error())
	}
	return nil
}

// msgServer applies the identity rule before x/staking's own MsgServer. Every
// other staking message passes through untouched.
type msgServer struct {
	stakingtypes.MsgServer
	gate *Gate
}

// NewMsgServer wraps a staking MsgServer with the identity rule.
func NewMsgServer(inner stakingtypes.MsgServer, gate *Gate) stakingtypes.MsgServer {
	return msgServer{MsgServer: inner, gate: gate}
}

func (s msgServer) CreateValidator(ctx context.Context, msg *stakingtypes.MsgCreateValidator) (*stakingtypes.MsgCreateValidatorResponse, error) {
	if err := s.gate.checkCreate(ctx, msg); err != nil {
		return nil, err
	}
	return s.MsgServer.CreateValidator(ctx, msg)
}

func (s msgServer) EditValidator(ctx context.Context, msg *stakingtypes.MsgEditValidator) (*stakingtypes.MsgEditValidatorResponse, error) {
	if err := s.gate.checkEdit(ctx, msg); err != nil {
		return nil, err
	}
	return s.MsgServer.EditValidator(ctx, msg)
}

// msgRegistrar is the Msg server registrar x/staking sees: it swaps the
// MsgServer staking registers for the gated one before it reaches the router.
type msgRegistrar struct {
	gogogrpc.Server
	gate *Gate
}

func (r msgRegistrar) RegisterService(sd *grpc.ServiceDesc, handler any) {
	if srv, ok := handler.(stakingtypes.MsgServer); ok {
		handler = NewMsgServer(srv, r.gate)
	}
	r.Server.RegisterService(sd, handler)
}

// configurator hands x/staking the gated registrar and otherwise behaves like
// the real configurator, so x/staking's query server and migrations register
// exactly as upstream wrote them.
type configurator struct {
	sdkmodule.Configurator
	gate *Gate
}

func (c configurator) MsgServer() gogogrpc.Server {
	return msgRegistrar{Server: c.Configurator.MsgServer(), gate: c.gate}
}

// module is x/staking with its Msg service registered through the gate.
// Everything else is inherited.
type module struct {
	staking.AppModule
	gate *Gate
}

// RegisterServices implements module.HasServices.
func (m module) RegisterServices(cfg sdkmodule.Configurator) {
	m.AppModule.RegisterServices(configurator{Configurator: cfg, gate: m.gate})
}

// ProvideModule builds x/staking as usual and wraps its AppModule.
func ProvideModule(in staking.ModuleInputs, gate *Gate) staking.ModuleOutputs {
	out := staking.ProvideModule(in)
	inner, ok := out.Module.(staking.AppModule)
	if !ok {
		panic("x/staking provided an unexpected module type")
	}
	out.Module = module{AppModule: inner, gate: gate}
	return out
}

// NewPrecompile builds the EVM staking precompile on the gated MsgServer, the
// same way cosmos/evm's defaults build it on the plain one.
func NewPrecompile(
	keeper *stakingkeeper.Keeper,
	bankKeeper cmn.BankKeeper,
	addrCdc address.Codec,
	gate *Gate,
) *stakingprecompile.Precompile {
	return stakingprecompile.NewPrecompile(
		*keeper,
		NewMsgServer(stakingkeeper.NewMsgServerImpl(keeper), gate),
		stakingkeeper.NewQuerier(keeper),
		bankKeeper,
		addrCdc,
	)
}
