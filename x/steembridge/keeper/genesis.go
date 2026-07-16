package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"steemvm/x/steembridge/types"
)

// InitGenesis initializes the module's state from a provided genesis state.
func (k Keeper) InitGenesis(ctx context.Context, genState types.GenesisState) error {
	for _, elem := range genState.DepositList {
		if err := k.Deposit.Set(ctx, elem.Id, elem); err != nil {
			return err
		}
		if err := k.DepositByStatus.Set(ctx, collections.Join(int32(elem.Status), elem.Id)); err != nil {
			return err
		}
		// Live expiry removes the dedup index and per-validator confirmations
		// so the (txid, opIndex) key becomes submittable fresh; rebuilding
		// them for EXPIRED deposits would permanently block the key.
		if elem.Status == types.DepositStatus_DEPOSIT_STATUS_EXPIRED {
			continue
		}
		if err := k.DepositByTxid.Set(ctx, collections.Join(elem.Txid, elem.OpIndex), elem.Id); err != nil {
			return err
		}
		for _, confirmation := range elem.ValidatorConfirmations {
			validatorAddr, err := sdk.ValAddressFromBech32(confirmation.ValidatorAddress)
			if err != nil {
				return err
			}
			if err := k.DepositConfirmedBy.Set(ctx, collections.Join3(elem.Txid, elem.OpIndex, []byte(validatorAddr))); err != nil {
				return err
			}
		}
	}

	if err := k.DepositSeq.Set(ctx, genState.DepositCount); err != nil {
		return err
	}
	for _, elem := range genState.WithdrawalList {
		if err := k.Withdrawal.Set(ctx, elem.Id, elem); err != nil {
			return err
		}
		if err := k.WithdrawalByStatus.Set(ctx, collections.Join(int32(elem.Status), elem.Id)); err != nil {
			return err
		}
	}

	if err := k.WithdrawalSeq.Set(ctx, genState.WithdrawalCount); err != nil {
		return err
	}

	if err := k.Totals.Set(ctx, types.BridgeTotals{
		TotalMintedAsteem: genState.TotalMintedAsteem,
		TotalBurnedAsteem: genState.TotalBurnedAsteem,
	}); err != nil {
		return err
	}

	for _, elem := range genState.NameRegistrationList {
		if err := k.NameRegistration.Set(ctx, elem.Id, elem); err != nil {
			return err
		}
		if err := k.NameRegistrationByStatus.Set(ctx, collections.Join(int32(elem.Status), elem.Id)); err != nil {
			return err
		}
		if err := k.NameRegistrationByAccount.Set(ctx, collections.Join(elem.SteemAccount, elem.Id)); err != nil {
			return err
		}
		// Same rationale as deposits: EXPIRED registrations released their
		// dedup key and attestation entries.
		if elem.Status == types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_EXPIRED {
			continue
		}
		if err := k.NameRegistrationByTxid.Set(ctx, collections.Join(elem.Txid, elem.OpIndex), elem.Id); err != nil {
			return err
		}
		for _, confirmation := range elem.ValidatorConfirmations {
			validatorAddr, err := sdk.ValAddressFromBech32(confirmation.ValidatorAddress)
			if err != nil {
				return err
			}
			if err := k.NameRegistrationConfirmedBy.Set(ctx, collections.Join3(elem.Txid, elem.OpIndex, []byte(validatorAddr))); err != nil {
				return err
			}
		}
		if elem.Status == types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_AWAITING_CONFIRMATION {
			destAddr, err := k.addressCodec.StringToBytes(elem.DerivedDestination)
			if err != nil {
				return err
			}
			if err := k.NameRegistrationAwaitingByDest.Set(ctx, collections.Join([]byte(destAddr), elem.Id)); err != nil {
				return err
			}
		}
	}

	if err := k.NameRegistrationSeq.Set(ctx, genState.NameRegistrationCount); err != nil {
		return err
	}

	for _, elem := range genState.ActiveNameList {
		if err := k.ActiveName.Set(ctx, elem.SteemAccount, elem); err != nil {
			return err
		}
		addr, err := k.addressCodec.StringToBytes(elem.Address)
		if err != nil {
			return err
		}
		if err := k.ActiveNameByAddress.Set(ctx, collections.Join([]byte(addr), elem.SteemAccount)); err != nil {
			return err
		}
	}

	return k.Params.Set(ctx, genState.Params)
}

// ExportGenesis returns the module's exported genesis.
func (k Keeper) ExportGenesis(ctx context.Context) (*types.GenesisState, error) {
	var err error

	genesis := types.DefaultGenesis()
	genesis.Params, err = k.Params.Get(ctx)
	if err != nil {
		return nil, err
	}
	err = k.Deposit.Walk(ctx, nil, func(key uint64, elem types.Deposit) (bool, error) {
		genesis.DepositList = append(genesis.DepositList, elem)
		return false, nil
	})
	if err != nil {
		return nil, err
	}

	genesis.DepositCount, err = k.DepositSeq.Peek(ctx)
	if err != nil {
		return nil, err
	}
	err = k.Withdrawal.Walk(ctx, nil, func(key uint64, elem types.Withdrawal) (bool, error) {
		genesis.WithdrawalList = append(genesis.WithdrawalList, elem)
		return false, nil
	})
	if err != nil {
		return nil, err
	}

	genesis.WithdrawalCount, err = k.WithdrawalSeq.Peek(ctx)
	if err != nil {
		return nil, err
	}

	totals, err := k.Totals.Get(ctx)
	if err != nil {
		return nil, err
	}
	genesis.TotalMintedAsteem = totals.TotalMintedAsteem
	genesis.TotalBurnedAsteem = totals.TotalBurnedAsteem

	err = k.NameRegistration.Walk(ctx, nil, func(key uint64, elem types.NameRegistration) (bool, error) {
		genesis.NameRegistrationList = append(genesis.NameRegistrationList, elem)
		return false, nil
	})
	if err != nil {
		return nil, err
	}

	genesis.NameRegistrationCount, err = k.NameRegistrationSeq.Peek(ctx)
	if err != nil {
		return nil, err
	}

	err = k.ActiveName.Walk(ctx, nil, func(key string, elem types.NameRecord) (bool, error) {
		genesis.ActiveNameList = append(genesis.ActiveNameList, elem)
		return false, nil
	})
	if err != nil {
		return nil, err
	}

	return genesis, nil
}
