package types

import (
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// DefaultGenesis returns the default genesis state
func DefaultGenesis() *GenesisState {
	return &GenesisState{
		Params:               DefaultParams(),
		DepositList:          []Deposit{},
		WithdrawalList:       []Withdrawal{},
		TotalMintedAsteem:    math.ZeroInt(),
		TotalBurnedAsteem:    math.ZeroInt(),
		NameRegistrationList: []NameRegistration{},
		ActiveNameList:       []NameRecord{},
	}
}

// Validate performs basic genesis state validation returning an error upon any
// failure.
func (gs GenesisState) Validate() error {
	depositIdMap := make(map[uint64]bool)
	depositTxidMap := make(map[string]bool)
	depositCount := gs.GetDepositCount()
	for _, elem := range gs.DepositList {
		if _, ok := depositIdMap[elem.Id]; ok {
			return fmt.Errorf("duplicated id for deposit")
		}
		if elem.Id >= depositCount {
			return fmt.Errorf("deposit id should be lower or equal than the last id")
		}
		depositIdMap[elem.Id] = true

		dedupKey := fmt.Sprintf("%s/%d", elem.Txid, elem.OpIndex)
		if _, ok := depositTxidMap[dedupKey]; ok {
			return fmt.Errorf("duplicated (txid, op_index) for deposit: %s", dedupKey)
		}
		depositTxidMap[dedupKey] = true

		validatorSeen := make(map[string]bool)
		for _, confirmation := range elem.ValidatorConfirmations {
			if validatorSeen[confirmation.ValidatorAddress] {
				return fmt.Errorf("duplicated validator confirmation for deposit %d: %s", elem.Id, confirmation.ValidatorAddress)
			}
			validatorSeen[confirmation.ValidatorAddress] = true
		}
	}
	withdrawalIdMap := make(map[uint64]bool)
	withdrawalCount := gs.GetWithdrawalCount()
	for _, elem := range gs.WithdrawalList {
		if _, ok := withdrawalIdMap[elem.Id]; ok {
			return fmt.Errorf("duplicated id for withdrawal")
		}
		if elem.Id >= withdrawalCount {
			return fmt.Errorf("withdrawal id should be lower or equal than the last id")
		}
		withdrawalIdMap[elem.Id] = true
	}

	if gs.TotalMintedAsteem.IsNil() || gs.TotalMintedAsteem.IsNegative() {
		return fmt.Errorf("total minted asteem must be a non-negative amount")
	}
	if gs.TotalBurnedAsteem.IsNil() || gs.TotalBurnedAsteem.IsNegative() {
		return fmt.Errorf("total burned asteem must be a non-negative amount")
	}

	registrationByID := make(map[uint64]*NameRegistration)
	registrationTxidMap := make(map[string]bool)
	registrationCount := gs.GetNameRegistrationCount()
	for i, elem := range gs.NameRegistrationList {
		if _, ok := registrationByID[elem.Id]; ok {
			return fmt.Errorf("duplicated id for name registration")
		}
		if elem.Id >= registrationCount {
			return fmt.Errorf("name registration id should be lower or equal than the last id")
		}
		registrationByID[elem.Id] = &gs.NameRegistrationList[i]

		dedupKey := fmt.Sprintf("%s/%d", elem.Txid, elem.OpIndex)
		if _, ok := registrationTxidMap[dedupKey]; ok {
			return fmt.Errorf("duplicated (txid, op_index) for name registration: %s", dedupKey)
		}
		registrationTxidMap[dedupKey] = true

		validatorSeen := make(map[string]bool)
		for _, confirmation := range elem.ValidatorConfirmations {
			if validatorSeen[confirmation.ValidatorAddress] {
				return fmt.Errorf("duplicated validator confirmation for name registration %d: %s", elem.Id, confirmation.ValidatorAddress)
			}
			validatorSeen[confirmation.ValidatorAddress] = true
		}

		switch elem.Status {
		case NameRegistrationStatus_NAME_REGISTRATION_STATUS_AWAITING_CONFIRMATION,
			NameRegistrationStatus_NAME_REGISTRATION_STATUS_ACTIVE:
			if _, err := sdk.AccAddressFromBech32(elem.DerivedDestination); err != nil {
				return fmt.Errorf("name registration %d has status %s but an unparseable derived destination %q", elem.Id, elem.Status, elem.DerivedDestination)
			}
		}
	}

	activeNameSeen := make(map[string]bool)
	for _, elem := range gs.ActiveNameList {
		if activeNameSeen[elem.SteemAccount] {
			return fmt.Errorf("duplicated steem account in active name list: %s", elem.SteemAccount)
		}
		activeNameSeen[elem.SteemAccount] = true

		if err := ValidateSteemAccountName(elem.SteemAccount); err != nil {
			return fmt.Errorf("invalid steem account in active name list: %w", err)
		}
		if _, err := sdk.AccAddressFromBech32(elem.Address); err != nil {
			return fmt.Errorf("active name %s has an unparseable address %q", elem.SteemAccount, elem.Address)
		}

		registration, ok := registrationByID[elem.RegistrationId]
		if !ok {
			return fmt.Errorf("active name %s references missing registration %d", elem.SteemAccount, elem.RegistrationId)
		}
		if registration.Status != NameRegistrationStatus_NAME_REGISTRATION_STATUS_ACTIVE {
			return fmt.Errorf("active name %s references registration %d with non-ACTIVE status %s", elem.SteemAccount, elem.RegistrationId, registration.Status)
		}
		if registration.SteemAccount != elem.SteemAccount {
			return fmt.Errorf("active name %s references registration %d for a different account %s", elem.SteemAccount, elem.RegistrationId, registration.SteemAccount)
		}
		if registration.DerivedDestination != elem.Address {
			return fmt.Errorf("active name %s address %s does not match registration %d destination %s", elem.SteemAccount, elem.Address, elem.RegistrationId, registration.DerivedDestination)
		}
	}

	return gs.Params.Validate()
}
