package types

import (
	"strconv"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	errortypes "github.com/cosmos/cosmos-sdk/types/errors"
)

// DefaultBridgeEnabled represents the BridgeEnabled default value.
// Bridging starts disabled: it must be enabled via governance.
var DefaultBridgeEnabled bool = false

// DefaultBridgeOutEnabled represents the BridgeOutEnabled default value.
var DefaultBridgeOutEnabled bool = false

// DefaultGatewayAccount represents the GatewayAccount default value.
//
// Deprecated: this param field is no longer read by consensus logic — the
// gateway account is the hardcoded GatewayAccount constant (keys.go), never
// governance-settable. The field/default are kept only so genesis/query
// output stays informative rather than blank; set to match the constant so
// it never silently disagrees with what's actually enforced.
var DefaultGatewayAccount string = GatewayAccount

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
// roughly 3.5 days at a 6s block time, giving validators ample time to confirm.
var DefaultDepositTimeoutBlocks uint64 = 50400

// DefaultNameServiceEnabled represents the NameServiceEnabled default value.
// Like the bridge, the name service starts disabled and is enabled via governance.
var DefaultNameServiceEnabled bool = false

// DefaultNameRegistrationMinMillisteem represents the NameRegistrationMinMillisteem
// default value: 1 millisteem = 0.001 STEEM, the smallest transferable Steem amount.
var DefaultNameRegistrationMinMillisteem uint64 = 1

// DefaultNamePendingTimeoutBlocks represents the NamePendingTimeoutBlocks default
// value: roughly 3.5 days at a 6s block time, applied separately to the attestation
// phase and the user-confirmation phase.
var DefaultNamePendingTimeoutBlocks uint64 = 50400

// DefaultRelayerStartBlock represents the RelayerStartBlock default value.
// 0 means each validator's relayer anchors to Steem's last irreversible
// block at its own first startup; a real deployment records the launch-time
// LIB in genesis so all validators share the same anchor.
var DefaultRelayerStartBlock uint64 = 0

// DefaultBridgeFeeBps is the default bridge fee: 25 basis points = 0.25%.
var DefaultBridgeFeeBps uint32 = 25

// DefaultWithdrawalTimeoutBlocks represents the WithdrawalTimeoutBlocks
// default value: roughly 3.5 days at a 6s block time — how long a bridge-out
// withdrawal may sit REQUESTED before Keeper.RefundExpiredWithdrawals
// automatically refunds it (no validator attestation required).
var DefaultWithdrawalTimeoutBlocks uint64 = 50400

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
	p := NewParams(
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
	p.BridgeFeeBps = DefaultBridgeFeeBps
	p.WithdrawalTimeoutBlocks = DefaultWithdrawalTimeoutBlocks
	return p
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

	if p.NameServiceEnabled && p.NamePendingTimeoutBlocks == 0 {
		return errorsmod.Wrap(errortypes.ErrInvalidRequest, "name pending timeout blocks must be positive while the name service is enabled")
	}

	if p.BridgeFeeBps > 10000 {
		return errorsmod.Wrap(errortypes.ErrInvalidRequest, "bridge fee bps cannot exceed 10000")
	}

	if p.BridgeOutEnabled && p.WithdrawalTimeoutBlocks == 0 {
		return errorsmod.Wrap(errortypes.ErrInvalidRequest, "withdrawal timeout blocks must be positive while bridge out is enabled")
	}

	return validateSteemAnchor(p.RelayerStartBlock, p.RelayerStartBlockId)
}

// validateSteemAnchor checks the Steem anchor: either no anchor (block 0, no
// ID) or a block number with its 40-hex Steem block ID. A Steem block ID's
// first 4 bytes are the block number (big-endian), so a mistyped number or ID
// is caught here rather than discovered by relayers after launch.
func validateSteemAnchor(block uint64, id string) error {
	if id == "" {
		return nil // no recorded ID: a bare relayer_start_block (or none)
	}
	if block == 0 {
		return errorsmod.Wrap(errortypes.ErrInvalidRequest, "relayer_start_block_id is set but relayer_start_block is 0")
	}
	if len(id) != 40 {
		return errorsmod.Wrapf(errortypes.ErrInvalidRequest, "relayer_start_block_id must be 40 hex chars, got %d", len(id))
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return errorsmod.Wrap(errortypes.ErrInvalidRequest, "relayer_start_block_id must be lowercase hex")
		}
	}
	if num, err := strconv.ParseUint(id[:8], 16, 32); err != nil || num != block {
		return errorsmod.Wrapf(errortypes.ErrInvalidRequest, "relayer_start_block_id %s is not the ID of Steem block %d", id, block)
	}
	return nil
}

// CheckSteemBlock rejects an attestation of a Steem block from before the
// Steem anchor (relayer_start_block): that history predates this chain.
func (p Params) CheckSteemBlock(steemBlock uint64) error {
	if p.RelayerStartBlock > 0 && steemBlock < p.RelayerStartBlock {
		return errorsmod.Wrapf(ErrSteemBlockBeforeAnchor, "steem block %d is before this chain's Steem anchor %d", steemBlock, p.RelayerStartBlock)
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
