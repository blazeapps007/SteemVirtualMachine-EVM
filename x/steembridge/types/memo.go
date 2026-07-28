package types

import (
	"regexp"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/common"
)

// hexAddressRegex matches a 0x-prefixed 20-byte EVM address, any case
// (EIP-55 checksummed or not).
var hexAddressRegex = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// DeriveDestination parses a bridge deposit memo and derives the destination
// account and its type. This is the single, canonical derivation path used
// both in consensus (deposit / name-registration resolution) and by the
// validator relayer to decide whether a gateway transfer is one it should
// attest at all — so the relayer's "supported memo" filter and the chain's
// claimability decision can never drift.
//
// Supported formats, after trimming leading/trailing whitespace and an
// optional intent-prefix token ("svm-deposit" or "svm-register"):
//   - a bech32 Cosmos address using the chain's configured account prefix
//   - a "0x" + 40 hex character EVM address
//
// A "0x..." memo derives the SAME underlying account as its 20 address bytes
// interpreted directly as an AccAddress, so minted funds are visible both as
// a Cosmos account and via eth_getBalance at the 0x address.
//
// Anything else is unparseable: ok is false. In consensus the caller must not
// mint (UNCLAIMABLE); in the relayer the transfer is simply not attested.
func DeriveDestination(memo string) (destAddr sdk.AccAddress, destType DestinationType, ok bool) {
	trimmed := strings.TrimSpace(memo)

	// Strip a single leading intent-prefix token, if present. The prefix is
	// only routing metadata; the address that follows it is the destination.
	// "svm-deposit" alone (no address) falls through to the unparseable path.
	for _, prefix := range []string{"svm-deposit", "svm-register"} {
		if rest, found := strings.CutPrefix(trimmed, prefix); found {
			// Require a separator so e.g. "svm-depositgarbage" stays unparseable.
			if rest == "" || rest[0] == ' ' || rest[0] == '\t' {
				trimmed = strings.TrimSpace(rest)
			}
			break
		}
	}

	if addr, err := sdk.AccAddressFromBech32(trimmed); err == nil {
		return addr, DestinationType_DESTINATION_TYPE_COSMOS, true
	}

	if hexAddressRegex.MatchString(trimmed) {
		return sdk.AccAddress(common.HexToAddress(trimmed).Bytes()), DestinationType_DESTINATION_TYPE_EVM, true
	}

	return nil, DestinationType_DESTINATION_TYPE_NONE, false
}
