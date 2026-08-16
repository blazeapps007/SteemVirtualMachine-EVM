package keeper

import (
	"fmt"

	"cosmossdk.io/collections"
	"cosmossdk.io/core/address"
	corestore "cosmossdk.io/core/store"
	"github.com/cosmos/cosmos-sdk/codec"

	"steemvm/x/oracle/data/types"
)

type Keeper struct {
	storeService corestore.KVStoreService
	cdc          codec.Codec
	addressCodec address.Codec
	// Address capable of executing a MsgUpdateParams message.
	// Typically, this should be the x/gov module account.
	authority []byte

	stakingKeeper types.StakingKeeper
	// oracleKeeper is the parent engine this module reports price participation
	// to. It may be nil (standalone/tests); reporting is skipped when so.
	oracleKeeper types.OracleKeeper

	Schema collections.Schema
	Params collections.Item[types.Params]

	// ExchangeRate maps a pair -> its finalized ExchangeRate.
	ExchangeRate collections.Map[string, types.ExchangeRate]
	// Prevote maps a validator operator address (bytes) -> its committed prevote.
	Prevote collections.Map[[]byte, types.AggregateExchangeRatePrevote]
	// Vote maps a validator operator address (bytes) -> its revealed vote.
	Vote collections.Map[[]byte, types.AggregateExchangeRateVote]
}

func NewKeeper(
	storeService corestore.KVStoreService,
	cdc codec.Codec,
	addressCodec address.Codec,
	authority []byte,

	stakingKeeper types.StakingKeeper,
	oracleKeeper types.OracleKeeper,
) Keeper {
	if _, err := addressCodec.BytesToString(authority); err != nil {
		panic(fmt.Sprintf("invalid authority address %s: %s", authority, err))
	}

	sb := collections.NewSchemaBuilder(storeService)

	k := Keeper{
		storeService: storeService,
		cdc:          cdc,
		addressCodec: addressCodec,
		authority:    authority,

		stakingKeeper: stakingKeeper,
		oracleKeeper:  oracleKeeper,

		Params: collections.NewItem(sb, types.ParamsKey, "params", codec.CollValue[types.Params](cdc)),
		ExchangeRate: collections.NewMap(
			sb, types.ExchangeRateKey, "exchange_rate",
			collections.StringKey, codec.CollValue[types.ExchangeRate](cdc),
		),
		Prevote: collections.NewMap(
			sb, types.PrevoteKey, "prevote",
			collections.BytesKey, codec.CollValue[types.AggregateExchangeRatePrevote](cdc),
		),
		Vote: collections.NewMap(
			sb, types.VoteKey, "vote",
			collections.BytesKey, codec.CollValue[types.AggregateExchangeRateVote](cdc),
		),
	}

	schema, err := sb.Build()
	if err != nil {
		panic(err)
	}
	k.Schema = schema

	return k
}

// GetAuthority returns the module's authority.
func (k Keeper) GetAuthority() []byte {
	return k.authority
}
