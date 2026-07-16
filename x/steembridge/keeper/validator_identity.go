package keeper

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"steemvm/x/steembridge/types"
)

// ValidateValidatorCreationEligibility is the single source of truth for whether
// an account may create or keep a validator. It is read-only and shared by the
// ante gate (app/ante_steembridge.go) for both MsgCreateValidator and
// MsgEditValidator, mirroring the ValidateDepositAcceptance pattern.
//
// operatorValBytes is the validator operator address' 20 bytes, which are the
// SAME bytes as the operator's account address (valoper and account differ only
// in bech32 prefix). moniker is the validator's staking Description.moniker (the
// Steem username) and details is Description.details (the three Steem keys).
//
// When the name service is disabled the check is a no-op (the whole feature is
// inert). Otherwise moniker+details must parse into a valid ValidatorIdentity
// whose username is registered, ACTIVE, and owned by this exact account.
func (k Keeper) ValidateValidatorCreationEligibility(ctx context.Context, operatorValBytes []byte, moniker, details string) error {
	params, err := k.Params.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			// steembridge genesis has not been applied yet. This path is hit when
			// genesis gentxs execute through the ante during InitChain, before
			// this module's InitGenesis runs (genutil InitGenesis is ordered
			// ahead of steembridge). Genesis validators are trusted — their
			// identity is seeded in genesis — so there is nothing to enforce.
			return nil
		}
		return err
	}
	if !params.NameServiceEnabled {
		return nil
	}

	identity, err := types.ParseValidatorIdentity(moniker, details)
	if err != nil {
		return fmt.Errorf("validator must set moniker to its Steem username and embed its keys in "+
			"Description.details (owner=STM...;active=STM...;posting=STM...): %w", err)
	}

	record, err := k.ActiveName.Get(ctx, identity.SteemUsername)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return fmt.Errorf("steem username %q (validator moniker) has no active name-service registration; "+
				"register and confirm the name before creating a validator", identity.SteemUsername)
		}
		return err
	}

	linkedBytes, err := k.addressCodec.StringToBytes(record.Address)
	if err != nil {
		return err
	}
	if !bytes.Equal(linkedBytes, operatorValBytes) {
		return fmt.Errorf("steem username %q (validator moniker) is registered to a different account than this validator operator",
			identity.SteemUsername)
	}
	return nil
}

// ValidateValidatorEdit is the MsgEditValidator counterpart. x/staking's
// DoNotModifyDesc sentinel in a Description field means "leave it unchanged", so
// an edit that touches neither the moniker nor the details cannot affect the
// identity and is allowed through. Otherwise the effective post-edit moniker and
// details (current value substituted for any sentinel field) are validated, so
// an edit can never strip or invalidate the identity.
func (k Keeper) ValidateValidatorEdit(ctx context.Context, operatorValBytes []byte, editMoniker, editDetails string) error {
	monikerUnchanged := editMoniker == stakingtypes.DoNotModifyDesc
	detailsUnchanged := editDetails == stakingtypes.DoNotModifyDesc
	if monikerUnchanged && detailsUnchanged {
		return nil
	}

	moniker, details := editMoniker, editDetails
	if monikerUnchanged || detailsUnchanged {
		cur, err := k.stakingKeeper.GetValidator(ctx, sdk.ValAddress(operatorValBytes))
		if err != nil {
			return err
		}
		if monikerUnchanged {
			moniker = cur.Description.Moniker
		}
		if detailsUnchanged {
			details = cur.Description.Details
		}
	}
	return k.ValidateValidatorCreationEligibility(ctx, operatorValBytes, moniker, details)
}

// GetValidatorIdentity loads a validator by operator address and parses the
// Steem identity out of its moniker (username) + Description.details (keys).
// Used by the ValidatorIdentity query.
func (k Keeper) GetValidatorIdentity(ctx context.Context, valAddr sdk.ValAddress) (types.ValidatorIdentity, error) {
	validator, err := k.stakingKeeper.GetValidator(ctx, valAddr)
	if err != nil {
		return types.ValidatorIdentity{}, err
	}
	return types.ParseValidatorIdentity(validator.Description.Moniker, validator.Description.Details)
}
