package app

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	stdante "github.com/cosmos/cosmos-sdk/x/auth/ante"
	sdkvesting "github.com/cosmos/cosmos-sdk/x/auth/vesting/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	evmante "github.com/cosmos/evm/ante"
	cosmosante "github.com/cosmos/evm/ante/cosmos"
	vmante "github.com/cosmos/evm/ante/evm"
	evmtypes "github.com/cosmos/evm/x/vm/types"
	ibcante "github.com/cosmos/ibc-go/v10/modules/core/ante"

	steembridgekeeper "steemvm/x/steembridge/keeper"
	steembridgetypes "steemvm/x/steembridge/types"
)

// newSteembridgeAnteHandler builds the app's top-level ante dispatcher.
//
// cosmos/evm@v0.6.0's ante.HandlerOptions/NewAnteHandler exposes no
// extension point for adding a custom decorator to its Cosmos-tx decorator
// chain (the chain is hardcoded inside the unexported newCosmosAnteHandler
// in cosmos/evm's ante/cosmos.go). This function reproduces that same
// dispatch (route by extension option) and that same Cosmos-tx chain, both
// built entirely from cosmos/evm's and the stock SDK's own exported decorator
// constructors, with one change: MinGasPriceDecorator and DeductFeeDecorator
// (the chain's only two fee-charging decorators) are wrapped by a single
// exemption decorator that waives BOTH for the steembridge module's
// fee-exempt messages (validator attestations and name confirmations — see
// steembridgeFeeExemptionDecorator) — waiving only DeductFeeDecorator
// isn't enough, since MinGasPriceDecorator runs first and independently
// rejects zero-fee txs whenever the feemarket's min_gas_price is non-zero.
// Ethereum and dynamic-fee-extension txs are untouched and delegate to
// cosmos/evm's own handler unchanged, since neither tx type can ever carry
// steembridge messages.
func newSteembridgeAnteHandler(
	options evmante.HandlerOptions,
	bridgeKeeper steembridgekeeper.Keeper,
	stakingKeeper *stakingkeeper.Keeper,
) sdk.AnteHandler {
	defaultHandler := evmante.NewAnteHandler(options)

	return func(ctx sdk.Context, tx sdk.Tx, sim bool) (sdk.Context, error) {
		if txWithExtensions, ok := tx.(stdante.HasExtensionOptionsTx); ok {
			if len(txWithExtensions.GetExtensionOptions()) > 0 {
				return defaultHandler(ctx, tx, sim)
			}
		}

		handler := newSteembridgeCosmosAnteHandler(ctx, options, bridgeKeeper, stakingKeeper)
		return handler(ctx, tx, sim)
	}
}

// newSteembridgeCosmosAnteHandler is a fork of cosmos/evm's unexported
// newCosmosAnteHandler (ante/cosmos.go), built from the same exported
// decorator constructors in the same order, with the two fee-charging
// decorators wrapped by a single exemption decorator. Re-diff this against
// cosmos/evm's ante/cosmos.go on every cosmos/evm version bump.
func newSteembridgeCosmosAnteHandler(
	ctx sdk.Context,
	options evmante.HandlerOptions,
	bridgeKeeper steembridgekeeper.Keeper,
	stakingKeeper *stakingkeeper.Keeper,
) sdk.AnteHandler {
	feemarketParams := options.FeeMarketKeeper.GetParams(ctx)
	var txFeeChecker stdante.TxFeeChecker
	if options.DynamicFeeChecker {
		txFeeChecker = vmante.NewDynamicFeeChecker(&feemarketParams)
	}

	minGasPriceDecorator := cosmosante.NewMinGasPriceDecorator(&feemarketParams)
	consumeGasForTxSizeDecorator := stdante.NewConsumeGasForTxSizeDecorator(options.AccountKeeper)
	deductFeeDecorator := stdante.NewDeductFeeDecorator(options.AccountKeeper, options.BankKeeper, options.FeegrantKeeper, txFeeChecker)

	return sdk.ChainAnteDecorators(
		cosmosante.NewRejectMessagesDecorator(), // reject MsgEthereumTxs
		cosmosante.NewAuthzLimiterDecorator( // disable the Msg types that cannot be included on an authz.MsgExec msgs field
			sdk.MsgTypeURL(&evmtypes.MsgEthereumTx{}),
			sdk.MsgTypeURL(&sdkvesting.MsgCreateVestingAccount{}),
		),
		stdante.NewSetUpContextDecorator(),
		stdante.NewExtensionOptionsDecorator(options.ExtensionOptionChecker),
		stdante.NewValidateBasicDecorator(),
		newSteembridgeValidatorGateDecorator(bridgeKeeper),
		stdante.NewTxTimeoutHeightDecorator(),
		stdante.NewValidateMemoDecorator(options.AccountKeeper),
		newSteembridgeFeeExemptionDecorator(minGasPriceDecorator, consumeGasForTxSizeDecorator, deductFeeDecorator, bridgeKeeper, stakingKeeper),
		// SetPubKeyDecorator must be called before all signature verification decorators
		stdante.NewSetPubKeyDecorator(options.AccountKeeper),
		stdante.NewValidateSigCountDecorator(options.AccountKeeper),
		stdante.NewSigGasConsumeDecorator(options.AccountKeeper, options.SigGasConsumer),
		stdante.NewSigVerificationDecorator(options.AccountKeeper, options.SignModeHandler),
		stdante.NewIncrementSequenceDecorator(options.AccountKeeper),
		ibcante.NewRedundantRelayDecorator(options.IBCKeeper),
		vmante.NewGasWantedDecorator(options.EvmKeeper, options.FeeMarketKeeper, &feemarketParams),
	)
}

