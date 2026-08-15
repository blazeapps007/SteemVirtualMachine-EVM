package types

// Event types emitted by the parent x/oracle module.
const (
	EventTypeParametersUpdated = "parameters_updated"
	// EventTypeOracleSlash is emitted once per validator slashed at a window boundary.
	EventTypeOracleSlash = "oracle_slash"
)

// Event attribute keys emitted by the parent x/oracle module.
const (
	AttributeKeyValidator = "validator"
	AttributeKeyReason    = "reason"
	AttributeKeyScore     = "score"
)
