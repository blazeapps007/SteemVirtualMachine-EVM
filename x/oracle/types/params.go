package types

import (
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	errortypes "github.com/cosmos/cosmos-sdk/types/errors"
)

// Default shared slashing/reward params (plan §2b). At ~6s blocks: SlashWindow
// ~1 day, RewardDistributionWindow ~30 days.
var (
	DefaultSlashWindow              uint64         = 14400
	DefaultSlashFraction            math.LegacyDec = math.LegacyMustNewDecFromStr("0.01")
	DefaultMinValidPerWindow        math.LegacyDec = math.LegacyMustNewDecFromStr("0.50")
	DefaultWPrice                   math.LegacyDec = math.LegacyMustNewDecFromStr("0.5")
	DefaultWBridge                  math.LegacyDec = math.LegacyMustNewDecFromStr("0.5")
	DefaultDutyFloor                math.LegacyDec = math.LegacyZeroDec()
	DefaultRewardDistributionWindow uint64         = 432000
	DefaultBridgeGraceBlocks        uint64         = 100
)

// NewParams creates a new Params instance.
func NewParams(
	slashWindow uint64,
	slashFraction math.LegacyDec,
	minValidPerWindow math.LegacyDec,
	wPrice math.LegacyDec,
	wBridge math.LegacyDec,
	dutyFloor math.LegacyDec,
	rewardDistributionWindow uint64,
	bridgeGraceBlocks uint64,
) Params {
	return Params{
		SlashWindow:              slashWindow,
		SlashFraction:            slashFraction,
		MinValidPerWindow:        minValidPerWindow,
		WPrice:                   wPrice,
		WBridge:                  wBridge,
		DutyFloor:                dutyFloor,
		RewardDistributionWindow: rewardDistributionWindow,
		BridgeGraceBlocks:        bridgeGraceBlocks,
	}
}

// DefaultParams returns a default set of parameters.
func DefaultParams() Params {
	return NewParams(
		DefaultSlashWindow,
		DefaultSlashFraction,
		DefaultMinValidPerWindow,
		DefaultWPrice,
		DefaultWBridge,
		DefaultDutyFloor,
		DefaultRewardDistributionWindow,
		DefaultBridgeGraceBlocks,
	)
}

// Validate validates the set of params.
func (p Params) Validate() error {
	if p.SlashWindow == 0 {
		return errorsmod.Wrap(errortypes.ErrInvalidRequest, "slash_window must be positive")
	}
	if err := validateFraction("slash_fraction", p.SlashFraction, false); err != nil {
		return err
	}
	if err := validateFraction("min_valid_per_window", p.MinValidPerWindow, false); err != nil {
		return err
	}
	if err := validateFraction("duty_floor", p.DutyFloor, false); err != nil {
		return err
	}
	// Weights must be non-negative and not both zero (the keeper falls back to an
	// unweighted mean at zero total weight, but a config of two zeros is a mistake).
	if err := validateNonNegative("w_price", p.WPrice); err != nil {
		return err
	}
	if err := validateNonNegative("w_bridge", p.WBridge); err != nil {
		return err
	}
	if !p.WPrice.IsNil() && !p.WBridge.IsNil() && p.WPrice.Add(p.WBridge).IsZero() {
		return errorsmod.Wrap(errortypes.ErrInvalidRequest, "w_price and w_bridge cannot both be zero")
	}
	if p.RewardDistributionWindow == 0 {
		return errorsmod.Wrap(errortypes.ErrInvalidRequest, "reward_distribution_window must be positive")
	}
	return nil
}

// validateFraction validates a LegacyDec that must lie in [0,1].
func validateFraction(name string, v math.LegacyDec, mustBePositive bool) error {
	if err := validateNonNegative(name, v); err != nil {
		return err
	}
	if mustBePositive && v.IsZero() {
		return errorsmod.Wrapf(errortypes.ErrInvalidRequest, "%s must be positive", name)
	}
	if v.GT(math.LegacyOneDec()) {
		return errorsmod.Wrapf(errortypes.ErrInvalidRequest, "%s cannot exceed 1", name)
	}
	return nil
}

func validateNonNegative(name string, v math.LegacyDec) error {
	if v.IsNil() {
		return errorsmod.Wrapf(errortypes.ErrInvalidRequest, "%s cannot be nil", name)
	}
	if v.IsNegative() {
		return errorsmod.Wrapf(errortypes.ErrInvalidRequest, "%s cannot be negative", name)
	}
	return nil
}
