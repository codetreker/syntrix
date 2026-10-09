package evaluator

import (
	"github.com/codetreker/syntrix/internal/trigger/evaluator/cel"
)

// Evaluator is the CEL-based trigger condition evaluator.
type Evaluator = cel.Evaluator

// NewEvaluator creates a new CEL evaluator.
var NewEvaluator = cel.NewEvaluator
