package steemtx

import (
	"encoding/hex"
	"errors"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// Sign returns a Steem-compatible compact recoverable signature of digest, as
// hex: 1 header byte (27 + 4 + recovery id; always 0x1f or 0x20) then r then s.
//
// Steem rejects non-canonical signatures: s must be low (the lower half of
// the curve order) and r and s must each pass the byte rule in isCanonical.
// About half of all signatures fail the byte rule, so this retries with the
// next RFC 6979 nonce (extraIterations) until one passes.
func Sign(key *secp256k1.PrivateKey, digest [32]byte) (string, error) {
	keyBytes := key.Key.Bytes()
	for attempt := uint32(0); attempt < 256; attempt++ {
		k := secp256k1.NonceRFC6979(keyBytes[:], digest[:], nil, nil, attempt)
		sig, recovery, ok := signWithNonce(&key.Key, k, digest[:])
		k.Zero()
		if !ok {
			continue
		}
		r, s := sig.R(), sig.S()
		var out [65]byte
		out[0] = 27 + 4 + recovery // 4 = compressed public key
		r.PutBytesUnchecked(out[1:33])
		s.PutBytesUnchecked(out[33:65])
		if isCanonical(out[1:]) {
			return hex.EncodeToString(out[:]), nil
		}
	}
	return "", errors.New("steemtx: no canonical signature found")
}

// signWithNonce mirrors the ECDSA signing step in dcrd's ecdsa package,
// including low-S normalization, but takes the nonce as a parameter.
func signWithNonce(priv, k *secp256k1.ModNScalar, hash []byte) (*ecdsa.Signature, byte, bool) {
	var kG secp256k1.JacobianPoint
	secp256k1.ScalarBaseMultNonConst(k, &kG)
	kG.ToAffine()

	var xBytes [32]byte
	kG.X.PutBytes(&xBytes)
	var r secp256k1.ModNScalar
	overflow := r.SetBytes(&xBytes)
	if r.IsZero() {
		return nil, 0, false
	}
	recovery := byte(overflow<<1) | byte(kG.Y.IsOddBit())

	var e secp256k1.ModNScalar
	e.SetByteSlice(hash)
	kinv := new(secp256k1.ModNScalar).InverseValNonConst(k)
	s := new(secp256k1.ModNScalar).Mul2(priv, &r).Add(&e).Mul(kinv)
	if s.IsZero() {
		return nil, 0, false
	}
	if s.IsOverHalfOrder() {
		s.Negate()
		recovery ^= 0x01
	}
	return ecdsa.NewSignature(&r, s), recovery, true
}

// isCanonical is Steem's byte rule on r (rs[0:32]) and s (rs[32:64]): no high
// bit in the first byte, and no leading 0x00 unless the next byte has its high
// bit set.
func isCanonical(rs []byte) bool {
	return rs[0]&0x80 == 0 && !(rs[0] == 0 && rs[1]&0x80 == 0) &&
		rs[32]&0x80 == 0 && !(rs[32] == 0 && rs[33]&0x80 == 0)
}

// RecoverSigner returns the STM public key that produced sigHex over digest.
// Non-canonical signatures are rejected, exactly as the chain does.
func RecoverSigner(digest [32]byte, sigHex string) (string, error) {
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != 65 {
		return "", errors.New("steemtx: signature must be 65 bytes of hex")
	}
	if !isCanonical(sig[1:]) {
		return "", errors.New("steemtx: signature is not canonical")
	}
	pub, _, err := ecdsa.RecoverCompact(sig, digest[:])
	if err != nil {
		return "", err
	}
	return EncodePublicKey(pub), nil
}
