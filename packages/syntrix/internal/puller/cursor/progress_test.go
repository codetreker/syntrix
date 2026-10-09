package cursor

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProgressMarker(t *testing.T) {
	t.Run("NewProgressMarker", func(t *testing.T) {
		pm := NewProgressMarker()
		require.NotNil(t, pm)
		assert.Empty(t, pm.Positions)
	})

	t.Run("SetAndGetPosition", func(t *testing.T) {
		pm := NewProgressMarker()
		pm.SetPosition("backend1", "pos1")
		pm.SetPosition("backend2", "pos2")

		assert.Equal(t, "pos1", pm.GetPosition("backend1"))
		assert.Equal(t, "pos2", pm.GetPosition("backend2"))
		assert.Empty(t, pm.GetPosition("backend3"))
	})

	t.Run("EncodeDecode", func(t *testing.T) {
		pm := NewProgressMarker()
		pm.SetPosition("backend1", "pos1")
		pm.SetPosition("backend2", "pos2")

		encoded := pm.Encode()
		require.NotEmpty(t, encoded)

		decoded, err := DecodeProgressMarker(encoded)
		require.NoError(t, err)
		require.NotNil(t, decoded)

		assert.Equal(t, "pos1", decoded.GetPosition("backend1"))
		assert.Equal(t, "pos2", decoded.GetPosition("backend2"))
	})

	t.Run("DecodeEmpty", func(t *testing.T) {
		decoded, err := DecodeProgressMarker("")
		require.NoError(t, err)
		require.NotNil(t, decoded)
		assert.Empty(t, decoded.Positions)
	})

	t.Run("DecodeInvalid", func(t *testing.T) {
		_, err := DecodeProgressMarker("invalid-base64")
		require.Error(t, err)
	})

	t.Run("Clone", func(t *testing.T) {
		pm := NewProgressMarker()
		pm.SetPosition("backend1", "pos1")

		clone := pm.Clone()
		require.NotNil(t, clone)
		assert.Equal(t, "pos1", clone.GetPosition("backend1"))

		// Modify clone, original should not change
		clone.SetPosition("backend1", "pos2")
		assert.Equal(t, "pos2", clone.GetPosition("backend1"))
		assert.Equal(t, "pos1", pm.GetPosition("backend1"))
	})

	t.Run("NilReceiver", func(t *testing.T) {
		var pm *ProgressMarker
		assert.Empty(t, pm.Encode())
		assert.NotNil(t, pm.Clone())
	})

	t.Run("NilPositions", func(t *testing.T) {
		pm := &ProgressMarker{Positions: nil}
		assert.Empty(t, pm.Encode())
		assert.Empty(t, pm.GetPosition("backend1"))

		pm.SetPosition("backend1", "pos1")
		assert.Equal(t, "pos1", pm.GetPosition("backend1"))
	})
}

func TestBootstrapMarkerCloneKeepsIndependentCaptureLineages(t *testing.T) {
	original := &ProgressMarker{Positions: map[string]string{"source-a": "", "source-b": ""}, Lineages: map[string]string{"source-a": "epoch-a", "source-b": "epoch-b"}}
	encoded := original.Encode()
	require.NotEmpty(t, encoded)
	restored, err := DecodeProgressMarker(encoded)
	require.NoError(t, err)
	require.Equal(t, original, restored)
	consumer := restored.Clone()
	consumer.Positions["source-a"] = "applied-event"
	consumer.Lineages["source-a"] = "replacement-epoch"
	require.Equal(t, "", restored.Positions["source-a"])
	require.Equal(t, "epoch-a", restored.Lineages["source-a"])
	require.Equal(t, encoded, original.Encode())
}

func TestProgressDecodeRejectsMalformedPayloadAndOwnsEmptyPositionMap(t *testing.T) {
	for _, payload := range []string{`{`, `{"p":12}`, `{"l":["wrong-shape"]}`} {
		marker, err := DecodeProgressMarker(base64.RawURLEncoding.EncodeToString([]byte(payload)))
		require.Error(t, err)
		require.Nil(t, marker)
	}
	marker, err := DecodeProgressMarker(base64.RawURLEncoding.EncodeToString([]byte(`{"l":{"source":"epoch"}}`)))
	require.NoError(t, err)
	marker.SetPosition("source", "event")
	require.Equal(t, "epoch", marker.Lineages["source"])
	require.Equal(t, "event", marker.GetPosition("source"))
}
