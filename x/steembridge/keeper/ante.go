package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"steemvm/x/steembridge/types"
)

// MaxFreeDepositsPerValidatorPerBlock caps how many fee-exempt validator
// attestations (MsgSubmitSteemDeposit and MsgSubmitNameRegistration draw
// from this single shared budget) a single validator can have processed free
// of charge in a single block, as defense in depth alongside the stateful
// acceptance checks the ante handler also performs.
const MaxFreeDepositsPerValidatorPerBlock = 100

// MaxFreeNameConfirmationsPerAddressPerBlock caps how many fee-exempt
// MsgConfirmName messages a single confirmer address can have processed free
// of charge in a single block. Deliberately small: a qualifying confirmation
// requires an actual AWAITING registration destined to the signer, which
// itself cost a real Steem-side payment plus a 2/3 validator attestation, so
// free confirmations are inherently scarce.
const MaxFreeNameConfirmationsPerAddressPerBlock = 10

// ConsumeFreeDepositQuota increments the per-validator, per-block counter of
// fee-exempt attestation messages (deposits and name registrations) used by
// the ante handler's fee exemption decorator (app/ante_steembridge.go), and
// reports whether this validator is still under the cap. The counter resets
// lazily whenever the stored height differs from the current block height,
// so no O(validators) BeginBlocker sweep is needed.
func (k Keeper) ConsumeFreeDepositQuota(ctx context.Context, validatorAddr []byte) (bool, error) {
	return consumeFreeQuota(ctx, k.FreeDepositCounter, validatorAddr, MaxFreeDepositsPerValidatorPerBlock)
}

// ConsumeFreeNameConfirmQuota is ConsumeFreeDepositQuota's counterpart for
// MsgConfirmName, tracked per confirmer account address.
func (k Keeper) ConsumeFreeNameConfirmQuota(ctx context.Context, confirmerAddr []byte) (bool, error) {
	return consumeFreeQuota(ctx, k.FreeNameConfirmCounter, confirmerAddr, MaxFreeNameConfirmationsPerAddressPerBlock)
}

// consumeFreeQuota implements the shared lazily-reset per-address, per-block
// counter behind both free-tx caps.
func consumeFreeQuota(ctx context.Context, counters collections.Map[[]byte, types.FreeDepositCounter], addr []byte, quota uint64) (bool, error) {
	height := sdk.UnwrapSDKContext(ctx).BlockHeight()

	counter, err := counters.Get(ctx, addr)
	if err != nil {
		if !errors.Is(err, collections.ErrNotFound) {
			return false, err
		}
		counter = types.FreeDepositCounter{}
	}
	if counter.Height != height {
		counter = types.FreeDepositCounter{Height: height}
	}
	if counter.Count >= quota {
		return false, nil
	}

	counter.Count++
	if err := counters.Set(ctx, addr, counter); err != nil {
		return false, err
	}
	return true, nil
}
