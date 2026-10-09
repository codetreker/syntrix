package scenario

import (
	"context"
	"fmt"
	"math/rand"
	"sync"

	"github.com/codetreker/syntrix/pkg/benchmark/generator"
	"github.com/codetreker/syntrix/pkg/benchmark/types"
)

// CRUDScenario implements a basic CRUD benchmark scenario.
type CRUDScenario struct {
	config       *types.Config
	docGenerator *generator.DocumentGenerator
	idGenerator  *generator.IDGenerator
	createdIDs   []string
	mu           sync.RWMutex
	opIndex      int
}

// NewCRUDScenario creates a new CRUD scenario.
func NewCRUDScenario(config *types.Config) (*CRUDScenario, error) {
	if config == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	// Create document generator
	docGen, err := generator.NewDocumentGenerator(
		config.Data.FieldsCount,
		config.Data.DocumentSize,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create document generator: %w", err)
	}

	// Create ID generator
	idGen := generator.NewIDGenerator("bench-", true)

	return &CRUDScenario{
		config:       config,
		docGenerator: docGen,
		idGenerator:  idGen,
		createdIDs:   make([]string, 0, 1000),
		opIndex:      0,
	}, nil
}

// Name returns the scenario identifier.
func (s *CRUDScenario) Name() string {
	return "crud"
}

// Setup prepares scenario prerequisites.
func (s *CRUDScenario) Setup(ctx context.Context, env *types.TestEnv) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Seed initial documents if configured
	if s.config.Data.SeedData > 0 {
		client := env.Client
		collection := s.getCollectionName()

		for i := 0; i < s.config.Data.SeedData; i++ {
			doc, err := s.docGenerator.Generate()
			if err != nil {
				return fmt.Errorf("failed to generate seed document %d: %w", i, err)
			}

			result, err := client.CreateDocument(ctx, collection, doc)
			if err != nil {
				return fmt.Errorf("failed to create seed document %d: %w", i, err)
			}

			s.createdIDs = append(s.createdIDs, result.ID)
		}
	}

	return nil
}

// NextOperation returns the next operation to execute.
func (s *CRUDScenario) NextOperation() (types.Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	collection := s.getCollectionName()

	// Determine operation type based on configuration or default distribution
	opType := s.selectOperationType()

	switch opType {
	case "create":
		doc, err := s.docGenerator.Generate()
		if err != nil {
			return nil, fmt.Errorf("failed to generate document: %w", err)
		}
		op := NewCreateOperationWithCallback(collection, doc, s.RegisterCreatedID)
		s.opIndex++
		return op, nil

	case "read":
		if len(s.createdIDs) == 0 {
			// Fall back to create if no documents exist
			doc, err := s.docGenerator.Generate()
			if err != nil {
				return nil, fmt.Errorf("failed to generate document: %w", err)
			}
			op := NewCreateOperationWithCallback(collection, doc, s.RegisterCreatedID)
			s.opIndex++
			return op, nil
		}
		id := s.createdIDs[rand.Intn(len(s.createdIDs))]
		op := NewReadOperation(collection, id)
		s.opIndex++
		return op, nil

	case "update":
		if len(s.createdIDs) == 0 {
			// Fall back to create if no documents exist
			doc, err := s.docGenerator.Generate()
			if err != nil {
				return nil, fmt.Errorf("failed to generate document: %w", err)
			}
			op := NewCreateOperationWithCallback(collection, doc, s.RegisterCreatedID)
			s.opIndex++
			return op, nil
		}
		id := s.createdIDs[rand.Intn(len(s.createdIDs))]
		doc, err := s.docGenerator.Generate()
		if err != nil {
			return nil, fmt.Errorf("failed to generate document: %w", err)
		}
		op := NewUpdateOperation(collection, id, doc)
		s.opIndex++
		return op, nil

	case "delete":
		if len(s.createdIDs) == 0 {
			// Fall back to create if no documents exist
			doc, err := s.docGenerator.Generate()
			if err != nil {
				return nil, fmt.Errorf("failed to generate document: %w", err)
			}
			op := NewCreateOperationWithCallback(collection, doc, s.RegisterCreatedID)
			s.opIndex++
			return op, nil
		}
		// Remove and use the last ID
		idx := len(s.createdIDs) - 1
		id := s.createdIDs[idx]
		s.createdIDs = s.createdIDs[:idx]
		op := NewDeleteOperation(collection, id)
		s.opIndex++
		return op, nil

	default:
		return nil, fmt.Errorf("unknown operation type: %s", opType)
	}
}

// Teardown cleans up scenario resources.
func (s *CRUDScenario) Teardown(ctx context.Context, env *types.TestEnv) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clean up created documents if configured
	if s.config.Data.Cleanup && len(s.createdIDs) > 0 {
		client := env.Client
		collection := s.getCollectionName()

		for _, id := range s.createdIDs {
			if err := client.DeleteDocument(ctx, collection, id); err != nil {
				// Log error but continue cleanup
				continue
			}
		}

		s.createdIDs = s.createdIDs[:0]
	}

	return nil
}

// RegisterCreatedID registers a newly created document ID.
func (s *CRUDScenario) RegisterCreatedID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createdIDs = append(s.createdIDs, id)
}

// getCollectionName returns the collection name to use.
func (s *CRUDScenario) getCollectionName() string {
	prefix := s.config.Data.CollectionPrefix
	if prefix == "" {
		prefix = "benchmarks"
	}
	return prefix
}

// selectOperationType selects the next operation type based on configuration.
func (s *CRUDScenario) selectOperationType() string {
	// If scenario has specific operations configured, use weighted selection
	if len(s.config.Scenario.Operations) > 0 {
		totalWeight := 0
		for _, op := range s.config.Scenario.Operations {
			totalWeight += op.Weight
		}

		if totalWeight > 0 {
			r := rand.Intn(totalWeight)
			cumulative := 0
			for _, op := range s.config.Scenario.Operations {
				cumulative += op.Weight
				if r < cumulative {
					return op.Type
				}
			}
		}
	}

	// Default: equal distribution across CRUD operations
	ops := []string{"create", "read", "update", "delete"}
	return ops[s.opIndex%len(ops)]
}
