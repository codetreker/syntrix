package nats

import (
	"reflect"
	"testing"
	"time"

	deployconfig "github.com/codetreker/syntrix/deployment/config"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testBrokerSpecification(t *testing.T) deployconfig.BrokerSpecification {
	t.Helper()
	spec, err := deployconfig.DefaultOperatorBinding().Specification()
	require.NoError(t, err)
	return spec
}

func TestVerifyStreamSpecificationInventory(t *testing.T) {
	mutations := map[string]func(*jetstream.StreamConfig){
		"Name":                   func(c *jetstream.StreamConfig) { c.Name = "secret-value" },
		"Subjects":               func(c *jetstream.StreamConfig) { c.Subjects = []string{"foreign.>"} },
		"Retention":              func(c *jetstream.StreamConfig) { c.Retention = jetstream.WorkQueuePolicy },
		"MaxConsumers":           func(c *jetstream.StreamConfig) { c.MaxConsumers = 2 },
		"MaxMsgs":                func(c *jetstream.StreamConfig) { c.MaxMsgs++ },
		"MaxBytes":               func(c *jetstream.StreamConfig) { c.MaxBytes++ },
		"Discard":                func(c *jetstream.StreamConfig) { c.Discard = jetstream.DiscardNew },
		"DiscardNewPerSubject":   func(c *jetstream.StreamConfig) { c.DiscardNewPerSubject = true },
		"MaxAge":                 func(c *jetstream.StreamConfig) { c.MaxAge++ },
		"MaxMsgsPerSubject":      func(c *jetstream.StreamConfig) { c.MaxMsgsPerSubject = 1 },
		"MaxMsgSize":             func(c *jetstream.StreamConfig) { c.MaxMsgSize = 1 },
		"Storage":                func(c *jetstream.StreamConfig) { c.Storage = jetstream.MemoryStorage },
		"Replicas":               func(c *jetstream.StreamConfig) { c.Replicas = 2 },
		"NoAck":                  func(c *jetstream.StreamConfig) { c.NoAck = true },
		"Duplicates":             func(c *jetstream.StreamConfig) { c.Duplicates++ },
		"Placement":              func(c *jetstream.StreamConfig) { c.Placement = &jetstream.Placement{} },
		"Mirror":                 func(c *jetstream.StreamConfig) { c.Mirror = &jetstream.StreamSource{} },
		"Sources":                func(c *jetstream.StreamConfig) { c.Sources = []*jetstream.StreamSource{{}} },
		"Sealed":                 func(c *jetstream.StreamConfig) { c.Sealed = true },
		"DenyDelete":             func(c *jetstream.StreamConfig) { c.DenyDelete = true },
		"DenyPurge":              func(c *jetstream.StreamConfig) { c.DenyPurge = true },
		"AllowRollup":            func(c *jetstream.StreamConfig) { c.AllowRollup = true },
		"Compression":            func(c *jetstream.StreamConfig) { c.Compression = jetstream.S2Compression },
		"SubjectTransform":       func(c *jetstream.StreamConfig) { c.SubjectTransform = &jetstream.SubjectTransformConfig{} },
		"RePublish":              func(c *jetstream.StreamConfig) { c.RePublish = &jetstream.RePublish{} },
		"AllowDirect":            func(c *jetstream.StreamConfig) { c.AllowDirect = true },
		"MirrorDirect":           func(c *jetstream.StreamConfig) { c.MirrorDirect = true },
		"ConsumerLimits":         func(c *jetstream.StreamConfig) { c.ConsumerLimits.MaxAckPending = 1 },
		"Metadata":               func(c *jetstream.StreamConfig) { c.Metadata = nil },
		"Template":               func(c *jetstream.StreamConfig) { c.Template = "secret-value" },
		"AllowMsgTTL":            func(c *jetstream.StreamConfig) { c.AllowMsgTTL = true },
		"SubjectDeleteMarkerTTL": func(c *jetstream.StreamConfig) { c.SubjectDeleteMarkerTTL = time.Second },
		"AllowMsgCounter":        func(c *jetstream.StreamConfig) { c.AllowMsgCounter = true },
		"AllowAtomicPublish":     func(c *jetstream.StreamConfig) { c.AllowAtomicPublish = true },
		"AllowMsgSchedules":      func(c *jetstream.StreamConfig) { c.AllowMsgSchedules = true },
		"PersistMode":            func(c *jetstream.StreamConfig) { c.PersistMode = jetstream.AsyncPersistMode },
	}
	assertClassifiedFields(t, reflect.TypeOf(jetstream.StreamConfig{}), mutations, "Description", "FirstSeq")
	for field, edit := range mutations {
		t.Run(field, func(t *testing.T) {
			spec := testBrokerSpecification(t)
			actual := testBrokerSpecification(t).Stream
			edit(&actual)
			err := verifyStreamSpecification(spec, actual)
			require.Error(t, err)
			var mismatch *SpecificationError
			require.ErrorAs(t, err, &mismatch)
			assert.Equal(t, "stream", mismatch.Object)
			assert.NotEmpty(t, mismatch.Field)
			assert.Contains(t, err.Error(), "NATS stream specification mismatch:")
			assert.NotContains(t, err.Error(), "secret-value")
			assert.Error(t, verifySpecification(spec, actual, spec.Consumer))
		})
	}
	t.Run("consumer_inactive_limit", func(t *testing.T) {
		spec := testBrokerSpecification(t)
		actual := testBrokerSpecification(t).Stream
		actual.ConsumerLimits.InactiveThreshold = time.Second
		assert.Error(t, verifyStreamSpecification(spec, actual))
	})
}

func TestVerifyConsumerSpecificationInventory(t *testing.T) {
	now := time.Now()
	mutations := map[string]func(*jetstream.ConsumerConfig){
		"Name":               func(c *jetstream.ConsumerConfig) { c.Name = "secret-value" },
		"Durable":            func(c *jetstream.ConsumerConfig) { c.Durable = "secret-value" },
		"DeliverPolicy":      func(c *jetstream.ConsumerConfig) { c.DeliverPolicy = jetstream.DeliverNewPolicy },
		"OptStartSeq":        func(c *jetstream.ConsumerConfig) { c.OptStartSeq = 1 },
		"OptStartTime":       func(c *jetstream.ConsumerConfig) { c.OptStartTime = &now },
		"AckPolicy":          func(c *jetstream.ConsumerConfig) { c.AckPolicy = jetstream.AckNonePolicy },
		"AckWait":            func(c *jetstream.ConsumerConfig) { c.AckWait++ },
		"MaxDeliver":         func(c *jetstream.ConsumerConfig) { c.MaxDeliver = 1 },
		"BackOff":            func(c *jetstream.ConsumerConfig) { c.BackOff = []time.Duration{time.Second} },
		"FilterSubject":      func(c *jetstream.ConsumerConfig) { c.FilterSubject = "foreign.>" },
		"ReplayPolicy":       func(c *jetstream.ConsumerConfig) { c.ReplayPolicy = jetstream.ReplayOriginalPolicy },
		"RateLimit":          func(c *jetstream.ConsumerConfig) { c.RateLimit = 1 },
		"MaxWaiting":         func(c *jetstream.ConsumerConfig) { c.MaxWaiting++ },
		"MaxAckPending":      func(c *jetstream.ConsumerConfig) { c.MaxAckPending++ },
		"HeadersOnly":        func(c *jetstream.ConsumerConfig) { c.HeadersOnly = true },
		"MaxRequestBatch":    func(c *jetstream.ConsumerConfig) { c.MaxRequestBatch = 1 },
		"MaxRequestExpires":  func(c *jetstream.ConsumerConfig) { c.MaxRequestExpires = time.Second },
		"MaxRequestMaxBytes": func(c *jetstream.ConsumerConfig) { c.MaxRequestMaxBytes = 1 },
		"InactiveThreshold":  func(c *jetstream.ConsumerConfig) { c.InactiveThreshold = time.Second },
		"Replicas":           func(c *jetstream.ConsumerConfig) { c.Replicas = 2 },
		"MemoryStorage":      func(c *jetstream.ConsumerConfig) { c.MemoryStorage = true },
		"FilterSubjects":     func(c *jetstream.ConsumerConfig) { c.FilterSubjects = []string{"TRIGGERS.>"} },
		"Metadata":           func(c *jetstream.ConsumerConfig) { c.Metadata = nil },
		"PauseUntil":         func(c *jetstream.ConsumerConfig) { c.PauseUntil = &now },
		"PriorityPolicy":     func(c *jetstream.ConsumerConfig) { c.PriorityPolicy = jetstream.PriorityPolicyPinned },
		"PinnedTTL":          func(c *jetstream.ConsumerConfig) { c.PinnedTTL = time.Second },
		"PriorityGroups":     func(c *jetstream.ConsumerConfig) { c.PriorityGroups = []string{"a"} },
		"DeliverSubject":     func(c *jetstream.ConsumerConfig) { c.DeliverSubject = "outbound" },
		"DeliverGroup":       func(c *jetstream.ConsumerConfig) { c.DeliverGroup = "workers" },
		"FlowControl":        func(c *jetstream.ConsumerConfig) { c.FlowControl = true },
		"IdleHeartbeat":      func(c *jetstream.ConsumerConfig) { c.IdleHeartbeat = time.Second },
	}
	assertClassifiedFields(t, reflect.TypeOf(jetstream.ConsumerConfig{}), mutations, "Description", "SampleFrequency")
	for field, edit := range mutations {
		t.Run(field, func(t *testing.T) {
			spec := testBrokerSpecification(t)
			actual := testBrokerSpecification(t).Consumer
			edit(&actual)
			err := verifyConsumerSpecification(spec, actual)
			require.Error(t, err)
			var mismatch *SpecificationError
			require.ErrorAs(t, err, &mismatch)
			assert.Equal(t, "consumer", mismatch.Object)
			assert.NotEmpty(t, mismatch.Field)
			assert.Contains(t, err.Error(), "NATS consumer specification mismatch:")
			assert.NotContains(t, err.Error(), "secret-value")
			assert.Error(t, verifySpecification(spec, spec.Stream, actual))
		})
	}
}

func assertClassifiedFields[T any](t *testing.T, typ reflect.Type, classified map[string]T, ignored ...string) {
	t.Helper()
	ignore := make(map[string]bool, len(ignored))
	for _, name := range ignored {
		ignore[name] = true
	}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		_, authoritative := classified[name]
		assert.True(t, authoritative || ignore[name], "SDK field %s.%s requires policy classification", typ.Name(), name)
	}
}

