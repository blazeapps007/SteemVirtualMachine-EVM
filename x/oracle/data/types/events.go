package types

// Event types emitted by the oracledata module.
const (
	EventTypeAggregatePrevote  = "aggregate_prevote"
	EventTypeAggregateVote     = "aggregate_vote"
	EventTypeExchangeRateUpdate = "exchange_rate_update"
	EventTypeParametersUpdated = "parameters_updated"
)

// Event attribute keys emitted by the oracledata module.
const (
	AttributeKeyValidator    = "validator"
	AttributeKeyHash         = "hash"
	AttributeKeyPair         = "pair"
	AttributeKeyExchangeRate = "exchange_rate"
	AttributeKeyEpoch        = "epoch"
)