// steembridgeFeeExemptionDecorator waives BOTH of the chain's fee-charging
// decorators (MinGasPriceDecorator and DeductFeeDecorator) for a tx whose
// every message is one of the module's three fee-exempt kinds, each
// individually qualifying:
//
//   - MsgSubmitSteemDeposit: signed by a currently bonded validator, would be
//     accepted by the keeper (Keeper.ValidateDepositAcceptance), and within
//     the shared per-validator per-block attestation cap.
//   - MsgSubmitNameRegistration: same rules, sharing the same attestation cap
//     (Keeper.ValidateNameRegistrationAcceptance).
//   - MsgConfirmName: the signer is the derived destination of an actual
//     AWAITING registration (Keeper.ValidateNameConfirmationAcceptance),
//     within the per-confirmer per-block cap, and not a duplicate
//     registration_id within this same tx (two confirms of the same id both
//     pass the read-only check, but the second fails at delivery — it must
//     not ride free).
//
// Mixed txs qualify as long as every message individually qualifies. Any
// failing condition runs both fee decorators normally (in their original
// relative order, with ConsumeTxSizeGasDecorator still running between them
// exactly as upstream), so honest confirmations are free and garbage costs
// gas like anything else.
type steembridgeFeeExemptionDecorator struct {
	minGasPrice         cosmosante.MinGasPriceDecorator
	consumeGasForTxSize stdante.ConsumeTxSizeGasDecorator
	deductFee           stdante.DeductFeeDecorator
	bridgeKeeper        steembridgekeeper.Keeper
	stakingKeeper       *stakingkeeper.Keeper
}

func newSteembridgeFeeExemptionDecorator(
	minGasPrice cosmosante.MinGasPriceDecorator,
	consumeGasForTxSize stdante.ConsumeTxSizeGasDecorator,
	deductFee stdante.DeductFeeDecorator,
	bridgeKeeper steembridgekeeper.Keeper,
	stakingKeeper *stakingkeeper.Keeper,
) steembridgeFeeExemptionDecorator {
	return steembridgeFeeExemptionDecorator{
		minGasPrice:         minGasPrice,
		consumeGasForTxSize: consumeGasForTxSize,
		deductFee:           deductFee,
		bridgeKeeper:        bridgeKeeper,
		stakingKeeper:       stakingKeeper,
	}
}

func (d steembridgeFeeExemptionDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	if d.qualifiesForFeeExemption(ctx, tx) {
		return next(ctx, tx, simulate)
	}
	return d.minGasPrice.AnteHandle(ctx, tx, simulate, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		return d.consumeGasForTxSize.AnteHandle(ctx, tx, simulate, func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
			return d.deductFee.AnteHandle(ctx, tx, simulate, next)
		})
	})
}

