package steemtx

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"strings"

	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"golang.org/x/crypto/ripemd160" //nolint:staticcheck // Steem's key checksum is RIPEMD-160 by definition
)

// DecodeWIF decodes a Steem/Bitcoin WIF private key:
// base58(0x80 || 32-byte key || first 4 bytes of sha256(sha256(0x80 || key))).
func DecodeWIF(wif string) (*secp256k1.PrivateKey, error) {
	raw := base58.Decode(strings.TrimSpace(wif))
	if len(raw) != 37 || raw[0] != 0x80 {
		return nil, errors.New("steemtx: not a WIF private key")
	}
	sum := sha256.Sum256(raw[:33])
	sum = sha256.Sum256(sum[:])
	if !bytes.Equal(sum[:4], raw[33:]) {
		return nil, errors.New("steemtx: bad WIF checksum")
	}
	return secp256k1.PrivKeyFromBytes(raw[1:33]), nil
}

// DecodePublicKey decodes an STM public key to its 33-byte compressed form:
// "STM" + base58(33-byte key || first 4 bytes of ripemd160(key)).
func DecodePublicKey(s string) ([]byte, error) {
	if !strings.HasPrefix(s, "STM") {
		return nil, errors.New("steemtx: public key must start with STM")
	}
	raw := base58.Decode(s[3:])
	if len(raw) != 37 {
		return nil, errors.New("steemtx: bad public key length")
	}
	h := ripemd160.New()
	h.Write(raw[:33])
	if !bytes.Equal(h.Sum(nil)[:4], raw[33:]) {
		return nil, errors.New("steemtx: bad public key checksum")
	}
	return raw[:33], nil
}

// EncodePublicKey encodes a public key in STM form.
func EncodePublicKey(pub *secp256k1.PublicKey) string {
	key := pub.SerializeCompressed()
	h := ripemd160.New()
	h.Write(key)
	return "STM" + base58.Encode(append(key, h.Sum(nil)[:4]...))
}
