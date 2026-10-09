package recovery

import (
	"testing"
)

func TestEqualsIgnoreCase_UpperCase(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"abc", "abc", true},
		{"ABC", "abc", true}, // Covers ca uppercase
		{"abc", "ABC", true}, // Covers cb uppercase
		{"AbC", "aBc", true}, // Mixed
		{"abc", "abd", false},
		{"abc", "abcd", false},
	}

	for _, tt := range tests {
		if got := equalsIgnoreCase(tt.a, tt.b); got != tt.want {
			t.Errorf("equalsIgnoreCase(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
