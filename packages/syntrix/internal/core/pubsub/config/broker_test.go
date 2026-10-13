package config

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func managedBinding() BrokerBinding {
	b := DefaultOperatorBinding()
	b.ResourceMode = "managed"
	b.StreamName = "SYNTRIX_" + b.Ownership.InstanceID + "_TRIGGERS"
	b.SubjectPrefix = b.StreamName
	b.ConsumerName = b.StreamName + "_DELIVERY"
	return b
}

func TestBrokerSpecificationPolicy(t *testing.T) {
	for _, storage := range []string{"file", "memory"} {
		for _, maxAge := range []time.Duration{time.Nanosecond, time.Minute, 24 * time.Hour} {
			t.Run(storage+"/"+maxAge.String(), func(t *testing.T) {
				b := managedBinding()
				b.Policy.Storage, b.Policy.MaxAge = storage, maxAge
				spec, err := b.Specification()
				require.NoError(t, err)
				assert.Equal(t, []string{b.StreamName + ".>"}, spec.Stream.Subjects)
				assert.Equal(t, b.SubjectPrefix, spec.SubjectPrefix)
				assert.Equal(t, jetstream.LimitsPolicy, spec.Stream.Retention)
				assert.Equal(t, jetstream.DiscardOld, spec.Stream.Discard)
				assert.Equal(t, 1, spec.Stream.MaxConsumers)
				assert.Equal(t, b.Policy.Replicas, spec.Stream.Replicas)
				assert.Equal(t, b.Policy.MaxBytes, spec.Stream.MaxBytes)
				assert.Equal(t, b.Policy.MaxMessages, spec.Stream.MaxMsgs)
				assert.Equal(t, maxAge, spec.Stream.MaxAge)
				assert.Equal(t, min(2*time.Minute, maxAge), spec.Stream.Duplicates)
				assert.EqualValues(t, -1, spec.Stream.MaxMsgsPerSubject)
				assert.EqualValues(t, -1, spec.Stream.MaxMsgSize)
				if storage == "file" {
					assert.Equal(t, jetstream.FileStorage, spec.Stream.Storage)
				} else {
					assert.Equal(t, jetstream.MemoryStorage, spec.Stream.Storage)
				}
				assert.Equal(t, b.ConsumerName, spec.Consumer.Name)
				assert.Equal(t, b.ConsumerName, spec.Consumer.Durable)
				assert.Equal(t, b.SubjectPrefix+".>", spec.Consumer.FilterSubject)
				assert.Equal(t, jetstream.DeliverAllPolicy, spec.Consumer.DeliverPolicy)
				assert.Equal(t, jetstream.AckExplicitPolicy, spec.Consumer.AckPolicy)
				assert.Equal(t, jetstream.ReplayInstantPolicy, spec.Consumer.ReplayPolicy)
				assert.Equal(t, 30*time.Second, spec.Consumer.AckWait)
				assert.Equal(t, -1, spec.Consumer.MaxDeliver)
				assert.Equal(t, 512, spec.Consumer.MaxWaiting)
				assert.Equal(t, 1000, spec.Consumer.MaxAckPending)
				assert.Zero(t, spec.Consumer.Replicas)
				assert.False(t, spec.Consumer.MemoryStorage)
				assert.Equal(t, map[string]string{
					"syntrix.installation_id": b.Ownership.InstallationID,
					"syntrix.instance_id":     b.Ownership.InstanceID,
					"syntrix.allocation_id":   b.Ownership.AllocationID,
					"syntrix.spec_revision":   "1",
				}, spec.Stream.Metadata)
				assert.Equal(t, spec.Stream.Metadata, spec.Consumer.Metadata)
			})
		}
	}
}

func TestBrokerSpecificationIndependentInstances(t *testing.T) {
	a, b := managedBinding(), managedBinding()
	b.Ownership.InstanceID = "ffffffffffffffffffffffffffffffff"
	b.StreamName = "SYNTRIX_" + b.Ownership.InstanceID + "_TRIGGERS"
	b.SubjectPrefix, b.ConsumerName = b.StreamName, b.StreamName+"_DELIVERY"
	x, err := a.Specification()
	require.NoError(t, err)
	y, err := b.Specification()
	require.NoError(t, err)
	assert.NotEqual(t, x.Stream.Name, y.Stream.Name)
	assert.NotEqual(t, x.Consumer.Durable, y.Consumer.Durable)
	assert.NotEqual(t, x.Consumer.FilterSubject, y.Consumer.FilterSubject)
	x.Stream.Metadata["syntrix.instance_id"] = "changed"
	x.Stream.Subjects[0] = "changed"
	assert.Equal(t, a.Ownership.InstanceID, x.Consumer.Metadata["syntrix.instance_id"])
	z, err := a.Specification()
	require.NoError(t, err)
	assert.Equal(t, a.Ownership.InstanceID, z.Stream.Metadata["syntrix.instance_id"])
	assert.Equal(t, []string{a.SubjectPrefix + ".>"}, z.Stream.Subjects)
}