func TestVerifySpecificationServerNormalization(t *testing.T) {
	for _, replicas := range []int{1, 3, 5} {
		for _, unlimitedValue := range []int64{0, -1} {
			for _, consumerReplicas := range []int{0, replicas} {
				b := deployconfig.DefaultOperatorBinding()
				b.Policy.Replicas = replicas
				spec, err := b.Specification()
				require.NoError(t, err)
				actual, err := b.Specification()
				require.NoError(t, err)
				actual.Stream.MaxMsgsPerSubject = unlimitedValue
				actual.Stream.MaxMsgSize = int32(unlimitedValue)
				actual.Stream.Sources = []*jetstream.StreamSource{}
				actual.Stream.Description, actual.Stream.FirstSeq = "operator description", 500
				actual.Consumer.Replicas = consumerReplicas
				actual.Consumer.MaxDeliver = 0
				actual.Consumer.MaxRequestBatch, actual.Consumer.MaxRequestMaxBytes = int(unlimitedValue), int(unlimitedValue)
				actual.Consumer.BackOff = []time.Duration{}
				actual.Consumer.FilterSubjects, actual.Consumer.PriorityGroups = []string{}, []string{}
				actual.Consumer.Description, actual.Consumer.SampleFrequency = "observed description", "50%"
				actual.Stream.Metadata["server.info"] = "additional"
				actual.Consumer.Metadata["server.info"] = "additional"
				require.NoError(t, verifySpecification(spec, actual.Stream, actual.Consumer))
			}
		}
	}
}

