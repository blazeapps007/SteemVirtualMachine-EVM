package types

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// factsEncoder builds an unambiguous byte encoding of an attested fact-set:
// every field is length- or width-prefixed, so no two distinct fact-sets can
// encode to the same bytes. The hash of this encoding is the "candidate key"
// under which per-fact-set attestation votes are tallied, so validators that
// observed the SAME Steem facts accrue power together and a fact-set that
// reaches the 2/3 threshold resolves — a minority reporting different facts
// starts its own candidate and cannot block or redirect the majority.
type factsEncoder struct{ buf []byte }

func (e *factsEncoder) u64(v uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	e.buf = append(e.buf, b[:]...)
}

func (e *factsEncoder) str(s string) {
	e.u64(uint64(len(s)))
	e.buf = append(e.buf, s...)
}

func (e *factsEncoder) sum() string {
	h := sha256.Sum256(e.buf)
	return hex.EncodeToString(h[:])
}

// DepositFactsHash hashes the full observed fact-set of a deposit attestation.
// (txid, opIndex) are deliberately excluded: they are the dedup key the
// candidate is scoped under, not part of what validators disagree about.
func DepositFactsHash(steemBlock uint64, steemTimestamp, steemSender, gatewayAccount string, amountMillisteem uint64, memo string, asset BridgeAsset) string {
	e := &factsEncoder{}
	e.u64(steemBlock)
	e.str(steemTimestamp)
	e.str(steemSender)
	e.str(gatewayAccount)
	e.u64(amountMillisteem)
	e.str(memo)
	e.u64(uint64(uint32(asset)))
	return e.sum()
}

// NameFactsHash hashes the full observed fact-set of a name-registration
// attestation, on the same basis as DepositFactsHash.
func NameFactsHash(steemBlock uint64, steemTimestamp, steemAccount, gatewayAccount string, amountMillisteem uint64, memo string) string {
	e := &factsEncoder{}
	e.u64(steemBlock)
	e.str(steemTimestamp)
	e.str(steemAccount)
	e.str(gatewayAccount)
	e.u64(amountMillisteem)
	e.str(memo)
	return e.sum()
}
