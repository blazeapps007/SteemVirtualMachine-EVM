package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"steemvm/x/oracle/bridge/keeper"
	"steemvm/x/oracle/bridge/types"
)

// TestAttestDeposit_MinorityCannotBlockWithWrongFacts is the regression test
// for the per-fact-set deposit tally. A minority validator front-runs with
// wrong facts (a different amount and destination); the honest majority then
// attests the true facts. The minority's fact-set must not pin the record: the
// honest fact-set crosses the threshold and mints the TRUE amount to the TRUE
// destination.
func TestAttestDeposit_MinorityCannotBlockWithWrongFacts(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableBridge(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	attacker := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(attacker.ValAddr, 3, true)
	honestA := newTestValidator(t, 2)
	f.stakingKeeper.setValidator(honestA.ValAddr, 50, true)
	honestB := newTestValidator(t, 3)
	f.stakingKeeper.setValidator(honestB.ValAddr, 47, true)

	realDest := sdk.AccAddress([]byte("real_destination_bytes")[:20])
	attackerDest := sdk.AccAddress([]byte("attacker_dest_bytesxx")[:20])
	txid := "ffff000000000000000000000000000000000000"

	// Attacker front-runs with wrong amount + wrong destination.
	bogus := baseDepositMsg(attacker, txid, 0)
	bogus.AmountMillisteem = 1
	bogus.Memo = attackerDest.String()
	_, err := ms.AttestDeposit(f.ctx, bogus)
	require.NoError(t, err)

	// Honest majority attests the true facts (baseDepositMsg's 1000 millisteem).
	for _, v := range []testValidator{honestA, honestB} {
		msg := baseDepositMsg(v, txid, 0)
		msg.Memo = realDest.String()
		_, err := ms.AttestDeposit(f.ctx, msg)
		require.NoError(t, err)
	}

	genesis, err := f.keeper.ExportGenesis(f.ctx)
	require.NoError(t, err)
	d := genesis.DepositList[0]
	require.True(t, d.Minted, "the honest fact-set (97%) mints despite the front-run")
	require.Equal(t, realDest.String(), d.DerivedDestination, "mints to the TRUE destination")
	require.Equal(t, uint64(1000), d.AmountMillisteem, "canonical facts are the honest fact-set's")
	want := types.MillisteemToAsteem(1000)
	require.Equal(t, sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, want)), f.bankKeeper.balances[realDest.String()])
	require.True(t, f.bankKeeper.balances[attackerDest.String()].IsZero(), "nothing is minted to the attacker's destination")
}

// TestSubmitNameRegistration_MinorityCannotBlockWithWrongFacts is the
// name-service counterpart: an attacker front-runs a registration with a
// different destination memo; the honest majority's fact-set wins and parks
// the registration AWAITING for the TRUE destination.
func TestSubmitNameRegistration_MinorityCannotBlockWithWrongFacts(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableNameService(t, f)
	ms := keeper.NewMsgServerImpl(f.keeper)

	attacker := newTestValidator(t, 1)
	f.stakingKeeper.setValidator(attacker.ValAddr, 3, true)
	honestA := newTestValidator(t, 2)
	f.stakingKeeper.setValidator(honestA.ValAddr, 50, true)
	honestB := newTestValidator(t, 3)
	f.stakingKeeper.setValidator(honestB.ValAddr, 47, true)

	txid := "aaaa000000000000000000000000000000000099"

	bogus := baseNameRegistrationMsg(attacker, txid, 0)
	bogus.Memo = "0x2222222222222222222222222222222222222222"
	_, err := ms.SubmitNameRegistration(f.ctx, bogus)
	require.NoError(t, err)

	// The honest majority attests the real registration (baseNameRegistrationMsg's memo).
	real := baseNameRegistrationMsg(honestA, txid, 0)
	for _, v := range []testValidator{honestA, honestB} {
		msg := baseNameRegistrationMsg(v, txid, 0)
		_, err := ms.SubmitNameRegistration(f.ctx, msg)
		require.NoError(t, err)
	}

	id, err := f.keeper.NameRegistrationByTxid.Get(f.ctx, collections.Join(txid, uint32(0)))
	require.NoError(t, err)
	reg, err := f.keeper.NameRegistration.Get(f.ctx, id)
	require.NoError(t, err)
	require.Equal(t, types.NameRegistrationStatus_NAME_REGISTRATION_STATUS_AWAITING_CONFIRMATION, reg.Status,
		"the honest fact-set (97%) resolves despite the front-run")
	require.Equal(t, real.Memo, reg.Memo, "canonical facts are the honest fact-set's")
}
