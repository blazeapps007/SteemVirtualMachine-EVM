package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"

	"steemvm/x/steembridge/keeper"
	"steemvm/x/steembridge/types"
)

// Real blaze.apps Steem public keys (public data).
const (
	vOwnerKey   = "STM59ytBUdJ1DaSeFoz4GP5nBFiVBF2dpEDN4cHcR4wYFxWvE2ctR"
	vActiveKey  = "STM5fvPvqwkwDTKXW1wsr7wPdeVXjetjvg1Wer8Zs7P24KFHht2Be"
	vPostingKey = "STM5nr8yJLekbns9BNtx8FUxZTvphtfuNKVkTqCXswAjM6GTrQtdX"
)

// keysDetails is the Description.details blob (three keys only; the username is
// the moniker).
func keysDetails() string {
	return "owner=" + vOwnerKey + ";active=" + vActiveKey + ";posting=" + vPostingKey
}

func seedActiveName(t *testing.T, f *fixtureWithFakes, username, addr string) {
	t.Helper()
	require.NoError(t, f.keeper.ActiveName.Set(f.ctx, username, types.NameRecord{
		SteemAccount: username,
		Address:      addr,
	}))
}

func TestValidateValidatorCreationEligibility(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableNameService(t, f)

	val := newTestValidator(t, 9)
	seedActiveName(t, f, "blaze.apps", val.AccAddr)

	// Happy path: moniker is a username registered, active, owned by this operator.
	require.NoError(t, f.keeper.ValidateValidatorCreationEligibility(f.ctx, val.ValAddr.Bytes(), "blaze.apps", keysDetails()))

	// Moniker/username with no active registration.
	require.Error(t, f.keeper.ValidateValidatorCreationEligibility(f.ctx, val.ValAddr.Bytes(), "unregistered", keysDetails()))

	// Username registered, but to a DIFFERENT account than this operator.
	other := newTestValidator(t, 10)
	seedActiveName(t, f, "someoneelse", other.AccAddr)
	require.Error(t, f.keeper.ValidateValidatorCreationEligibility(f.ctx, val.ValAddr.Bytes(), "someoneelse", keysDetails()))

	// Empty moniker, and malformed / incomplete key blobs.
	require.Error(t, f.keeper.ValidateValidatorCreationEligibility(f.ctx, val.ValAddr.Bytes(), "", keysDetails()))
	require.Error(t, f.keeper.ValidateValidatorCreationEligibility(f.ctx, val.ValAddr.Bytes(), "blaze.apps", "not-an-identity"))
	require.Error(t, f.keeper.ValidateValidatorCreationEligibility(f.ctx, val.ValAddr.Bytes(), "blaze.apps",
		"owner=STMbad;active="+vActiveKey+";posting="+vPostingKey))
}

func TestValidateValidatorEdit(t *testing.T) {
	f := initFixtureWithFakes(t)
	enableNameService(t, f)

	val := newTestValidator(t, 15)
	seedActiveName(t, f, "blaze.apps", val.AccAddr)
	f.stakingKeeper.setValidatorWithDetails(val.ValAddr, "blaze.apps", keysDetails())

	dnm := stakingtypes.DoNotModifyDesc

	// Benign edit: neither moniker nor details changed -> allowed.
	require.NoError(t, f.keeper.ValidateValidatorEdit(f.ctx, val.ValAddr.Bytes(), dnm, dnm))

	// Edit details only (moniker sentinel): current moniker substituted, still valid.
	require.NoError(t, f.keeper.ValidateValidatorEdit(f.ctx, val.ValAddr.Bytes(), dnm, keysDetails()))

	// Edit that blanks details -> rejected (can't strip the keys).
	require.Error(t, f.keeper.ValidateValidatorEdit(f.ctx, val.ValAddr.Bytes(), dnm, ""))

	// Edit moniker to an unregistered username -> rejected.
	require.Error(t, f.keeper.ValidateValidatorEdit(f.ctx, val.ValAddr.Bytes(), "unregistered", dnm))
}

func TestValidateValidatorCreationEligibility_NameServiceDisabled(t *testing.T) {
	f := initFixtureWithFakes(t)
	// Name service left disabled (default): the gate is a no-op, even with junk input.
	val := newTestValidator(t, 11)
	require.NoError(t, f.keeper.ValidateValidatorCreationEligibility(f.ctx, val.ValAddr.Bytes(), "", ""))
	require.NoError(t, f.keeper.ValidateValidatorCreationEligibility(f.ctx, val.ValAddr.Bytes(), "x", "garbage"))
}

func TestValidateValidatorCreationEligibility_ParamsNotSet(t *testing.T) {
	f := initFixtureWithFakes(t)
	// Simulate the genesis-gentx path: the gate runs through the ante during
	// InitChain before steembridge InitGenesis has set Params. With Params
	// absent the gate must skip (trust the seeded genesis validator), not panic.
	require.NoError(t, f.keeper.Params.Remove(f.ctx))
	val := newTestValidator(t, 14)
	require.NoError(t, f.keeper.ValidateValidatorCreationEligibility(f.ctx, val.ValAddr.Bytes(), "x", "garbage"))
}

func TestValidatorIdentityQuery_ByValoper(t *testing.T) {
	f := initFixtureWithFakes(t)
	qs := keeper.NewQueryServerImpl(f.keeper)

	val := newTestValidator(t, 12)
	// Register the validator (moniker = username, keys in details) in the fake staking keeper.
	f.stakingKeeper.setValidatorWithDetails(val.ValAddr, "blaze.apps", keysDetails())

	// Query accepts the operator (steemvaloper...) form via parseAddressArg.
	resp, err := qs.ValidatorIdentity(f.ctx, &types.QueryValidatorIdentityRequest{ValidatorAddress: val.ValAddr.String()})
	require.NoError(t, err)
	require.Equal(t, "blaze.apps", resp.SteemUsername)
	require.Equal(t, vOwnerKey, resp.OwnerPubkey)
	require.Equal(t, vActiveKey, resp.ActivePubkey)
	require.Equal(t, vPostingKey, resp.PostingPubkey)

	// Also accepts the account (steem1...) form — same bytes.
	resp2, err := qs.ValidatorIdentity(f.ctx, &types.QueryValidatorIdentityRequest{ValidatorAddress: sdk.AccAddress(val.ValAddr).String()})
	require.NoError(t, err)
	require.Equal(t, resp, resp2)

	// Unknown validator -> NotFound (parses address, but no such validator).
	unknown := newTestValidator(t, 13)
	_, err = qs.ValidatorIdentity(f.ctx, &types.QueryValidatorIdentityRequest{ValidatorAddress: unknown.ValAddr.String()})
	require.Error(t, err)
}
