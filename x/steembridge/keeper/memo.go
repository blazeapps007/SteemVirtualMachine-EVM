package keeper

import (
	"fmt"
	"regexp"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/common"

	"steemvm/x/steembridge/types"
)

// hexAddressRegex matches a 0x-prefixed 20-byte EVM address, any case
// (EIP-55 checksummed or not).
var hexAddressRegex = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// DeriveDestination parses a bridge deposit memo and derives the destination
// account and its type. This is the single, canonical in-consensus derivation
// path referenced throughout the module design: client-side derivation is
// deliberately never trusted, since any divergence between client
// implementations (address conversion, normalization) would stall deposits
// at threshold forever.
//
// Supported formats, after trimming leading/trailing whitespace and an
// optional intent-prefix token ("svm-deposit" or "svm-register", used by the
// validator relayer to route transfers):
//   - a bech32 Cosmos address using the chain's configured account prefix
//   - a "0x" + 40 hex character EVM address
//
// A "0x..." memo derives the SAME underlying account as its 20 address bytes
// interpreted directly as an AccAddress, so minted funds are visible both as
// a Cosmos account and via eth_getBalance at the 0x address, with no erc20
// detour required.
//
// Anything else is unparseable: ok is false, and the caller must not mint —
// see the module's UNCLAIMABLE handling.
func DeriveDestination(memo string) (destAddr sdk.AccAddress, destType types.DestinationType, ok bool) {
	trimmed := strings.TrimSpace(memo)

	// Strip a single leading intent-prefix token, if present. The prefix is
	// only routing metadata for validators' relayers; the address that
	// follows it is the destination. "svm-deposit" alone (no address) falls
	// through to the unparseable path below.
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
		return addr, types.DestinationType_DESTINATION_TYPE_COSMOS, true
	}

	if hexAddressRegex.MatchString(trimmed) {
		return sdk.AccAddress(common.HexToAddress(trimmed).Bytes()), types.DestinationType_DESTINATION_TYPE_EVM, true
	}

	return nil, types.DestinationType_DESTINATION_TYPE_NONE, false
}

// parseAddressArg decodes an account address supplied as a query/CLI argument,
// accepting the chain's account bech32 form (steem1...), the validator operator
// form (steemvaloper...), or a 0x-prefixed EVM hex address — all map to the same
// 20 underlying bytes. Mirrors DeriveDestination's dual-format handling so a
// caller can paste whichever view they have.
func (k Keeper) parseAddressArg(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if hexAddressRegex.MatchString(s) {
		return common.HexToAddress(s).Bytes(), nil
	}
	if addr, err := k.addressCodec.StringToBytes(s); err == nil {
		return addr, nil
	}
	// Fall back to the validator operator (steemvaloper...) form; same 20 bytes.
	if valAddr, err := sdk.ValAddressFromBech32(s); err == nil {
		return valAddr.Bytes(), nil
	}
	return nil, fmt.Errorf("invalid address %q: not a bech32 account, validator operator, or 0x hex address", s)
}
