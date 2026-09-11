package cursor

import (
	"encoding/base64"
	"encoding/json"
)

// ProgressMarker tracks consumer positions across all backends.
// This is an internal structure - consumers see it as an opaque string.
type ProgressMarker struct {
	// Positions maps backend name to last event ID for that backend.
	Positions map[string]string `json:"p"`
	Lineages  map[string]string `json:"l,omitempty"`
}

// NewProgressMarker creates an empty progress marker.
func NewProgressMarker() *ProgressMarker {
	return &ProgressMarker{
		Positions: make(map[string]string),
	}
}

// DecodeProgressMarker decodes a progress marker from its string representation.
// Returns an error if the string is invalid.
func DecodeProgressMarker(s string) (*ProgressMarker, error) {
	if s == "" {
		return NewProgressMarker(), nil
	}

	data, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}

	var pm ProgressMarker
	if err := json.Unmarshal(data, &pm); err != nil {
		return nil, err
	}

	if pm.Positions == nil {
		pm.Positions = make(map[string]string)
	}
	return &pm, nil
}

// Encode encodes the progress marker to a string.
func (pm *ProgressMarker) Encode() string {
	if pm == nil || len(pm.Positions) == 0 {
		return ""
	}
	data, _ := json.Marshal(pm)
	return base64.RawURLEncoding.EncodeToString(data)
}

// SetPosition updates the position for a backend.
func (pm *ProgressMarker) SetPosition(backend, eventID string) {
	if pm.Positions == nil {
		pm.Positions = make(map[string]string)
	}
	pm.Positions[backend] = eventID
}

// GetPosition returns the position for a backend.
func (pm *ProgressMarker) GetPosition(backend string) string {
	if pm.Positions == nil {
		return ""
	}
	return pm.Positions[backend]
}

// Clone creates a deep copy of the progress marker.
func (pm *ProgressMarker) Clone() *ProgressMarker {
	if pm == nil {
		return NewProgressMarker()
	}

	clone := NewProgressMarker()
	if pm.Lineages != nil {
		clone.Lineages = make(map[string]string, len(pm.Lineages))
		for k, v := range pm.Lineages {
			clone.Lineages[k] = v
		}
	}
	for k, v := range pm.Positions {
		clone.Positions[k] = v
	}
	return clone
}
