package types

import (
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	errortypes "github.com/cosmos/cosmos-sdk/types/errors"
)

// DefaultBridgeEnabled represents the BridgeEnabled default value.
// Bridging starts disabled: it must be enabled (with a gateway_account configured) via governance.
var DefaultBridgeEnabled bool = false

// DefaultBridgeOutEnabled represents the BridgeOutEnabled default value.
var DefaultBridgeOutEnabled bool = false

// DefaultGatewayAccount represents the GatewayAccount default value.
// Empty until governance configures the real Steem gateway account.
var DefaultGatewayAccount string = ""

// DefaultBridgeConfirmationThreshold represents the BridgeConfirmationThreshold default value:
// two-thirds of bonded voting power, matching Cosmos SDK's own supermajority convention.
var DefaultBridgeConfirmationThreshold math.LegacyDec = math.LegacyMustNewDecFromStr("0.666666666666666667")

// DefaultMinimumBridgeAmount represents the MinimumBridgeAmount default value.
// TODO: Determine the default value.
var DefaultMinimumBridgeAmount uint64 = 0

// DefaultMaximumBridgeAmount represents the MaximumBridgeAmount default value.
// TODO: Determine the default value.
var DefaultMaximumBridgeAmount uint64 = 0

// DefaultDepositTimeoutBlocks represents the DepositTimeoutBlocks default value:
// roughly 7 days at a 6s block time, giving validators ample time to confirm.
var DefaultDepositTimeoutBlocks uint64 = 100800

// DefaultNameServiceEnabled represents the NameServiceEnabled default value.
// Like the bridge, the name service starts disabled and is enabled via governance.
var DefaultNameServiceEnabled bool = false

// DefaultNameRegistrationMinMillisteem represents the NameRegistrationMinMillisteem
// default value: 1 millisteem = 0.001 STEEM, the smallest transferable Steem amount.
var DefaultNameRegistrationMinMillisteem uint64 = 1

// DefaultNamePendingTimeoutBlocks represents the NamePendingTimeoutBlocks default
// value: roughly 7 days at a 6s block time, applied separately to the attestation
// phase and the user-confirmation phase.
var DefaultNamePendingTimeoutBlocks uint64 = 100800

// DefaultRelayerStartBlock represents the RelayerStartBlock default value.
// 0 means each validator's relayer anchors to Steem's last irreversible
// block at its own first startup; a real deployment records the launch-time
// LIB in genesis so all validators share the same anchor.
var DefaultRelayerStartBlock uint64 = 0

// NewParams creates a new Params instance.
func NewParams(
	bridgeEnabled bool,
	bridgeOutEnabled bool,
	gatewayAccount string,
	bridgeConfirmationThreshold math.LegacyDec,
	minimumBridgeAmount uint64,
	maximumBridgeAmount uint64,
	depositTimeoutBlocks uint64,
	nameServiceEnabled bool,
	nameRegistrationMinMillisteem uint64,
	namePendingTimeoutBlocks uint64,
	relayerStartBlock uint64,
) Params {
	return Params{
		BridgeEnabled:                 bridgeEnabled,
		BridgeOutEnabled:              bridgeOutEnabled,
		GatewayAccount:                gatewayAccount,
		BridgeConfirmationThreshold:   bridgeConfirmationThreshold,
		MinimumBridgeAmount:           minimumBridgeAmount,
		MaximumBridgeAmount:           maximumBridgeAmount,
		DepositTimeoutBlocks:          depositTimeoutBlocks,
		NameServiceEnabled:            nameServiceEnabled,
		NameRegistrationMinMillisteem: nameRegistrationMinMillisteem,
		NamePendingTimeoutBlocks:      namePendingTimeoutBlocks,
		RelayerStartBlock:             relayerStartBlock,
	}
}

// DefaultParams returns a default set of parameters.
func DefaultParams() Params {
	return NewParams(
		DefaultBridgeEnabled,
		DefaultBridgeOutEnabled,
		DefaultGatewayAccount,
		DefaultBridgeConfirmationThreshold,
		DefaultMinimumBridgeAmount,
		DefaultMaximumBridgeAmount,
		DefaultDepositTimeoutBlocks,
		DefaultNameServiceEnabled,
		DefaultNameRegistrationMinMillisteem,
		DefaultNamePendingTimeoutBlocks,
		DefaultRelayerStartBlock,
	)
}

