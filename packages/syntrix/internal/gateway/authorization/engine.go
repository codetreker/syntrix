package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"

	authorizationconfig "github.com/codetreker/syntrix/internal/gateway/authorization/config"
	"github.com/codetreker/syntrix/internal/query"
	"github.com/codetreker/syntrix/pkg/model"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker/decls"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"gopkg.in/yaml.v3"
)

// Errors for rule loading
var (
	ErrEmptyDatabase     = errors.New("database field cannot be empty")
	ErrDuplicateDatabase = errors.New("duplicate database definition")
	ErrNotDirectory      = errors.New("path is not a directory")
	ErrDatabaseMismatch  = errors.New("explicit database name in path does not match database field")
)

type Engine interface {
	Evaluate(ctx context.Context, database string, path string, action string, req Request, existingRes *Resource) (bool, error)
	GetRules() *RuleSet
	GetRulesForDatabase(database string) *RuleSet
	UpdateRules(database string, content []byte) error
	LoadRulesFromDir(dirPath string) error
}

type ruleEngine struct {
	dbRules    map[string]*RuleSet // rules grouped by database
	celEnv     *cel.Env
	programMap sync.Map // map[string]cel.Program
	query      query.Service
	mu         sync.RWMutex
}

func NewEngine(cfg authorizationconfig.Config, q query.Service) (Engine, error) {
	// Define CEL environment
	env, err := cel.NewEnv(
		cel.Declarations(
			decls.NewVar("request", decls.NewMapType(decls.String, decls.Dyn)),
			decls.NewVar("resource", decls.NewMapType(decls.String, decls.Dyn)),
		),
		cel.Lib(newAuthzLib(q)),
	)
	if err != nil {
		return nil, err
	}

	e := &ruleEngine{
		dbRules: make(map[string]*RuleSet),
		celEnv:  env,
		query:   q,
	}

	if cfg.RulesPath != "" {
		if err := e.LoadRulesFromDir(cfg.RulesPath); err != nil {
			return nil, fmt.Errorf("failed to load rules from %s: %w", cfg.RulesPath, err)
		}
	}

	return e, nil
}

// LoadRulesFromDir loads rules from all YAML files in a directory.
// Each file must have a database field. Same database in multiple files is rejected.
func (e *ruleEngine) LoadRulesFromDir(dirPath string) error {
	info, err := os.Stat(dirPath)
	if err != nil {
		return fmt.Errorf("failed to stat directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s", ErrNotDirectory, dirPath)
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return fmt.Errorf("failed to read directory: %w", err)
	}

	result := make(map[string]*RuleSet)
	// Track database -> source file for conflict detection
	seen := make(map[string]string)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}

		filePath := filepath.Join(dirPath, name)
		rules, err := loadRulesFromFile(filePath)
		if err != nil {
			return fmt.Errorf("file %s: %w", name, err)
		}

		if rules.Database == "" {
			return fmt.Errorf("file %s: %w", name, ErrEmptyDatabase)
		}

		// Check for duplicate database
		if existingFile, ok := seen[rules.Database]; ok {
			return fmt.Errorf("%w: database %q defined in both %s and %s",
				ErrDuplicateDatabase, rules.Database, existingFile, name)
		}
		seen[rules.Database] = name

		// Process match paths: replace {database} placeholder or validate explicit name
		if err := processMatchPaths(rules.Match, rules.Database); err != nil {
			return fmt.Errorf("file %s: %w", name, err)
		}

		// Validate rules
		if err := e.validateRules(rules); err != nil {
			return fmt.Errorf("file %s: %w", name, err)
		}

		result[rules.Database] = rules
	}

	e.mu.Lock()
	e.dbRules = result
	e.programMap = sync.Map{} // Clear cache
	e.mu.Unlock()

	return nil
}

// loadRulesFromFile loads a RuleSet from a YAML file.
func loadRulesFromFile(path string) (*RuleSet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	var rules RuleSet
	if err := yaml.Unmarshal(data, &rules); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	return &rules, nil
}

