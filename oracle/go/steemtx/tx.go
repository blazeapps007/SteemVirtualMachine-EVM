package steemtx

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"time"
)

// ChainID is the Steem mainnet chain ID: 32 zero bytes.
var ChainID = make([]byte, 32)

// Transaction is a Steem transaction in its JSON form (as stored in
// proposals and passed to broadcast_transaction_synchronous).
type Transaction struct {
	RefBlockNum    uint16      `json:"ref_block_num"`
	RefBlockPrefix uint32      `json:"ref_block_prefix"`
	Expiration     string      `json:"expiration"`
	Operations     []Operation `json:"operations"`
	Extensions     []any       `json:"extensions"`
	Signatures     []string    `json:"signatures,omitempty"`
}

// DynamicProps is the subset of get_dynamic_global_properties the signer uses.
type DynamicProps struct {
	HeadBlockNumber       uint32 `json:"head_block_number"`
	HeadBlockID           string `json:"head_block_id"`
	Time                  string `json:"time"`
	TotalVestingShares    string `json:"total_vesting_shares"`
	TotalVestingFundSteem string `json:"total_vesting_fund_steem"`
}

// NewTransaction references the head block and expires ttl after the head
// block time. Proposals use 58 minutes; Steem rejects anything over 60.
func NewTransaction(p DynamicProps, ops []Operation, ttl time.Duration) (Transaction, error) {
	id, err := hex.DecodeString(p.HeadBlockID)
	if err != nil {
		return Transaction{}, err
	}
	if len(id) < 8 {
		return Transaction{}, errors.New("steemtx: head_block_id too short")
	}
	head, err := time.Parse(TimeLayout, p.Time)
	if err != nil {
		return Transaction{}, err
	}
	return Transaction{
		RefBlockNum:    uint16(p.HeadBlockNumber & 0xffff),
		RefBlockPrefix: binary.LittleEndian.Uint32(id[4:8]),
		Expiration:     head.Add(ttl).UTC().Format(TimeLayout),
		Operations:     ops,
		Extensions:     []any{},
	}, nil
}

// Serialize encodes the transaction without signatures.
func (tx Transaction) Serialize() ([]byte, error) {
	var w Writer
	w.U16(tx.RefBlockNum)
	w.U32(tx.RefBlockPrefix)
	if err := w.Time(tx.Expiration); err != nil {
		return nil, err
	}
	w.Varint(uint32(len(tx.Operations)))
	for _, op := range tx.Operations {
		if err := w.Operation(op); err != nil {
			return nil, err
		}
	}
	w.Varint(0) // extensions
	return w.Bytes(), nil
}

// Digest is what validators sign: sha256(chain_id || serialized tx).
func (tx Transaction) Digest() ([32]byte, error) {
	b, err := tx.Serialize()
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(append(append([]byte{}, ChainID...), b...)), nil
}

// ID is the Steem trx_id and the proposal ID: hex(sha256(serialized)[:20]).
func (tx Transaction) ID() (string, error) {
	b, err := tx.Serialize()
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:20]), nil
}
