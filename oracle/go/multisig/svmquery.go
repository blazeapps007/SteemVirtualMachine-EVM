package multisig

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cosmos/cosmos-sdk/client"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	bridgetypes "steemvm/x/oracle/bridge/types"
)

// QuerySVM implements SVM over the node's ABCI query API. clientCtx must carry
// a node client; historical reads use clientCtx.WithHeight.
type QuerySVM struct {
	ClientCtx client.Context
	// Operator is this oracle's validator operator address bytes (same bytes
	// as its account address).
	Operator sdk.ValAddress
}

func (q QuerySVM) at(height int64) client.Context {
	if height > 0 {
		return q.ClientCtx.WithHeight(height)
	}
	return q.ClientCtx
}

func (q QuerySVM) Height(ctx context.Context) (int64, error) {
	st, err := q.ClientCtx.Client.Status(ctx)
	if err != nil {
		return 0, err
	}
	return st.SyncInfo.LatestBlockHeight, nil
}

func (q QuerySVM) WithdrawalTimeoutBlocks(ctx context.Context) (uint64, error) {
	res, err := bridgetypes.NewQueryClient(q.ClientCtx).Params(ctx, &bridgetypes.QueryParamsRequest{})
	if err != nil {
		return 0, err
	}
	return res.Params.WithdrawalTimeoutBlocks, nil
}

func toWithdrawal(w bridgetypes.Withdrawal) Withdrawal {
	sym := "STEEM"
	if w.Asset == bridgetypes.BridgeAsset_BRIDGE_ASSET_SBD {
		sym = "SBD"
	}
	status := strings.TrimPrefix(w.Status.String(), "WITHDRAWAL_STATUS_")
	return Withdrawal{ID: w.Id, Destination: w.DestinationSteemAccount, AmountMilli: w.AmountMillisteem,
		Symbol: sym, CreatedAt: w.CreatedAt, Status: status}
}

func (q QuerySVM) RequestedWithdrawals(ctx context.Context) ([]Withdrawal, error) {
	qc := bridgetypes.NewQueryClient(q.ClientCtx)
	var out []Withdrawal
	var key []byte
	for {
		res, err := qc.RequestedWithdrawals(ctx, &bridgetypes.QueryRequestedWithdrawalsRequest{
			Pagination: &query.PageRequest{Key: key, Limit: 200},
		})
		if err != nil {
			return nil, err
		}
		for _, w := range res.Withdrawals {
			out = append(out, toWithdrawal(w))
		}
		if res.Pagination == nil || len(res.Pagination.NextKey) == 0 {
			return out, nil
		}
		key = res.Pagination.NextKey
	}
}

func (q QuerySVM) Withdrawal(ctx context.Context, id uint64) (Withdrawal, bool, error) {
	res, err := bridgetypes.NewQueryClient(q.ClientCtx).GetWithdrawal(ctx, &bridgetypes.QueryGetWithdrawalRequest{Id: id})
	if err != nil {
		if isKeyNotFound(err) {
			return Withdrawal{}, false, nil
		}
		return Withdrawal{}, false, err
	}
	return toWithdrawal(res.Withdrawal), true, nil
}

// isKeyNotFound matches the bridge queries' sdkerrors.ErrKeyNotFound ("key not
// found") as it arrives over ABCI query, and nothing broader: a pruned height
// or a transport error must stay an error, not read as "absent".
func isKeyNotFound(err error) bool { return strings.Contains(err.Error(), "key not found") }

// ownerOfName resolves an ACTIVE name link to its address bytes at a height.
func (q QuerySVM) ownerOfName(ctx context.Context, cc client.Context, name string) ([]byte, bool, error) {
	res, err := bridgetypes.NewQueryClient(cc).ResolveName(ctx, &bridgetypes.QueryResolveNameRequest{SteemAccount: name})
	if err != nil {
		if isKeyNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	addr, err := sdk.AccAddressFromBech32(res.Name.Address)
	if err != nil {
		return nil, false, nil
	}
	return addr, true, nil
}

func (q QuerySVM) ValidatorStakes(ctx context.Context, height int64) ([]StakeEntry, error) {
	cc := q.at(height)
	sq := stakingtypes.NewQueryClient(cc)
	var out []StakeEntry
	var key []byte
	for {
		res, err := sq.Validators(ctx, &stakingtypes.QueryValidatorsRequest{
			Status:     stakingtypes.BondStatusBonded,
			Pagination: &query.PageRequest{Key: key, Limit: 200},
		})
		if err != nil {
			return nil, err
		}
		for _, val := range res.Validators {
			if val.Jailed || !val.Tokens.IsPositive() {
				continue
			}
			name := strings.TrimSpace(val.Description.Moniker)
			if name == "" {
				continue
			}
			op, err := sdk.ValAddressFromBech32(val.OperatorAddress)
			if err != nil {
				continue
			}
			owner, ok, err := q.ownerOfName(ctx, cc, name)
			if err != nil {
				return nil, err
			}
			if !ok || !bytes.Equal(owner, op.Bytes()) {
				continue // moniker is not an ACTIVE name link owned by this operator
			}
			out = append(out, StakeEntry{Account: name, Tokens: val.Tokens.BigInt()})
		}
		if res.Pagination == nil || len(res.Pagination.NextKey) == 0 {
			return out, nil
		}
		key = res.Pagination.NextKey
	}
}

func (q QuerySVM) SelfSteemAccount(ctx context.Context) (string, error) {
	res, err := stakingtypes.NewQueryClient(q.ClientCtx).Validator(ctx, &stakingtypes.QueryValidatorRequest{ValidatorAddr: q.Operator.String()})
	if err != nil {
		return "", fmt.Errorf("this oracle's key is not a validator operator: %w", err)
	}
	name := strings.TrimSpace(res.Validator.Description.Moniker)
	owner, ok, err := q.ownerOfName(ctx, q.ClientCtx, name)
	if err != nil {
		return "", err
	}
	if !ok || !bytes.Equal(owner, q.Operator.Bytes()) {
		return "", errors.New("validator moniker " + name + " is not an ACTIVE Steem name link owned by this operator")
	}
	return name, nil
}
