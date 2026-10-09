// Package cel provides CEL expression compilation and evaluation for the Streamer service.
package cel

import (
	"fmt"
	"strings"

	pb "github.com/codetreker/syntrix/api/gen/streamer/v1"
	"github.com/codetreker/syntrix/internal/streamer/manager"
	"github.com/codetreker/syntrix/pkg/model"
	"github.com/google/cel-go/cel"
)

// Compiler compiles CEL expressions for subscription matching.
type Compiler struct {
	env *cel.Env
}

// NewCompiler creates a new CEL compiler with the standard environment.
func NewCompiler() (*Compiler, error) {
	env, _ := cel.NewEnv(
		cel.Variable("doc", cel.MapType(cel.StringType, cel.DynType)),
	)
	return &Compiler{env: env}, nil
}

// Compile compiles a CEL expression string into an ExpressionSubscriber.
func (c *Compiler) Compile(expr string) (*manager.ExpressionSubscriber, error) {
	ast, issues := c.env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("CEL compile error: %w", issues.Err())
	}

	prg, err := c.env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("CEL program creation error: %w", err)
	}

	return &manager.ExpressionSubscriber{
		Program: prg,
	}, nil
}

// CompileFilters compiles a slice of model.Filter into a CEL expression.
// This is used to convert the existing Filter-based subscription format.
func (c *Compiler) CompileFilters(filters []model.Filter) (cel.Program, error) {
	if len(filters) == 0 {
		return nil, nil
	}

	var expressions []string
	for _, f := range filters {
		expr, err := filterToExpression(f)
		if err != nil {
			return nil, err
		}
		expressions = append(expressions, expr)
	}

	fullExpr := strings.Join(expressions, " && ")
	return c.CompileExpression(fullExpr)
}

// CompileExpression compiles a CEL expression string.
func (c *Compiler) CompileExpression(expr string) (cel.Program, error) {
	ast, issues := c.env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("CEL compile error: %w", issues.Err())
	}

	prg, err := c.env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("CEL program creation error: %w", err)
	}

	return prg, nil
}

// Evaluate evaluates a compiled CEL program against document data.
func Evaluate(prg cel.Program, docData map[string]interface{}) (bool, error) {
	if prg == nil {
		return true, nil // No filter = match all
	}

	out, _, err := prg.Eval(map[string]interface{}{
		"doc": docData,
	})
	if err != nil {
		return false, err
	}

	result, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("CEL result is not boolean: %T", out.Value())
	}

	return result, nil
}

// filterToExpression converts a model.Filter to a CEL expression string.
func filterToExpression(f model.Filter) (string, error) {
	valStr, err := formatValue(f.Value)
	if err != nil {
		return "", err
	}

	field := "doc"
	parts := strings.Split(f.Field, ".")
	for _, p := range parts {
		field += fmt.Sprintf("['%s']", p)
	}

	switch f.Op {
	case model.OpEq:
		return fmt.Sprintf("%s == %s", field, valStr), nil
	case model.OpNe:
		return fmt.Sprintf("%s != %s", field, valStr), nil
	case model.OpGt:
		return fmt.Sprintf("%s > %s", field, valStr), nil
	case model.OpGte:
		return fmt.Sprintf("%s >= %s", field, valStr), nil
	case model.OpLt:
		return fmt.Sprintf("%s < %s", field, valStr), nil
	case model.OpLte:
		return fmt.Sprintf("%s <= %s", field, valStr), nil
	case model.OpIn:
		return fmt.Sprintf("%s in %s", field, valStr), nil
	case model.OpContains:
		return fmt.Sprintf("%s in %s", valStr, field), nil
	default:
		return "", fmt.Errorf("unsupported operator: %s", f.Op)
	}
}