// Validate validates the set of params.
func (p Params) Validate() error {
	if err := validateBridgeEnabled(p.BridgeEnabled); err != nil {
		return err
	}

	if err := validateBridgeOutEnabled(p.BridgeOutEnabled); err != nil {
		return err
	}

	if err := validateGatewayAccount(p.GatewayAccount); err != nil {
		return err
	}

	if err := validateBridgeConfirmationThreshold(p.BridgeConfirmationThreshold); err != nil {
		return err
	}

	if err := validateMinimumBridgeAmount(p.MinimumBridgeAmount); err != nil {
		return err
	}

	if err := validateMaximumBridgeAmount(p.MaximumBridgeAmount); err != nil {
		return err
	}

	if err := validateDepositTimeoutBlocks(p.DepositTimeoutBlocks); err != nil {
		return err
	}

	if err := validateNameRegistrationMinMillisteem(p.NameRegistrationMinMillisteem); err != nil {
		return err
	}

	if p.MaximumBridgeAmount != 0 && p.MaximumBridgeAmount < p.MinimumBridgeAmount {
		return errorsmod.Wrap(errortypes.ErrInvalidRequest, "maximum bridge amount cannot be less than minimum bridge amount")
	}

	if p.BridgeEnabled && p.GatewayAccount == "" {
		return errorsmod.Wrap(errortypes.ErrInvalidRequest, "gateway account must be set while the bridge is enabled")
	}

	// The name service shares the gateway account with the bridge but is
	// otherwise independent: bridge disabled + name service enabled is a
	// valid configuration.
	if p.NameServiceEnabled && p.GatewayAccount == "" {
		return errorsmod.Wrap(errortypes.ErrInvalidRequest, "gateway account must be set while the name service is enabled")
	}
	if p.NameServiceEnabled && p.NamePendingTimeoutBlocks == 0 {
		return errorsmod.Wrap(errortypes.ErrInvalidRequest, "name pending timeout blocks must be positive while the name service is enabled")
	}

	return nil
}

// validateBridgeEnabled validates the BridgeEnabled parameter.
func validateBridgeEnabled(v bool) error {
	// TODO implement validation
	return nil
}

// validateBridgeOutEnabled validates the BridgeOutEnabled parameter.
func validateBridgeOutEnabled(v bool) error {
	// TODO implement validation
	return nil
}

// validateGatewayAccount validates the GatewayAccount parameter.
// An empty value is allowed (bridge not yet configured); see the cross-field
// check in Validate requiring it to be set whenever BridgeEnabled is true.
func validateGatewayAccount(v string) error {
	if v == "" {
		return nil
	}
	return ValidateSteemAccountName(v)
}

// validateBridgeConfirmationThreshold validates the BridgeConfirmationThreshold parameter.
func validateBridgeConfirmationThreshold(v math.LegacyDec) error {
	if v.IsNil() {
		return errorsmod.Wrap(errortypes.ErrInvalidRequest, "bridge confirmation threshold cannot be nil")
	}
	if v.IsNegative() || v.IsZero() {
		return errorsmod.Wrap(errortypes.ErrInvalidRequest, "bridge confirmation threshold must be positive")
	}
	if v.GT(math.LegacyOneDec()) {
		return errorsmod.Wrap(errortypes.ErrInvalidRequest, "bridge confirmation threshold cannot exceed 1")
	}
	return nil
}

// validateMinimumBridgeAmount validates the MinimumBridgeAmount parameter.
func validateMinimumBridgeAmount(v uint64) error {
	// TODO implement validation
	return nil
}

// validateMaximumBridgeAmount validates the MaximumBridgeAmount parameter.
func validateMaximumBridgeAmount(v uint64) error {
	// TODO implement validation
	return nil
}

// validateDepositTimeoutBlocks validates the DepositTimeoutBlocks parameter.
func validateDepositTimeoutBlocks(v uint64) error {
	// TODO implement validation
	return nil
}

// validateNameRegistrationMinMillisteem validates the NameRegistrationMinMillisteem
// parameter. Zero-amount transfer operations cannot exist on Steem, so a
// minimum below 1 millisteem would be meaningless.
func validateNameRegistrationMinMillisteem(v uint64) error {
	if v == 0 {
		return errorsmod.Wrap(errortypes.ErrInvalidRequest, "name registration minimum must be at least 1 millisteem")
	}
	return nil
}
