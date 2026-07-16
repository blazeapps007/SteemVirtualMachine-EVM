package app

import (
	"testing"

	"github.com/cosmos/cosmos-sdk/codec/legacy"
	"github.com/cosmos/cosmos-sdk/x/auth/migrations/legacytx"
	"github.com/cosmos/evm/crypto/ethsecp256k1"
	"github.com/stretchr/testify/require"
)

// TestGlobalLegacyAminoEncodesEthSecp256k1PubKey guards the tx simulation path
// (`--gas auto`). x/auth's ConsumeTxSizeGasDecorator builds a mock StdSignature
// carrying the signer's pubkey and marshals it with the SDK's *global*
// legacy.Cdc (x/auth/ante/basic.go). Registering eth_secp256k1 only on the
// interface registry is not enough: without the legacy amino registration this
// package's init() performs, that MustMarshal panics with "Cannot encode
// unregistered concrete type ethsecp256k1.PubKey", failing EVERY simulated
// Cosmos tx on this chain.
//
// The companion trap is documented in that init(): the registration must add
// only the CONCRETE key types, because evmcryptocodec.RegisterCrypto would also
// re-register the cryptotypes.PubKey interface and panic at startup with
// "TypeInfo already exists for types.PubKey".
func TestGlobalLegacyAminoEncodesEthSecp256k1PubKey(t *testing.T) {
	priv, err := ethsecp256k1.GenerateKey()
	require.NoError(t, err)

	simSig := legacytx.StdSignature{PubKey: priv.PubKey()} //nolint:staticcheck // mirrors what the ante does

	require.NotPanics(t, func() {
		_ = legacy.Cdc.MustMarshal(simSig)
	}, "legacy.Cdc must encode eth_secp256k1 pubkeys or --gas auto panics for every Cosmos tx")
}