// processMatchPaths replaces {database} placeholder with actual database name
// or validates that explicit database names match the database field.
func processMatchPaths(blocks map[string]MatchBlock, database string) error {
	processed := make(map[string]MatchBlock)

	for pattern, block := range blocks {
		newPattern := pattern

		// Replace {database} placeholder
		if strings.Contains(pattern, "{database}") {
			newPattern = strings.ReplaceAll(pattern, "{database}", database)
		} else {
			// Check for explicit database name that doesn't match
			// Pattern like /databases/somedb/documents should match the database field
			if strings.HasPrefix(pattern, "/databases/") {
				parts := strings.SplitN(pattern, "/", 4) // ["", "databases", "dbname", "documents..."]
				if len(parts) >= 3 {
					explicitDB := parts[2]
					if explicitDB != database && explicitDB != "{database}" {
						return fmt.Errorf("%w: path has %q but database field is %q",
							ErrDatabaseMismatch, explicitDB, database)
					}
				}
			}
		}

		// Recursively process nested match blocks
		if len(block.Match) > 0 {
			if err := processMatchPaths(block.Match, database); err != nil {
				return err
			}
		}

		processed[newPattern] = block
	}

	// Replace original map with processed one
	for k := range blocks {
		delete(blocks, k)
	}
	for k, v := range processed {
		blocks[k] = v
	}

	return nil
}

func (e *ruleEngine) Evaluate(ctx context.Context, database string, path string, action string, req Request, existingRes *Resource) (bool, error) {
	// Check if user is a db_admin for this database - bypass all rules
	for _, db := range req.Auth.DBAdmin {
		if db == database {
			return true, nil
		}
	}

	e.mu.RLock()
	rules := e.dbRules[database]
	e.mu.RUnlock()

	if rules == nil {
		// No rules for this database - deny by default
		return false, nil
	}

	fullPath := "/databases/" + database + "/documents/" + strings.TrimPrefix(path, "/")
	return e.matchPath(ctx, rules.Match, fullPath, action, make(map[string]string), req, existingRes)
}

func (e *ruleEngine) matchPath(ctx context.Context, blocks map[string]MatchBlock, path string, action string, vars map[string]string, req Request, existingRes *Resource) (bool, error) {
	if len(blocks) == 0 {
		return false, nil
	}

	patterns := make([]string, 0, len(blocks))
	for p := range blocks {
		patterns = append(patterns, p)
	}

	sort.Slice(patterns, func(i, j int) bool {
		p1 := patterns[i]
		p2 := patterns[j]

		// 1. Concrete vs Wildcard (concrete first)
		isWild1 := strings.Contains(p1, "{")
		isWild2 := strings.Contains(p2, "{")
		if !isWild1 && isWild2 {
			return true
		}
		if isWild1 && !isWild2 {
			return false
		}

		// 2. Path length (longer first)
		if len(p1) != len(p2) {
			return len(p1) > len(p2)
		}

		// 3. Alphabetical (for determinism)
		return p1 < p2
	})

	for _, pattern := range patterns {
		block := blocks[pattern]
		matched, remaining, newVars := matchPattern(pattern, path)
		if matched {
			childVars := make(map[string]string)
			for k, v := range vars {
				childVars[k] = v
			}
			for k, v := range newVars {
				childVars[k] = v
			}

			if remaining == "" || remaining == "/" {
				return e.evaluateAllow(ctx, block.Allow, action, childVars, req, existingRes)
			}

			return e.matchPath(ctx, block.Match, remaining, action, childVars, req, existingRes)
		}
	}

	return false, nil
}

