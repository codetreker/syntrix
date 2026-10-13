package config

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

var cpuQuantity = regexp.MustCompile(`^([0-9]+m|[0-9]+(?:\.[0-9]{1,3})?)$`)
var memoryQuantity = regexp.MustCompile(`^([0-9]+)(Ki|Mi|Gi|Ti|Pi|Ei|k|M|G|T|P|E)?$`)

func (r Resources) Validate() error {
	for _, quantity := range []struct {
		field          string
		request, limit string
		parse          func(string) (*big.Rat, error)
	}{
		{"cpu", r.Requests.CPU, r.Limits.CPU, parseCPU},
		{"memory", r.Requests.Memory, r.Limits.Memory, parseMemory},
	} {
		request, err := quantity.parse(quantity.request)
		if err != nil {
			return fmt.Errorf("requests.%s: %w", quantity.field, err)
		}
		limit, err := quantity.parse(quantity.limit)
		if err != nil {
			return fmt.Errorf("limits.%s: %w", quantity.field, err)
		}
		if request.Cmp(limit) > 0 {
			return fmt.Errorf("requests.%s must not exceed limits.%s", quantity.field, quantity.field)
		}
	}
	return nil
}

func parseCPU(value string) (*big.Rat, error) {
	if !cpuQuantity.MatchString(value) {
		return nil, fmt.Errorf("must be positive integer millicores or decimal cores with at most three decimal places")
	}
	cores, _ := new(big.Rat).SetString(strings.TrimSuffix(value, "m"))
	if strings.HasSuffix(value, "m") {
		cores.Quo(cores, big.NewRat(1000, 1))
	}
	if cores.Sign() <= 0 {
		return nil, fmt.Errorf("must be positive")
	}
	return cores, nil
}

func parseMemory(value string) (*big.Rat, error) {
	parts := memoryQuantity.FindStringSubmatch(value)
	if parts == nil {
		return nil, fmt.Errorf("must be positive integer bytes with optional decimal k/M/G/T/P/E or binary Ki/Mi/Gi/Ti/Pi/Ei suffix")
	}
	bytes, _ := new(big.Int).SetString(parts[1], 10)
	if bytes.Sign() <= 0 {
		return nil, fmt.Errorf("must be positive")
	}
	if parts[2] != "" {
		base := int64(1000)
		unit := strings.TrimSuffix(parts[2], "i")
		if strings.HasSuffix(parts[2], "i") {
			base = 1024
		}
		exponent := strings.Index("kMGTPE", unit) + 1
		if unit == "K" {
			exponent = 1
		}
		multiplier := new(big.Int).Exp(big.NewInt(base), big.NewInt(int64(exponent)), nil)
		bytes.Mul(bytes, multiplier)
	}
	return new(big.Rat).SetInt(bytes), nil
}
