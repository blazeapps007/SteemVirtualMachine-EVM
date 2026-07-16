package types

import (
	"fmt"
	"strings"

	"github.com/btcsuite/btcd/btcutil/base58"
)

// ValidatorIdentity is the Steem identity a validator must supply to create (or
// keep) a validator. It binds the validator's operator account to a registered
// Steem username and records the account's three Steem authority public keys.
// The username is the validator's staking Description.moniker; the three keys
// are packed into Description.details. Both are persisted by x/staking, so no
// separate module state is needed.
type ValidatorIdentity struct {
	SteemUsername string
	OwnerPubkey   string
	ActivePubkey  string
	PostingPubkey string
}

// The details blob is a compact, order-independent list of key=value pairs
// separated by ';', carrying the three Steem public keys, e.g.
//
//	owner=STM8...;active=STM7...;posting=STM6...
//
// The Steem username is NOT here — it is the validator's moniker.
const (
	identityKeyOwner   = "owner"
	identityKeyActive  = "active"
	identityKeyPosting = "posting"

	// steemPubkeyPrefix is the network prefix on every Steem public key.
	steemPubkeyPrefix = "STM"
	// steemPubkeyDecodedLen is the byte length of a Steem public key's base58
	// body: a 33-byte compressed secp256k1 point + a 4-byte checksum.
	steemPubkeyDecodedLen = 37
)

// ParseValidatorIdentity builds a ValidatorIdentity from a validator's staking
// moniker (the Steem username) and its Description.details (the three Steem
// public keys) and validates it. All four fields are mandatory; each public key
// must pass ValidateSteemPublicKey.
func ParseValidatorIdentity(moniker, details string) (ValidatorIdentity, error) {
	id := ValidatorIdentity{SteemUsername: strings.TrimSpace(moniker)}
	seen := make(map[string]bool, 3)

	for _, part := range strings.Split(details, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			return ValidatorIdentity{}, fmt.Errorf("malformed identity segment %q (expected key=value)", part)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if seen[key] {
			return ValidatorIdentity{}, fmt.Errorf("duplicate identity key %q", key)
		}
		seen[key] = true
		switch key {
		case identityKeyOwner:
			id.OwnerPubkey = val
		case identityKeyActive:
			id.ActivePubkey = val
		case identityKeyPosting:
			id.PostingPubkey = val
		default:
			return ValidatorIdentity{}, fmt.Errorf("unknown identity key %q (details holds only owner/active/posting)", key)
		}
	}

	if err := id.Validate(); err != nil {
		return ValidatorIdentity{}, err
	}
	return id, nil
}

// Validate checks that the username and all three keys are present and each
// public key is a well-formed Steem key. It does not (and cannot, at consensus)
// verify that the keys actually belong to the Steem account — that is an
// off-chain trust point.
func (id ValidatorIdentity) Validate() error {
	if id.SteemUsername == "" {
		return fmt.Errorf("missing steem username (validator moniker)")
	}
	for _, kv := range []struct {
		name string
		key  string
	}{
		{identityKeyOwner, id.OwnerPubkey},
		{identityKeyActive, id.ActivePubkey},
		{identityKeyPosting, id.PostingPubkey},
	} {
		if kv.key == "" {
			return fmt.Errorf("missing %q public key", kv.name)
		}
		if err := ValidateSteemPublicKey(kv.key); err != nil {
			return fmt.Errorf("invalid %q public key: %w", kv.name, err)
		}
	}
	return nil
}

// ValidateSteemPublicKey checks that s is a syntactically valid Steem public
// key: the "STM" prefix followed by a base58-encoded 37-byte body (33-byte
// compressed key + 4-byte checksum). base58.Decode returns nil for input
// containing non-base58 characters, so a wrong alphabet fails the length check.
func ValidateSteemPublicKey(s string) error {
	body, ok := strings.CutPrefix(s, steemPubkeyPrefix)
	if !ok {
		return fmt.Errorf("missing %q prefix", steemPubkeyPrefix)
	}
	decoded := base58.Decode(body)
	if len(decoded) != steemPubkeyDecodedLen {
		return fmt.Errorf("base58 body decodes to %d bytes, want %d", len(decoded), steemPubkeyDecodedLen)
	}
	return nil
}
