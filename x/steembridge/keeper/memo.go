package keeper

import (
	"fmt"
	"regexp"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/common"
)

// hexAddressRegex matches a 0x-prefixed 20-byte EVM address, any case
// (EIP-55 checksummed or not).
var hexAddressRegex = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// DeriveDestination has moved to the types package (types.DeriveDestination)
// so the validator relayer can reuse the exact same in-consensus parser for
// its "supported memo" filter. Callers in this package use types.DeriveDestination.

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