func TestVerifySpecificationOwnership(t *testing.T) {
	for _, key := range []string{"syntrix.installation_id", "syntrix.instance_id", "syntrix.allocation_id", "syntrix.spec_revision"} {
		for _, remove := range []bool{false, true} {
			spec := testBrokerSpecification(t)
			actual := testBrokerSpecification(t)
			if remove {
				delete(actual.Stream.Metadata, key)
				delete(actual.Consumer.Metadata, key)
			} else {
				actual.Stream.Metadata[key], actual.Consumer.Metadata[key] = "secret-value", "secret-value"
			}
			assert.EqualError(t, verifyStreamSpecification(spec, actual.Stream), "NATS stream specification mismatch: metadata")
			assert.EqualError(t, verifyConsumerSpecification(spec, actual.Consumer), "NATS consumer specification mismatch: metadata")
		}
	}
}

func TestVerifySpecificationRejectsInvalidUnlimitedSentinels(t *testing.T) {
	for _, invalid := range []int64{-2, 1} {
		spec := testBrokerSpecification(t)
		stream := testBrokerSpecification(t).Stream
		stream.MaxMsgsPerSubject = invalid
		assert.Error(t, verifyStreamSpecification(spec, stream))
		stream = testBrokerSpecification(t).Stream
		stream.MaxMsgSize = int32(invalid)
		assert.Error(t, verifyStreamSpecification(spec, stream))
		consumer := testBrokerSpecification(t).Consumer
		consumer.MaxRequestBatch = int(invalid)
		assert.Error(t, verifyConsumerSpecification(spec, consumer))
		consumer = testBrokerSpecification(t).Consumer
		consumer.MaxRequestMaxBytes = int(invalid)
		assert.Error(t, verifyConsumerSpecification(spec, consumer))
	}
}
