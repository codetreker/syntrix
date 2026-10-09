package cel

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"sync"

	"github.com/codetreker/syntrix/internal/puller/events"
	"github.com/codetreker/syntrix/internal/trigger/types"
	"github.com/google/cel-go/cel"
)

var celNewEnv = cel.NewEnv

// MaxCacheSize is the maximum number of CEL programs to cache.
const MaxCacheSize = 1000

// Evaluator is responsible for matching events against trigger conditions.
type Evaluator interface {
	Evaluate(ctx context.Context, t *types.Trigger, event events.SyntrixChangeEvent) (bool, error)
}

// celeEvaluator implements Evaluator using Google CEL.
type celeEvaluator struct {
	env        *cel.Env
	prgCache   map[string]cel.Program
	cacheOrder []string // Track insertion order for simple FIFO eviction
	cacheMutex sync.RWMutex
}

func NewEvaluator() (Evaluator, error) {
	// Define the CEL environment with an 'event' variable
	env, err := celNewEnv(
		cel.Variable("event", cel.MapType(cel.StringType, cel.DynType)),
	)
	if err != nil {
		return nil, err
	}

	return &celeEvaluator{
		env:        env,
		prgCache:   make(map[string]cel.Program),
		cacheOrder: make([]string, 0, MaxCacheSize),
	}, nil
}

func (e *celeEvaluator) Evaluate(ctx context.Context, t *types.Trigger, event events.SyntrixChangeEvent) (bool, error) {
	// 1. Check database
	// Each trigger specifies which logical database it applies to
	if t.Database != "" && t.Database != "*" {
		eventDB := ""
		if event.Document != nil {
			eventDB = event.Document.Database
		} else if event.Before != nil {
			eventDB = event.Before.Database
		}
		if eventDB != t.Database {
			return false, nil
		}
	}

	// 2. Check event type (create, update, delete)
	eventTypeMatch := false
	for _, evt := range t.Events {
		if string(event.Type) == evt {
			eventTypeMatch = true
			break
		}
	}
	if !eventTypeMatch {
		return false, nil
	}

	// 3. Check collection
	// Handle case where event.Document might be nil (e.g. delete event might only have Before)
	var collectionToMatch string
	if event.Document != nil {
		collectionToMatch = event.Document.Collection
	} else if event.Before != nil {
		collectionToMatch = event.Before.Collection
	}

	if collectionToMatch != "" {
		matched, err := path.Match(t.Collection, collectionToMatch)
		if err != nil {
			return false, fmt.Errorf("invalid collection pattern: %w", err)
		}
		if !matched {
			return false, nil
		}
	}

	// 4. Evaluate CEL condition
	if t.Condition == "" {
		return true, nil
	}

	prg, err := e.getProgram(t.Condition)
	if err != nil {
		return false, fmt.Errorf("failed to get CEL program: %w", err)
	}

	// Prepare input
	input := map[string]interface{}{
		"event": map[string]interface{}{
			"type":      string(event.Type),
			"timestamp": event.Timestamp,
			"document":  nil,
			"before":    nil,
		},
	}

	// If Document is struct, we might need to convert it to map or rely on CEL's reflection if configured.
	// For simplicity, let's manually construct the map for the document part we care about.
	if event.Document != nil {
		docMap := make(map[string]interface{})
		// Flatten data fields
		for k, v := range event.Document.Data {
			docMap[k] = v
		}
		// Set system fields (these overwrite data fields if collision occurs, which is expected behavior for reserved fields)
		docMap["id"] = event.Document.Id
		docMap["collection"] = event.Document.Collection
		docMap["version"] = event.Document.Version

		input["event"].(map[string]interface{})["document"] = docMap
	}

	if event.Before != nil {
		docMap := make(map[string]interface{})
		// Flatten data fields
		for k, v := range event.Before.Data {
			docMap[k] = v
		}
		// Set system fields
		docMap["id"] = event.Before.Id
		docMap["collection"] = event.Before.Collection
		docMap["version"] = event.Before.Version

		input["event"].(map[string]interface{})["before"] = docMap
	}

	out, _, err := prg.Eval(input)
	if err != nil {
		return false, fmt.Errorf("CEL evaluation error: %w", err)
	}

	match, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("CEL condition must return boolean, got %T", out.Value())
	}

	return match, nil
}

func (e *celeEvaluator) getProgram(condition string) (cel.Program, error) {
	e.cacheMutex.RLock()
	prg, ok := e.prgCache[condition]
	e.cacheMutex.RUnlock()
	if ok {
		return prg, nil
	}

	e.cacheMutex.Lock()
	defer e.cacheMutex.Unlock()

	// Double check
	if prg, ok := e.prgCache[condition]; ok {
		return prg, nil
	}

	ast, issues := e.env.Compile(condition)
	if issues != nil && issues.Err() != nil {
		return nil, issues.Err()
	}

	prg, err := e.env.Program(ast)
	if err != nil {
		return nil, err
	}

	// Evict oldest entry if cache is full (simple FIFO)
	if len(e.prgCache) >= MaxCacheSize {
		oldest := e.cacheOrder[0]
		delete(e.prgCache, oldest)
		e.cacheOrder = e.cacheOrder[1:]
		slog.Info("CEL cache full, evicted oldest entry")
	}

	e.prgCache[condition] = prg
	e.cacheOrder = append(e.cacheOrder, condition)
	return prg, nil
}
