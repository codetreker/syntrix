package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFilterOp_IsValid(t *testing.T) {
	tests := []struct {
		name string
		op   FilterOp
		want bool
	}{
		{"OpEq", OpEq, true},
		{"OpNe", OpNe, true},
		{"OpGt", OpGt, true},
		{"OpGte", OpGte, true},
		{"OpLt", OpLt, true},
		{"OpLte", OpLte, true},
		{"OpIn", OpIn, true},
		{"OpContains", OpContains, true},
		{"Invalid", FilterOp("invalid"), false},
		{"Empty", FilterOp(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Ensure timeout mechanism as requested
			done := make(chan bool)
			go func() {
				assert.Equal(t, tt.want, tt.op.IsValid())
				done <- true
			}()
			select {
			case <-done:
			case <-time.After(100 * time.Millisecond):
				t.Fatal("test timed out")
			}
		})
	}
}

func TestValidOps(t *testing.T) {
	done := make(chan bool)
	go func() {
		ops := ValidOps()
		assert.Len(t, ops, 8)
		assert.Contains(t, ops, OpEq)
		assert.Contains(t, ops, OpNe)
		assert.Contains(t, ops, OpGt)
		assert.Contains(t, ops, OpGte)
		assert.Contains(t, ops, OpLt)
		assert.Contains(t, ops, OpLte)
		assert.Contains(t, ops, OpIn)
		assert.Contains(t, ops, OpContains)
		done <- true
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("test timed out")
	}
}

func TestFilter_Validate(t *testing.T) {
	tests := []struct {
		name string
		f    Filter
		want bool
	}{
		{"Valid Eq", Filter{Field: "a", Op: OpEq, Value: 1}, true},
		{"Valid In", Filter{Field: "b", Op: OpIn, Value: []int{1}}, true},
		{"Missing Field", Filter{Op: OpEq, Value: 1}, false},
		{"Invalid Op", Filter{Field: "c", Op: "bad", Value: 1}, false},
		{"Empty Op", Filter{Field: "d", Op: "", Value: 1}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			done := make(chan bool)
			go func() {
				assert.Equal(t, tt.want, tt.f.Validate())
				done <- true
			}()
			select {
			case <-done:
			case <-time.After(100 * time.Millisecond):
				t.Fatal("test timed out")
			}
		})
	}
}
