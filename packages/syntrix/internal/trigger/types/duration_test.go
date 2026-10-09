package types

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestDuration_JSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{
			name:  "String duration",
			input: `"10s"`,
			want:  10 * time.Second,
		},
		{
			name:  "String duration ms",
			input: `"500ms"`,
			want:  500 * time.Millisecond,
		},
		{
			name:  "Numeric duration (ns)",
			input: `1000000000`,
			want:  1 * time.Second,
		},
		{
			name:    "Invalid string",
			input:   `"invalid"`,
			wantErr: true,
		},
		{
			name:    "Invalid type",
			input:   `true`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d Duration
			err := json.Unmarshal([]byte(tt.input), &d)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, time.Duration(d))
			}
		})
	}
}

func TestDuration_MarshalJSON(t *testing.T) {
	d := Duration(5 * time.Second)
	b, err := json.Marshal(d)
	require.NoError(t, err)
	assert.Equal(t, `"5s"`, string(b))
}

func TestDuration_YAML(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{
			name:  "String duration",
			input: `10s`,
			want:  10 * time.Second,
		},
		{
			name:  "Numeric duration (ns)",
			input: `1000000000`,
			want:  1 * time.Second,
		},
		{
			name:    "Invalid string",
			input:   `invalid`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d Duration
			err := yaml.Unmarshal([]byte(tt.input), &d)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, time.Duration(d))
			}
		})
	}
}
