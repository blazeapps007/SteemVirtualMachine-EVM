package relayer

import (
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"steemvm/x/steembridge/types"
)

// Intent is what a gateway transfer's memo asks the bridge to do.
type Intent int

const (
	// IntentDeposit mints bridged STEEM ("svm-deposit <address>" or a bare
	// address memo — the historical deposit format).
	IntentDeposit Intent = iota
	// IntentRegister links the sender's Steem account name to the memo
	// address ("svm-register <address>").
	IntentRegister
)

// RouteMemo classifies a gateway transfer by its memo prefix. Everything
// that is not explicitly a name registration is attested as a deposit — the
// chain itself decides claimability from the memo, and unparseable-memo
// deposits become UNCLAIMABLE with a full audit trail rather than being
// silently dropped by validators.
func RouteMemo(memo string) Intent {
	trimmed := strings.TrimSpace(memo)
	if trimmed == "svm-register" || strings.HasPrefix(trimmed, "svm-register ") || strings.HasPrefix(trimmed, "svm-register\t") {
		return IntentRegister
	}
	return IntentDeposit
}

// BuildMsg converts a scanned transfer into the attestation message this
// validator should broadcast. All Steem-side facts are passed through
// verbatim (including the memo — the chain strips intent prefixes itself in
// DeriveDestination, so every validator's submission stays byte-identical).
func BuildMsg(t Transfer, intent Intent, validator sdk.AccAddress, gateway string) sdk.Msg {
	switch intent {
	case IntentRegister:
		return &types.MsgSubmitNameRegistration{
			Validator:        validator.String(),
			Txid:             t.Txid,
			OpIndex:          t.OpIndex,
			SteemBlock:       t.SteemBlock,
			SteemTimestamp:   t.SteemTimestamp,
			SteemAccount:     t.From,
			GatewayAccount:   gateway,
			AmountMillisteem: t.AmountMillisteem,
			Memo:             t.Memo,
		}
	default:
		return &types.MsgSubmitSteemDeposit{
			Validator:        validator.String(),
			Txid:             t.Txid,
			OpIndex:          t.OpIndex,
			SteemBlock:       t.SteemBlock,
			SteemTimestamp:   t.SteemTimestamp,
			SteemSender:      t.From,
			GatewayAccount:   gateway,
			AmountMillisteem: t.AmountMillisteem,
			Memo:             t.Memo,
		}
	}
}
