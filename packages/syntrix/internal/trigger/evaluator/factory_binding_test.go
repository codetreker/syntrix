package evaluator

import (
	"context"
	"strings"
	"testing"

	pubsubtesting "github.com/codetreker/syntrix/internal/core/pubsub/testing"
	"github.com/codetreker/syntrix/internal/trigger/types"
	"github.com/stretchr/testify/require"
)

func TestServiceRequiresExplicitPublisherSubjectPrefix(t *testing.T) {
	deps := Dependencies{Puller: &MockPuller{}, Publisher: pubsubtesting.NewMockPublisher()}
	_, err := NewService(deps, DefaultConfig())
	require.EqualError(t, err, "subject prefix is required with a publisher")
}
func TestServiceTaskSubjectUsesBindingPrefixForHashBoundary(t *testing.T) {
	publisher := pubsubtesting.NewMockPublisher()
	prefix := strings.Repeat("p", 120)
	cfg := DefaultConfig()
	cfg.StreamName = "CUSTOM_STREAM"
	cfg.RulesPath = ""
	svc, err := NewService(Dependencies{Puller: &MockPuller{}, Publisher: publisher, SubjectPrefix: prefix}, cfg)
	require.NoError(t, err)
	task := &types.DeliveryTask{Database: "db", Collection: "col", DocumentID: strings.Repeat("d", 700)}
	require.NoError(t, svc.(*service).publisher.Publish(context.Background(), task))
	require.True(t, task.SubjectHashed)
	messages := publisher.Messages()
	require.Len(t, messages, 1)
	require.True(t, strings.HasPrefix(messages[0].Subject, "hashed."))
	require.NotContains(t, messages[0].Subject, prefix)
}
