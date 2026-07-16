package types_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"steemvm/x/steembridge/types"
)

// Real blaze.apps Steem public keys (public data), used as valid samples.
const (
	sampleOwnerKey   = "STM59ytBUdJ1DaSeFoz4GP5nBFiVBF2dpEDN4cHcR4wYFxWvE2ctR"
	sampleActiveKey  = "STM5fvPvqwkwDTKXW1wsr7wPdeVXjetjvg1Wer8Zs7P24KFHht2Be"
	samplePostingKey = "STM5nr8yJLekbns9BNtx8FUxZTvphtfuNKVkTqCXswAjM6GTrQtdX"
)

func sampleDetails() string {
	return "owner=" + sampleOwnerKey + ";active=" + sampleActiveKey + ";posting=" + samplePostingKey
}

func TestValidateSteemPublicKey(t *testing.T) {
	require.NoError(t, types.ValidateSteemPublicKey(sampleOwnerKey))
	require.NoError(t, types.ValidateSteemPublicKey(sampleActiveKey))

	require.Error(t, types.ValidateSteemPublicKey(""), "empty must fail")
	require.Error(t, types.ValidateSteemPublicKey("STM"), "prefix only must fail")
	require.Error(t, types.ValidateSteemPublicKey("XYZ"+sampleOwnerKey[3:]), "wrong prefix must fail")
	require.Error(t, types.ValidateSteemPublicKey(sampleOwnerKey[3:]), "missing prefix must fail")
	require.Error(t, types.ValidateSteemPublicKey("STM0OIl"+sampleOwnerKey[3:]), "non-base58 chars must fail")
	require.Error(t, types.ValidateSteemPublicKey("STM5abc"), "too short must fail")
}

func TestParseValidatorIdentity(t *testing.T) {
	id, err := types.ParseValidatorIdentity("blaze.apps", sampleDetails())
	require.NoError(t, err)
	require.Equal(t, "blaze.apps", id.SteemUsername)
	require.Equal(t, sampleOwnerKey, id.OwnerPubkey)
	require.Equal(t, sampleActiveKey, id.ActivePubkey)
	require.Equal(t, samplePostingKey, id.PostingPubkey)

	// Order-independent and whitespace-tolerant; moniker whitespace trimmed.
	reordered := "posting=" + samplePostingKey + " ; owner=" + sampleOwnerKey + ";active=" + sampleActiveKey
	id2, err := types.ParseValidatorIdentity("  blaze.apps  ", reordered)
	require.NoError(t, err)
	require.Equal(t, id, id2)
}

func TestParseValidatorIdentity_Rejects(t *testing.T) {
	full := sampleDetails()
	cases := []struct {
		name    string
		moniker string
		details string
	}{
		{"empty details", "blaze.apps", ""},
		{"empty moniker", "", full},
		{"missing posting", "blaze.apps", "owner=" + sampleOwnerKey + ";active=" + sampleActiveKey},
		{"bad pubkey", "blaze.apps", "owner=STMnotvalid;active=" + sampleActiveKey + ";posting=" + samplePostingKey},
		{"steem key not allowed", "blaze.apps", "steem=blaze.apps;" + full},
		{"unknown key", "blaze.apps", full + ";extra=1"},
		{"duplicate key", "blaze.apps", "owner=" + sampleOwnerKey + ";" + full},
		{"no equals", "blaze.apps", "owner " + sampleOwnerKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := types.ParseValidatorIdentity(tc.moniker, tc.details)
			require.Error(t, err)
		})
	}
}