// formatValue formats a value for use in a CEL expression.
func formatValue(v interface{}) (string, error) {
	switch val := v.(type) {
	case string:
		return fmt.Sprintf("'%s'", strings.ReplaceAll(val, "'", "\\'")), nil
	case int:
		return fmt.Sprintf("%d", val), nil
	case int32:
		return fmt.Sprintf("%d", val), nil
	case int64:
		return fmt.Sprintf("%d", val), nil
	case float32:
		return fmt.Sprintf("%v", val), nil
	case float64:
		return fmt.Sprintf("%v", val), nil
	case bool:
		return fmt.Sprintf("%v", val), nil
	case []interface{}:
		var parts []string
		for _, item := range val {
			s, err := formatValue(item)
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		return fmt.Sprintf("[%s]", strings.Join(parts, ", ")), nil
	default:
		return "", fmt.Errorf("unsupported value type: %T", v)
	}
}

// CompileProtoFilters compiles a slice of proto Filter messages into an ExpressionSubscriber.
// This provides a type-safe API while using CEL for the underlying matching.
func (c *Compiler) CompileProtoFilters(filters []*pb.Filter) (*manager.ExpressionSubscriber, error) {
	if len(filters) == 0 {
		return &manager.ExpressionSubscriber{Program: nil}, nil
	}

	var expressions []string
	for _, f := range filters {
		expr, err := protoFilterToExpression(f)
		if err != nil {
			return nil, err
		}
		expressions = append(expressions, expr)
	}

	fullExpr := strings.Join(expressions, " && ")
	prg, err := c.CompileExpression(fullExpr)
	if err != nil {
		return nil, err
	}
	return &manager.ExpressionSubscriber{Program: prg}, nil
}

// protoFilterToExpression converts a proto Filter to a CEL expression string.
func protoFilterToExpression(f *pb.Filter) (string, error) {
	if f == nil {
		return "", fmt.Errorf("filter is nil")
	}

	valStr, err := formatProtoValue(f.Value)
	if err != nil {
		return "", err
	}

	field := "doc"
	parts := strings.Split(f.Field, ".")
	for _, p := range parts {
		field += fmt.Sprintf("['%s']", p)
	}

	switch f.Op {
	case "==":
		return fmt.Sprintf("%s == %s", field, valStr), nil
	case "!=":
		return fmt.Sprintf("%s != %s", field, valStr), nil
	case ">":
		return fmt.Sprintf("%s > %s", field, valStr), nil
	case ">=":
		return fmt.Sprintf("%s >= %s", field, valStr), nil
	case "<":
		return fmt.Sprintf("%s < %s", field, valStr), nil
	case "<=":
		return fmt.Sprintf("%s <= %s", field, valStr), nil
	case "in":
		return fmt.Sprintf("%s in %s", field, valStr), nil
	case "contains":
		return fmt.Sprintf("%s in %s", valStr, field), nil
	default:
		return "", fmt.Errorf("unsupported operator: %s", f.Op)
	}
}

// formatProtoValue formats a proto Value for use in a CEL expression.
func formatProtoValue(v *pb.Value) (string, error) {
	if v == nil {
		return "", fmt.Errorf("value is nil")
	}

	switch k := v.Kind.(type) {
	case *pb.Value_StringValue:
		return fmt.Sprintf("'%s'", strings.ReplaceAll(k.StringValue, "'", "\\'")), nil
	case *pb.Value_IntValue:
		return fmt.Sprintf("%d", k.IntValue), nil
	case *pb.Value_DoubleValue:
		return fmt.Sprintf("%v", k.DoubleValue), nil
	case *pb.Value_BoolValue:
		return fmt.Sprintf("%v", k.BoolValue), nil
	case *pb.Value_ListValue:
		if k.ListValue == nil {
			return "[]", nil
		}
		var parts []string
		for _, item := range k.ListValue.Values {
			s, err := formatProtoValue(item)
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		return fmt.Sprintf("[%s]", strings.Join(parts, ", ")), nil
	default:
		return "", fmt.Errorf("unsupported proto value type: %T", v.Kind)
	}
}
