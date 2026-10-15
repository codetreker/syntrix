package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nats-io/nats.go/jetstream"
)

type Ownership struct {
	InstallationID string `json:"installation_id" yaml:"installation_id"`
	InstanceID     string `json:"instance_id" yaml:"instance_id"`
	AllocationID   string `json:"allocation_id" yaml:"allocation_id"`
}

type BrokerBinding struct {
	ResourceMode  string       `json:"resource_mode" yaml:"resource_mode"`
	SpecRevision  uint64       `json:"spec_revision" yaml:"spec_revision"`
	Ownership     Ownership    `json:"ownership" yaml:"ownership"`
	StreamName    string       `json:"stream_name" yaml:"stream_name"`
	SubjectPrefix string       `json:"subject_prefix" yaml:"subject_prefix"`
	ConsumerName  string       `json:"consumer_name" yaml:"consumer_name"`
	Policy        BrokerPolicy `json:"policy" yaml:"policy"`
}

type BrokerPolicy struct {
	Storage     string        `json:"storage" yaml:"storage"`
	Replicas    int           `json:"replicas" yaml:"replicas"`
	MaxBytes    int64         `json:"max_bytes" yaml:"max_bytes"`
	MaxMessages int64         `json:"max_messages" yaml:"max_messages"`
	MaxAge      time.Duration `json:"max_age" yaml:"max_age"`
}

type BrokerSpecification struct {
	Stream        jetstream.StreamConfig   `json:"stream" yaml:"stream"`
	Consumer      jetstream.ConsumerConfig `json:"consumer" yaml:"consumer"`
	SubjectPrefix string                   `json:"subject_prefix" yaml:"subject_prefix"`
}

var brokerOpaqueID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func DefaultOperatorBinding() BrokerBinding {
	return BrokerBinding{
		ResourceMode: "operator",
		SpecRevision: 1,
		Ownership: Ownership{
			InstallationID: "syntrix-operator",
			InstanceID:     "00000000000000000000000000000001",
			AllocationID:   "00000000000000000000000000000001",
		},
		StreamName:    "TRIGGERS",
		SubjectPrefix: "TRIGGERS",
		ConsumerName:  "trigger-delivery",
		Policy: BrokerPolicy{
			Storage: "file", Replicas: 1,
			MaxBytes: 1 << 30, MaxMessages: 1_000_000, MaxAge: 24 * time.Hour,
		},
	}
}

func (b BrokerBinding) Specification() (BrokerSpecification, error) {
	if err := b.validate(); err != nil {
		return BrokerSpecification{}, err
	}
	storage := jetstream.FileStorage
	if b.Policy.Storage == "memory" {
		storage = jetstream.MemoryStorage
	}
	metadata := func() map[string]string {
		return map[string]string{
			"syntrix.installation_id": b.Ownership.InstallationID,
			"syntrix.instance_id":     b.Ownership.InstanceID,
			"syntrix.allocation_id":   b.Ownership.AllocationID,
			"syntrix.spec_revision":   strconv.FormatUint(b.SpecRevision, 10),
		}
	}
	return BrokerSpecification{
		Stream: jetstream.StreamConfig{
			Name: b.StreamName, Subjects: []string{b.SubjectPrefix + ".>"},
			Retention: jetstream.LimitsPolicy, Discard: jetstream.DiscardOld,
			Storage: storage, Replicas: b.Policy.Replicas, MaxConsumers: 1,
			MaxMsgs: b.Policy.MaxMessages, MaxBytes: b.Policy.MaxBytes, MaxAge: b.Policy.MaxAge,
			MaxMsgsPerSubject: -1, MaxMsgSize: -1,
			Duplicates:  min(2*time.Minute, b.Policy.MaxAge),
			Compression: jetstream.NoCompression, PersistMode: jetstream.DefaultPersistMode,
			Metadata: metadata(),
		},
		Consumer: jetstream.ConsumerConfig{
			Name: b.ConsumerName, Durable: b.ConsumerName,
			FilterSubject: b.SubjectPrefix + ".>",
			DeliverPolicy: jetstream.DeliverAllPolicy, AckPolicy: jetstream.AckExplicitPolicy,
			ReplayPolicy: jetstream.ReplayInstantPolicy, AckWait: 30 * time.Second,
			MaxDeliver: -1, MaxWaiting: 512, MaxAckPending: 1000,
			Metadata: metadata(),
		},
		SubjectPrefix: b.SubjectPrefix,
	}, nil
}

func (b BrokerBinding) validate() error {
	if b.ResourceMode != "managed" && b.ResourceMode != "operator" {
		return fmt.Errorf("broker resource_mode must be managed or operator")
	}
	if b.SpecRevision == 0 {
		return fmt.Errorf("broker spec_revision must be positive")
	}
	installation := b.Ownership.InstallationID
	if len(installation) == 0 || len(installation) > 128 || !utf8.ValidString(installation) || strings.TrimSpace(installation) == "" || strings.ContainsFunc(installation, func(r rune) bool { return !unicode.IsPrint(r) }) {
		return fmt.Errorf("broker installation_id must be a nonempty printable identifier of at most 128 bytes")
	}
	if !brokerOpaqueID.MatchString(b.Ownership.InstanceID) || !brokerOpaqueID.MatchString(b.Ownership.AllocationID) {
		return fmt.Errorf("broker instance_id and allocation_id must contain 32 lowercase hexadecimal characters")
	}
	if !validBrokerName(b.StreamName) || !validBrokerName(b.ConsumerName) || !validBrokerPrefix(b.SubjectPrefix) {
		return fmt.Errorf("broker stream_name, subject_prefix, and consumer_name must be valid explicit names")
	}
	if b.ResourceMode == "managed" {
		name := "SYNTRIX_" + b.Ownership.InstanceID + "_TRIGGERS"
		if b.StreamName != name || b.SubjectPrefix != name || b.ConsumerName != name+"_DELIVERY" {
			return fmt.Errorf("managed broker names must derive from instance_id")
		}
	}
	if b.Policy.Storage != "file" && b.Policy.Storage != "memory" {
		return fmt.Errorf("broker storage must be file or memory")
	}
	if b.Policy.Replicas < 1 || b.Policy.Replicas > 5 {
		return fmt.Errorf("broker replicas must be between 1 and 5")
	}
	if b.Policy.MaxBytes <= 0 || b.Policy.MaxMessages <= 0 || b.Policy.MaxAge <= 0 {
		return fmt.Errorf("broker max_bytes, max_messages, and max_age must be positive")
	}
	return nil
}

func validBrokerName(name string) bool {
	return len(name) > 0 && len(name) <= 255 && utf8.ValidString(name) && !strings.ContainsFunc(name, func(r rune) bool {
		return !unicode.IsPrint(r) || unicode.IsSpace(r) || strings.ContainsRune(".*>/\\", r)
	})
}

func validBrokerPrefix(prefix string) bool {
	if len(prefix) > 255 {
		return false
	}
	for _, token := range strings.Split(prefix, ".") {
		if !validBrokerName(token) {
			return false
		}
	}
	return true
}
