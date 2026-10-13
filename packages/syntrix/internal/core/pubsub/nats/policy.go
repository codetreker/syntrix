package nats

import (
	"fmt"
	"slices"

	deployconfig "github.com/codetreker/syntrix/deployment/config"
	"github.com/nats-io/nats.go/jetstream"
)

type policyCheck struct {
	field string
	match bool
}

type SpecificationError struct {
	Object string
	Field  string
}

func (e *SpecificationError) Error() string {
	return fmt.Sprintf("NATS %s specification mismatch: %s", e.Object, e.Field)
}

func verifySpecification(spec deployconfig.BrokerSpecification, stream jetstream.StreamConfig, consumer jetstream.ConsumerConfig) error {
	if err := verifyStreamSpecification(spec, stream); err != nil {
		return err
	}
	return verifyConsumerSpecification(spec, consumer)
}

func verifyStreamSpecification(spec deployconfig.BrokerSpecification, actual jetstream.StreamConfig) error {
	want := spec.Stream
	return verifyPolicy("stream", []policyCheck{
		{"name", actual.Name == want.Name},
		{"subjects", slices.Equal(actual.Subjects, want.Subjects)},
		{"retention", actual.Retention == want.Retention},
		{"discard", actual.Discard == want.Discard},
		{"storage", actual.Storage == want.Storage},
		{"replicas", actual.Replicas == want.Replicas},
		{"max_consumers", actual.MaxConsumers == want.MaxConsumers},
		{"max_messages", actual.MaxMsgs == want.MaxMsgs},
		{"max_bytes", actual.MaxBytes == want.MaxBytes},
		{"max_age", actual.MaxAge == want.MaxAge},
		{"max_messages_per_subject", unlimited(actual.MaxMsgsPerSubject)},
		{"max_message_size", unlimited(int64(actual.MaxMsgSize))},
		{"duplicates", actual.Duplicates == want.Duplicates},
		{"discard_new_per_subject", !actual.DiscardNewPerSubject},
		{"no_ack", !actual.NoAck},
		{"sealed", !actual.Sealed},
		{"deny_delete", !actual.DenyDelete},
		{"deny_purge", !actual.DenyPurge},
		{"allow_rollup", !actual.AllowRollup},
		{"allow_direct", !actual.AllowDirect},
		{"mirror_direct", !actual.MirrorDirect},
		{"allow_message_ttl", !actual.AllowMsgTTL},
		{"allow_message_counter", !actual.AllowMsgCounter},
		{"allow_atomic_publish", !actual.AllowAtomicPublish},
		{"allow_message_schedules", !actual.AllowMsgSchedules},
		{"subject_delete_marker_ttl", actual.SubjectDeleteMarkerTTL == 0},
		{"compression", actual.Compression == jetstream.NoCompression},
		{"persist_mode", actual.PersistMode == jetstream.DefaultPersistMode},
		{"placement", actual.Placement == nil},
		{"mirror", actual.Mirror == nil},
		{"sources", len(actual.Sources) == 0},
		{"subject_transform", actual.SubjectTransform == nil},
		{"republish", actual.RePublish == nil},
		{"template", actual.Template == ""},
		{"consumer_limits.inactive_threshold", actual.ConsumerLimits.InactiveThreshold == 0},
		{"consumer_limits.max_ack_pending", actual.ConsumerLimits.MaxAckPending == 0},
		{"metadata", ownedMetadataMatches(want.Metadata, actual.Metadata)},
	})
}

func verifyConsumerSpecification(spec deployconfig.BrokerSpecification, actual jetstream.ConsumerConfig) error {
	want := spec.Consumer
	return verifyPolicy("consumer", []policyCheck{
		{"name", actual.Name == want.Name},
		{"durable", actual.Durable == want.Durable},
		{"filter_subject", actual.FilterSubject == want.FilterSubject},
		{"filter_subjects", len(actual.FilterSubjects) == 0},
		{"deliver_policy", actual.DeliverPolicy == want.DeliverPolicy},
		{"ack_policy", actual.AckPolicy == want.AckPolicy},
		{"replay_policy", actual.ReplayPolicy == want.ReplayPolicy},
		{"ack_wait", actual.AckWait == want.AckWait},
		{"max_deliver", actual.MaxDeliver == want.MaxDeliver || actual.MaxDeliver == 0 && want.MaxDeliver == -1},
		{"max_waiting", actual.MaxWaiting == want.MaxWaiting},
		{"max_ack_pending", actual.MaxAckPending == want.MaxAckPending},
		{"headers_only", !actual.HeadersOnly},
		{"backoff", len(actual.BackOff) == 0},
		{"inactive_threshold", actual.InactiveThreshold == 0},
		{"memory_storage", !actual.MemoryStorage},
		{"replicas", actual.Replicas == 0 || actual.Replicas == spec.Stream.Replicas},
		{"start_sequence", actual.OptStartSeq == 0},
		{"start_time", actual.OptStartTime == nil},
		{"rate_limit", actual.RateLimit == 0},
		{"max_request_batch", unlimited(int64(actual.MaxRequestBatch))},
		{"max_request_expires", actual.MaxRequestExpires == 0},
		{"max_request_bytes", unlimited(int64(actual.MaxRequestMaxBytes))},
		{"pause_until", actual.PauseUntil == nil},
		{"priority_policy", actual.PriorityPolicy == jetstream.PriorityPolicyNone},
		{"pinned_ttl", actual.PinnedTTL == 0},
		{"priority_groups", len(actual.PriorityGroups) == 0},
		{"deliver_subject", actual.DeliverSubject == ""},
		{"deliver_group", actual.DeliverGroup == ""},
		{"flow_control", !actual.FlowControl},
		{"idle_heartbeat", actual.IdleHeartbeat == 0},
		{"metadata", ownedMetadataMatches(want.Metadata, actual.Metadata)},
	})
}

func verifyPolicy(object string, checks []policyCheck) error {
	for _, check := range checks {
		if !check.match {
			return &SpecificationError{Object: object, Field: check.field}
		}
	}
	return nil
}

func unlimited(value int64) bool { return value == 0 || value == -1 }

func ownedMetadataMatches(want, actual map[string]string) bool {
	for _, key := range [...]string{"syntrix.installation_id", "syntrix.instance_id", "syntrix.allocation_id", "syntrix.spec_revision"} {
		value, present := actual[key]
		if !present || value != want[key] {
			return false
		}
	}
	return true
}
