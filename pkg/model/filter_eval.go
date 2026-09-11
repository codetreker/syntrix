package model

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidateQueryField admits literal top-level fields and envelope metadata.
func ValidateQueryField(field string) error {
	if field == "" || !utf8.ValidString(field) || strings.Contains(field, ".") || strings.IndexFunc(field, unicode.IsControl) >= 0 {
		return fmt.Errorf("invalid top-level query field %q", field)
	}
	return nil
}

func NormalizeFilters(filters Filters) (Filters, error) {
	out := make(Filters, len(filters))
	for i, filter := range filters {
		if err := ValidateQueryField(filter.Field); err != nil {
			return nil, fmt.Errorf("filter %d: %w", i, err)
		}
		if !filter.Op.IsValid() {
			return nil, fmt.Errorf("filter %d: invalid operator %q", i, filter.Op)
		}
		value, err := NormalizeValue(filter.Value)
		if err != nil {
			return nil, fmt.Errorf("filter %d: %w", i, err)
		}
		switch filter.Op {
		case OpIn:
			items, ok := value.([]any)
			if !ok {
				return nil, fmt.Errorf("filter %d: in requires an array", i)
			}
			unique := make([]any, 0, len(items))
			seen := make(map[string]bool, len(items))
			for _, item := range items {
				key, err := ScalarKey(item)
				if err != nil {
					return nil, fmt.Errorf("filter %d: in member: %w", i, err)
				}
				if !seen[string(key)] {
					unique = append(unique, item)
					seen[string(key)] = true
				}
			}
			value = unique
		case OpGt, OpGte, OpLt, OpLte:
			if !isRangeValue(value) {
				return nil, fmt.Errorf("filter %d: %s requires a number or string", i, filter.Op)
			}
		default:
			if _, err := ScalarKey(value); err != nil {
				return nil, fmt.Errorf("filter %d: %w", i, err)
			}
		}
		out[i] = Filter{Field: filter.Field, Op: filter.Op, Value: value}
	}
	return out, nil
}

func isRangeValue(value any) bool {
	switch value.(type) {
	case int64, float64, string:
		return true
	}
	return false
}

func sameRangeFamily(a, b any) bool {
	switch a.(type) {
	case string:
		_, ok := b.(string)
		return ok
	case int64, float64:
		switch b.(type) {
		case int64, float64:
			return true
		}
	}
	return false
}

// EvaluateFilters validates the entire conjunction before evaluating any field.
// The accessor must distinguish missing values from explicit null.
func EvaluateFilters(filters Filters, field func(string) (any, bool)) (bool, error) {
	normalized, err := NormalizeFilters(filters)
	if err != nil {
		return false, err
	}
	matched := true
	for _, filter := range normalized {
		value, present := field(filter.Field)
		result, err := evaluateNormalizedFilter(filter, value, present)
		if err != nil {
			return false, fmt.Errorf("field %q: %w", filter.Field, err)
		}
		matched = matched && result
	}
	return matched, nil
}

func EvaluateFilter(filter Filter, value any, present bool) (bool, error) {
	normalized, err := NormalizeFilters(Filters{filter})
	if err != nil {
		return false, err
	}
	return evaluateNormalizedFilter(normalized[0], value, present)
}

func evaluateNormalizedFilter(filter Filter, value any, present bool) (bool, error) {
	if !present {
		return false, nil
	}
	normalized, err := NormalizeValue(value)
	if err != nil {
		return false, err
	}
	switch filter.Op {
	case OpEq:
		return EqualValues(normalized, filter.Value), nil
	case OpNe:
		return !EqualValues(normalized, filter.Value), nil
	case OpIn:
		for _, candidate := range filter.Value.([]any) {
			if EqualValues(normalized, candidate) {
				return true, nil
			}
		}
		return false, nil
	case OpContains:
		items, ok := normalized.([]any)
		if !ok {
			return false, nil
		}
		for _, item := range items {
			if EqualValues(item, filter.Value) {
				return true, nil
			}
		}
		return false, nil
	case OpGt, OpGte, OpLt, OpLte:
		if !sameRangeFamily(normalized, filter.Value) {
			return false, nil
		}
		comparison, err := CompareValues(normalized, filter.Value)
		if err != nil {
			return false, err
		}
		switch filter.Op {
		case OpGt:
			return comparison > 0, nil
		case OpGte:
			return comparison >= 0, nil
		case OpLt:
			return comparison < 0, nil
		case OpLte:
			return comparison <= 0, nil
		}
	}
	return false, fmt.Errorf("invalid operator %q", filter.Op)
}
