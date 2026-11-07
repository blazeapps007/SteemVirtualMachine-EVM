package steemtx

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
)

type vectorFile struct {
	Transactions []struct {
		Name       string          `json:"name"`
		Tx         json.RawMessage `json:"tx"`
		Serialized string          `json:"serialized"`
		Digest     string          `json:"digest"`
		ID         string          `json:"id"`
		Signatures []struct {
			Sig    string `json:"sig"`
			Pubkey string `json:"pubkey"`
		} `json:"signatures"`
	} `json:"transactions"`
	Keys []struct {
		WIF        string `json:"wif"`
		Pubkey     string `json:"pubkey"`
		Compressed string `json:"compressed"`
	} `json:"keys"`
	Assets []struct {
		Millisteem string `json:"millisteem"`
		Symbol     string `json:"symbol"`
		String     string `json:"string"`
	} `json:"assets"`
}

// Selftest runs the transaction, key, asset and signing vectors from
// oracle/multisig/vectors.json. The signer refuses to start unless it passes:
// a single wrong byte in serialization would make every signature invalid.
func Selftest(vectorsJSON []byte) error {
	var v vectorFile
	if err := json.Unmarshal(vectorsJSON, &v); err != nil {
		return fmt.Errorf("selftest: parse vectors: %w", err)
	}
	if len(v.Transactions) == 0 || len(v.Keys) == 0 {
		return fmt.Errorf("selftest: vectors file has no transactions or keys")
	}

	for _, tv := range v.Transactions {
		var tx Transaction
		if err := json.Unmarshal(tv.Tx, &tx); err != nil {
			return fmt.Errorf("selftest %q: parse tx: %w", tv.Name, err)
		}
		ser, err := tx.Serialize()
		if err != nil {
			return fmt.Errorf("selftest %q: serialize: %w", tv.Name, err)
		}
		if got := hex.EncodeToString(ser); got != tv.Serialized {
			return fmt.Errorf("selftest %q: serialized %s, want %s", tv.Name, got, tv.Serialized)
		}
		digest, _ := tx.Digest()
		if got := hex.EncodeToString(digest[:]); got != tv.Digest {
			return fmt.Errorf("selftest %q: digest %s, want %s", tv.Name, got, tv.Digest)
		}
		id, _ := tx.ID()
		if id != tv.ID {
			return fmt.Errorf("selftest %q: id %s, want %s", tv.Name, id, tv.ID)
		}
		// The stored JSON must round-trip to the same ID (other clients
		// recompute proposal IDs from the JSON).
		again, err := json.Marshal(tx)
		if err != nil {
			return fmt.Errorf("selftest %q: re-marshal: %w", tv.Name, err)
		}
		var tx2 Transaction
		if err := json.Unmarshal(again, &tx2); err != nil {
			return fmt.Errorf("selftest %q: re-parse: %w", tv.Name, err)
		}
		if id2, _ := tx2.ID(); id2 != tv.ID {
			return fmt.Errorf("selftest %q: JSON round-trip changed the id to %s", tv.Name, id2)
		}
		for i, s := range tv.Signatures {
			pub, err := RecoverSigner(digest, s.Sig)
			if err != nil {
				return fmt.Errorf("selftest %q: signature %d: %w", tv.Name, i, err)
			}
			if pub != s.Pubkey {
				return fmt.Errorf("selftest %q: signature %d recovers to %s, want %s", tv.Name, i, pub, s.Pubkey)
			}
		}
	}

	for _, kv := range v.Keys {
		key, err := DecodeWIF(kv.WIF)
		if err != nil {
			return fmt.Errorf("selftest key: %w", err)
		}
		if got := EncodePublicKey(key.PubKey()); got != kv.Pubkey {
			return fmt.Errorf("selftest key: pubkey %s, want %s", got, kv.Pubkey)
		}
		if got := hex.EncodeToString(key.PubKey().SerializeCompressed()); got != kv.Compressed {
			return fmt.Errorf("selftest key: compressed %s, want %s", got, kv.Compressed)
		}
		raw, err := DecodePublicKey(kv.Pubkey)
		if err != nil || hex.EncodeToString(raw) != kv.Compressed {
			return fmt.Errorf("selftest key: DecodePublicKey mismatch (%v)", err)
		}
		// Sign/recover round trips: every signature canonical, recovering to the key.
		for i := 0; i < 64; i++ {
			digest := sha256.Sum256([]byte("svm.bank selftest " + strconv.Itoa(i)))
			sig, err := Sign(key, digest)
			if err != nil {
				return fmt.Errorf("selftest sign %d: %w", i, err)
			}
			if sig[:2] != "1f" && sig[:2] != "20" {
				return fmt.Errorf("selftest sign %d: header %s, want 1f or 20", i, sig[:2])
			}
			pub, err := RecoverSigner(digest, sig)
			if err != nil || pub != kv.Pubkey {
				return fmt.Errorf("selftest sign %d: recovers to %s (%v), want %s", i, pub, err, kv.Pubkey)
			}
		}
	}

	for _, av := range v.Assets {
		milli, err := strconv.ParseUint(av.Millisteem, 10, 64)
		if err != nil {
			return fmt.Errorf("selftest asset: %w", err)
		}
		if got := FormatMilli(milli, av.Symbol); got != av.String {
			return fmt.Errorf("selftest asset: FormatMilli(%d, %s) = %q, want %q", milli, av.Symbol, got, av.String)
		}
		a, err := ParseAsset(av.String)
		if err != nil || a.Amount != int64(milli) || a.Symbol != av.Symbol || a.String() != av.String {
			return fmt.Errorf("selftest asset: ParseAsset(%q) = %+v (%v)", av.String, a, err)
		}
	}
	return nil
}