func matchPattern(pattern, path string) (bool, string, map[string]string) {
	pattern = strings.TrimPrefix(pattern, "/")
	path = strings.TrimPrefix(path, "/")

	patternParts := strings.Split(pattern, "/")
	pathParts := strings.Split(path, "/")

	if len(pathParts) < len(patternParts) {
		return false, "", nil
	}

	vars := make(map[string]string)

	for i, pPart := range patternParts {
		if strings.HasPrefix(pPart, "{") && strings.HasSuffix(pPart, "}") {
			varName := pPart[1 : len(pPart)-1]
			if strings.HasSuffix(varName, "=**") {
				realVarName := strings.TrimSuffix(varName, "=**")
				vars[realVarName] = strings.Join(pathParts[i:], "/")
				return true, "", vars
			}
			vars[varName] = pathParts[i]
		} else {
			if pPart != pathParts[i] {
				return false, "", nil
			}
		}
	}

	remaining := strings.Join(pathParts[len(patternParts):], "/")
	if remaining != "" {
		remaining = "/" + remaining
	}
	return true, remaining, vars
}

func (e *ruleEngine) evaluateAllow(ctx context.Context, allow map[string]string, action string, vars map[string]string, req Request, existingRes *Resource) (bool, error) {
	for actionsStr, condition := range allow {
		actions := strings.Split(actionsStr, ",")
		for _, a := range actions {
			a = strings.TrimSpace(a)
			if matchesAction(a, action) {
				result, err := e.evalCondition(ctx, condition, vars, req, existingRes)
				if err != nil {
					return false, err
				}
				if result {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func matchesAction(ruleAction, reqAction string) bool {
	if ruleAction == reqAction {
		return true
	}
	if ruleAction == "read" && (reqAction == "get" || reqAction == "list") {
		return true
	}
	if ruleAction == "write" && (reqAction == "create" || reqAction == "update" || reqAction == "delete") {
		return true
	}
	return false
}

func (e *ruleEngine) evalCondition(ctx context.Context, condition string, vars map[string]string, req Request, existingRes *Resource) (bool, error) {
	for k, v := range vars {
		condition = strings.ReplaceAll(condition, "$("+k+")", v)
	}

	prg, err := e.getProgram(condition, vars)
	if err != nil {
		return false, err
	}

	input := map[string]interface{}{
		"request":  structToMap(req),
		"resource": structToMap(existingRes),
	}
	for k, v := range vars {
		input[k] = v
	}

	out, _, err := prg.Eval(input)
	if err != nil {
		return false, err
	}

	return out.Value() == true, nil
}

func (e *ruleEngine) getProgram(expression string, vars map[string]string) (cel.Program, error) {
	varOpts := []cel.EnvOption{}
	for k := range vars {
		varOpts = append(varOpts, cel.Declarations(decls.NewVar(k, decls.String)))
	}

	env, err := e.celEnv.Extend(varOpts...)
	if err != nil {
		return nil, err
	}

	ast, issues := env.Compile(expression)
	if issues != nil && issues.Err() != nil {
		return nil, issues.Err()
	}

	return env.Program(ast)
}

func structToMap(v interface{}) map[string]interface{} {
	if v == nil {
		return nil
	}
	val := reflect.ValueOf(v)
	if val.Kind() == reflect.Ptr && val.IsNil() {
		return nil
	}
	b, _ := json.Marshal(v)
	var m map[string]interface{}
	json.Unmarshal(b, &m)
	return m
}

type authzLib struct {
	query query.Service
}

func newAuthzLib(q query.Service) *authzLib {
	return &authzLib{query: q}
}

func (l *authzLib) CompileOptions() []cel.EnvOption {
	return []cel.EnvOption{
		cel.Function("exists",
			cel.Overload("exists_string",
				[]*cel.Type{cel.StringType},
				cel.BoolType,
				cel.UnaryBinding(l.exists),
			),
		),
		cel.Function("get",
			cel.Overload("get_string",
				[]*cel.Type{cel.StringType},
				cel.MapType(cel.StringType, cel.DynType),
				cel.UnaryBinding(l.get),
			),
		),
	}
}

func (l *authzLib) ProgramOptions() []cel.ProgramOption {
	return []cel.ProgramOption{}
}

func (l *authzLib) exists(arg ref.Val) ref.Val {
	path, ok := arg.(types.String)
	if !ok {
		return types.NewErr("invalid argument to exists")
	}
	internalPath := stripDatabasePrefix(string(path))

	_, err := l.query.GetDocument(context.Background(), "default", internalPath)
	if err == model.ErrNotFound {
		return types.Bool(false)
	}
	if err != nil {
		return types.NewErr("error in exists: %v", err)
	}
	return types.Bool(true)
}

func (l *authzLib) get(arg ref.Val) ref.Val {
	path, ok := arg.(types.String)
	if !ok {
		return types.NewErr("invalid argument to get")
	}
	internalPath := stripDatabasePrefix(string(path))

	doc, err := l.query.GetDocument(context.Background(), "default", internalPath)
	if err != nil {
		return types.NewErr("error in get: %v", err)
	}

	data := model.Document{}
	for k, v := range doc {
		data[k] = v
	}
	data.StripProtectedFields()

	res := map[string]interface{}{
		"data": data,
		"id":   doc.GetID(),
	}
	return types.DefaultTypeAdapter.NativeToValue(res)
}

func stripDatabasePrefix(path string) string {
	parts := strings.Split(path, "/documents/")
	if len(parts) > 1 {
		return parts[1]
	}
	return path
}

func (e *ruleEngine) GetRules() *RuleSet {
	e.mu.RLock()
	defer e.mu.RUnlock()
	// Return the first ruleset for backward compatibility (or nil if empty)
	for _, rules := range e.dbRules {
		return rules
	}
	return nil
}

// GetRulesForDatabase returns rules for a specific database.
func (e *ruleEngine) GetRulesForDatabase(database string) *RuleSet {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.dbRules[database]
}

func (e *ruleEngine) UpdateRules(database string, content []byte) error {
	var rules RuleSet
	if err := yaml.Unmarshal(content, &rules); err != nil {
		return err
	}

	// Set database if not specified
	if rules.Database == "" {
		rules.Database = database
	} else if rules.Database != database {
		return fmt.Errorf("%w: content has database %q but updating %q",
			ErrDatabaseMismatch, rules.Database, database)
	}

	// Process match paths
	if err := processMatchPaths(rules.Match, database); err != nil {
		return err
	}

	if err := e.validateRules(&rules); err != nil {
		return err
	}

	e.mu.Lock()
	e.dbRules[database] = &rules
	e.programMap = sync.Map{} // Clear cache
	e.mu.Unlock()
	return nil
}

func (e *ruleEngine) validateRules(rules *RuleSet) error {
	return e.validateMatchBlocks(rules.Match, []string{})
}

func (e *ruleEngine) validateMatchBlocks(blocks map[string]MatchBlock, parentVars []string) error {
	for pattern, block := range blocks {
		vars := extractVars(pattern)
		currentVars := append([]string{}, parentVars...)
		currentVars = append(currentVars, vars...)

		varOpts := []cel.EnvOption{}
		for _, v := range currentVars {
			varOpts = append(varOpts, cel.Declarations(decls.NewVar(v, decls.String)))
		}
		// Add dummy_var for validation of $(var) replacement
		varOpts = append(varOpts, cel.Declarations(decls.NewVar("dummy_var", decls.String)))

		env, err := e.celEnv.Extend(varOpts...)
		if err != nil {
			return err
		}

		for _, condition := range block.Allow {
			// Replace $(var) with dummy_var for validation
			cleanCondition := condition
			for _, v := range currentVars {
				cleanCondition = strings.ReplaceAll(cleanCondition, "$("+v+")", "dummy_var")
			}

			if _, issues := env.Compile(cleanCondition); issues != nil && issues.Err() != nil {
				return fmt.Errorf("invalid CEL expression %q: %w", condition, issues.Err())
			}
		}
		if err := e.validateMatchBlocks(block.Match, currentVars); err != nil {
			return err
		}
	}
	return nil
}

func extractVars(pattern string) []string {
	var vars []string
	parts := strings.Split(pattern, "/")
	for _, part := range parts {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			varName := part[1 : len(part)-1]
			if strings.HasSuffix(varName, "=**") {
				varName = strings.TrimSuffix(varName, "=**")
			}
			vars = append(vars, varName)
		}
	}
	return vars
}
