package evaluator

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/codetreker/syntrix/internal/core/pubsub"
	"github.com/codetreker/syntrix/internal/trigger/types"
)

// TaskPublisher publishes delivery tasks.
type TaskPublisher interface {
	Publish(ctx context.Context, task *types.DeliveryTask) error
	Close() error
}

// natsPublisher implements TaskPublisher wrapping pubsub.Publisher.
type natsPublisher struct {
	pub     pubsub.Publisher
	metrics types.Metrics
	prefix  string
}

// NewTaskPublisher creates a TaskPublisher wrapping a pubsub.Publisher.
func NewTaskPublisher(pub pubsub.Publisher, streamName string, metrics types.Metrics) TaskPublisher {
	if metrics == nil {
		metrics = &types.NoopMetrics{}
	}
	if streamName == "" {
		streamName = "TRIGGERS"
	}
	return &natsPublisher{pub: pub, metrics: metrics, prefix: streamName}
}

func (p *natsPublisher) Publish(ctx context.Context, task *types.DeliveryTask) error {
	start := time.Now()
	// Subject format: <database>.<collection>.<documentId>
	// DocumentID is base64url encoded to ensure safety.
	// Note: prefix is handled by pubsub.Publisher, so we don't include it here.
	encodedDocumentID := base64.URLEncoding.EncodeToString([]byte(task.DocumentID))

	subject := fmt.Sprintf("%s.%s.%s", task.Database, task.Collection, encodedDocumentID)

	// Full subject for length check (including prefix)
	fullSubject := fmt.Sprintf("%s.%s", p.prefix, subject)

	// NATS subject length limit is usually 64KB, but for performance and practical reasons,
	// we might want to limit it. The requirement says "if length > 1024".
	if len(fullSubject) > 1024 {
		hash := sha256.Sum256([]byte(fullSubject))
		// Truncate to 32 chars (16 bytes hex)
		hashStr := hex.EncodeToString(hash[:16])
		subject = fmt.Sprintf("hashed.%s", hashStr)
		task.SubjectHashed = true
	}

	data, err := json.Marshal(task)
	if err != nil {
		p.metrics.IncPublishFailure(task.Database, task.Collection, "marshal_error")
		return err
	}

	err = p.pub.Publish(ctx, subject, data)
	if err != nil {
		p.metrics.IncPublishFailure(task.Database, task.Collection, err.Error())
		return err
	}

	p.metrics.IncPublishSuccess(task.Database, task.Collection, task.SubjectHashed)
	p.metrics.ObservePublishLatency(task.Database, task.Collection, time.Since(start))
	return nil
}

// Close releases resources held by the publisher.
func (p *natsPublisher) Close() error {
	return p.pub.Close()
}