func TestBrokerBindingValidation(t *testing.T) {
	cases := []struct {
		name string
		edit func(*BrokerBinding)
	}{
		{"mode", func(b *BrokerBinding) { b.ResourceMode = "" }},
		{"unknown_mode", func(b *BrokerBinding) { b.ResourceMode = "repair" }},
		{"revision", func(b *BrokerBinding) { b.SpecRevision = 0 }},
		{"installation_missing", func(b *BrokerBinding) { b.Ownership.InstallationID = "" }},
		{"installation_blank", func(b *BrokerBinding) { b.Ownership.InstallationID = " " }},
		{"installation_control", func(b *BrokerBinding) { b.Ownership.InstallationID = "secret\nvalue" }},
		{"installation_invalid_utf8", func(b *BrokerBinding) { b.Ownership.InstallationID = "\xff" }},
		{"installation_long", func(b *BrokerBinding) { b.Ownership.InstallationID = strings.Repeat("a", 129) }},
		{"instance_missing", func(b *BrokerBinding) { b.Ownership.InstanceID = "" }},
		{"instance_uppercase", func(b *BrokerBinding) { b.Ownership.InstanceID = strings.Repeat("A", 32) }},
		{"instance_long", func(b *BrokerBinding) { b.Ownership.InstanceID = strings.Repeat("0", 33) }},
		{"instance_nonhex", func(b *BrokerBinding) { b.Ownership.InstanceID = strings.Repeat("g", 32) }},
		{"allocation_missing", func(b *BrokerBinding) { b.Ownership.AllocationID = "" }},
		{"stream_missing", func(b *BrokerBinding) { b.StreamName = "" }},
		{"consumer_missing", func(b *BrokerBinding) { b.ConsumerName = "" }},
		{"prefix_missing", func(b *BrokerBinding) { b.SubjectPrefix = "" }},
		{"prefix_empty_token", func(b *BrokerBinding) { b.SubjectPrefix = "a..b" }},
		{"stream_long", func(b *BrokerBinding) { b.StreamName = strings.Repeat("a", 256) }},
		{"prefix_long", func(b *BrokerBinding) { b.SubjectPrefix = strings.Repeat("a", 256) }},
		{"consumer_wildcard", func(b *BrokerBinding) { b.ConsumerName = "a*" }},
		{"stream_dot", func(b *BrokerBinding) { b.StreamName = "a.b" }},
		{"stream_slash", func(b *BrokerBinding) { b.StreamName = "a/b" }},
		{"stream_backslash", func(b *BrokerBinding) { b.StreamName = "a\\b" }},
		{"prefix_wildcard", func(b *BrokerBinding) { b.SubjectPrefix = "a.>" }},
		{"stream_space", func(b *BrokerBinding) { b.StreamName = "a b" }},
		{"stream_control", func(b *BrokerBinding) { b.StreamName = "a\x00b" }},
		{"stream_invalid_utf8", func(b *BrokerBinding) { b.StreamName = "\xff" }},
		{"managed_stream_conflict", func(b *BrokerBinding) { *b = managedBinding(); b.StreamName = "foreign" }},
		{"managed_prefix_conflict", func(b *BrokerBinding) { *b = managedBinding(); b.SubjectPrefix = "foreign" }},
		{"managed_consumer_conflict", func(b *BrokerBinding) { *b = managedBinding(); b.ConsumerName = "foreign" }},
		{"storage_missing", func(b *BrokerBinding) { b.Policy.Storage = "" }},
		{"storage_unknown", func(b *BrokerBinding) { b.Policy.Storage = "disk" }},
		{"replicas_zero", func(b *BrokerBinding) { b.Policy.Replicas = 0 }},
		{"replicas_excess", func(b *BrokerBinding) { b.Policy.Replicas = 6 }},
		{"bytes_zero", func(b *BrokerBinding) { b.Policy.MaxBytes = 0 }},
		{"bytes_unlimited", func(b *BrokerBinding) { b.Policy.MaxBytes = -1 }},
		{"messages_zero", func(b *BrokerBinding) { b.Policy.MaxMessages = 0 }},
		{"messages_unlimited", func(b *BrokerBinding) { b.Policy.MaxMessages = -1 }},
		{"age_zero", func(b *BrokerBinding) { b.Policy.MaxAge = 0 }},
		{"age_negative", func(b *BrokerBinding) { b.Policy.MaxAge = -time.Second }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := DefaultOperatorBinding()
			tc.edit(&b)
			spec, err := b.Specification()
			require.Error(t, err)
			assert.Empty(t, spec)
			assert.NotContains(t, err.Error(), "secret\nvalue")
		})
	}
	for _, replicas := range []int{1, 5} {
		b := DefaultOperatorBinding()
		b.Policy.Replicas, b.Policy.MaxBytes, b.Policy.MaxMessages = replicas, math.MaxInt64, math.MaxInt64
		b.Policy.MaxAge = time.Duration(math.MaxInt64)
		b.SpecRevision = math.MaxUint64
		b.SubjectPrefix = "operator.triggers"
		b.Ownership.InstallationID = strings.Repeat("a", 128)
		_, err := b.Specification()
		require.NoError(t, err)
	}
}

func TestBrokerBindingWireFields(t *testing.T) {
	b := managedBinding()
	jsonBytes, err := json.Marshal(b)
	require.NoError(t, err)
	yamlBytes, err := yaml.Marshal(b)
	require.NoError(t, err)
	var jsonBinding, yamlBinding BrokerBinding
	require.NoError(t, json.Unmarshal(jsonBytes, &jsonBinding))
	require.NoError(t, yaml.Unmarshal(yamlBytes, &yamlBinding))
	assert.Equal(t, b, jsonBinding)
	assert.Equal(t, b, yamlBinding)
	for _, field := range []string{"resource_mode", "spec_revision", "ownership", "installation_id", "instance_id", "allocation_id", "stream_name", "subject_prefix", "consumer_name", "policy", "storage", "replicas", "max_bytes", "max_messages", "max_age"} {
		assert.Contains(t, string(jsonBytes), `"`+field+`"`)
		assert.Contains(t, string(yamlBytes), field+":")
	}
}
