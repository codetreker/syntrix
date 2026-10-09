package model

// FilterOp defines the supported filter operators.
type FilterOp string

const (
	OpEq       FilterOp = "=="       // Equal
	OpNe       FilterOp = "!="       // Not equal
	OpGt       FilterOp = ">"        // Greater than
	OpGte      FilterOp = ">="       // Greater than or equal
	OpLt       FilterOp = "<"        // Less than
	OpLte      FilterOp = "<="       // Less than or equal
	OpIn       FilterOp = "in"       // Value in array
	OpContains FilterOp = "contains" // Array contains value
)

// ValidOps returns all valid filter operators.
func ValidOps() []FilterOp {
	return []FilterOp{OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpContains}
}

// IsValid checks if the operator is valid.
func (op FilterOp) IsValid() bool {
	switch op {
	case OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpContains:
		return true
	}
	return false
}

// Filters is a slice of Filter.
type Filters []Filter

// Filter represents a query filter
type Filter struct {
	Field string      `json:"field"`
	Op    FilterOp    `json:"op"`
	Value interface{} `json:"value"`
}

// Validate checks if the filter is valid.
func (f Filter) Validate() bool {
	if f.Field == "" {
		return false
	}
	return f.Op.IsValid()
}