func (d steembridgeFeeExemptionDecorator) qualifiesForFeeExemption(ctx sdk.Context, tx sdk.Tx) bool {
	msgs := tx.GetMsgs()
	if len(msgs) == 0 {
		return false
	}

	seenConfirmIDs := make(map[uint64]bool)
	for _, msg := range msgs {
		switch m := msg.(type) {
		case *steembridgetypes.MsgSubmitSteemDeposit:
			if !d.qualifiesAsValidatorAttestation(ctx, m.Validator, func() error {
				return d.bridgeKeeper.ValidateDepositAcceptance(ctx, m)
			}) {
				return false
			}

		case *steembridgetypes.MsgSubmitNameRegistration:
			if !d.qualifiesAsValidatorAttestation(ctx, m.Validator, func() error {
				return d.bridgeKeeper.ValidateNameRegistrationAcceptance(ctx, m)
			}) {
				return false
			}

		case *steembridgetypes.MsgConfirmName:
			if seenConfirmIDs[m.RegistrationId] {
				return false
			}
			seenConfirmIDs[m.RegistrationId] = true

			confirmerAddr, err := sdk.AccAddressFromBech32(m.Confirmer)
			if err != nil {
				return false
			}
			if err := d.bridgeKeeper.ValidateNameConfirmationAcceptance(ctx, m); err != nil {
				return false
			}
			underCap, err := d.bridgeKeeper.ConsumeFreeNameConfirmQuota(ctx, confirmerAddr)
			if err != nil || !underCap {
				return false
			}

		default:
			return false
		}
	}

	return true
}

// qualifiesAsValidatorAttestation applies the shared attestation rules for
// MsgSubmitSteemDeposit and MsgSubmitNameRegistration: signer parses, is a
// currently bonded validator, the message would be accepted by the keeper,
// and the validator's shared per-block free-attestation budget is not
// exhausted.
func (d steembridgeFeeExemptionDecorator) qualifiesAsValidatorAttestation(ctx sdk.Context, validator string, validateAcceptance func() error) bool {
	validatorAddr, err := sdk.AccAddressFromBech32(validator)
	if err != nil {
		return false
	}

	val, err := d.stakingKeeper.GetValidator(ctx, sdk.ValAddress(validatorAddr))
	if err != nil || !val.IsBonded() {
		return false
	}

	if err := validateAcceptance(); err != nil {
		return false
	}

	underCap, err := d.bridgeKeeper.ConsumeFreeDepositQuota(ctx, validatorAddr)
	if err != nil || !underCap {
		return false
	}
	return true
}

// steembridgeValidatorGateDecorator forces every validator to carry a Steem
// identity. It rejects MsgCreateValidator / MsgEditValidator unless the
// validator's staking moniker is a registered, ACTIVE Steem username owned by
// this validator's own account, and its Description.details embeds the three
// Steem public keys (owner=STM...;active=STM...;posting=STM...). The check is
// waived when the name service is disabled. Read-only; it never writes state.
//
// On MsgEditValidator, x/staking's DoNotModifyDesc sentinel means "leave that
// field unchanged"; an edit touching neither moniker nor details can't affect
// the identity and passes, while any edit that rewrites either is validated on
// the effective post-edit values, so the identity can't be stripped. See
// Keeper.ValidateValidatorEdit.
type steembridgeValidatorGateDecorator struct {
	bridgeKeeper steembridgekeeper.Keeper
}

func newSteembridgeValidatorGateDecorator(bridgeKeeper steembridgekeeper.Keeper) steembridgeValidatorGateDecorator {
	return steembridgeValidatorGateDecorator{bridgeKeeper: bridgeKeeper}
}

func (d steembridgeValidatorGateDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	for _, msg := range tx.GetMsgs() {
		switch m := msg.(type) {
		case *stakingtypes.MsgCreateValidator:
			valAddr, err := sdk.ValAddressFromBech32(m.ValidatorAddress)
			if err != nil {
				return ctx, errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid validator address: %s", err)
			}
			if err := d.bridgeKeeper.ValidateValidatorCreationEligibility(ctx, valAddr.Bytes(), m.Description.Moniker, m.Description.Details); err != nil {
				return ctx, errorsmod.Wrap(sdkerrors.ErrUnauthorized, err.Error())
			}
		case *stakingtypes.MsgEditValidator:
			valAddr, err := sdk.ValAddressFromBech32(m.ValidatorAddress)
			if err != nil {
				return ctx, errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid validator address: %s", err)
			}
			if err := d.bridgeKeeper.ValidateValidatorEdit(ctx, valAddr.Bytes(), m.Description.Moniker, m.Description.Details); err != nil {
				return ctx, errorsmod.Wrap(sdkerrors.ErrUnauthorized, err.Error())
			}
		}
	}
	return next(ctx, tx, simulate)
}
