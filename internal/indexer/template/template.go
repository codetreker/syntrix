// Package template provides index template loading, validation, and matching.
package template

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/syntrixbase/syntrix/internal/indexer/encoding"
	"github.com/syntrixbase/syntrix/pkg/model"
	"gopkg.in/yaml.v3"
)

// Direction specifies sort order for a field.
type Direction string

const (
	Asc  Direction = "asc"
	Desc Direction = "desc"
)

type Mode string

const (
	Scalar     Mode = "scalar"
	Membership Mode = "membership"
)

// Field represents a scalar ordering or immediate array membership dimension.
type Field struct {
	Field string    `yaml:"field"`
	Order Direction `yaml:"order"`
	Mode  Mode      `yaml:"mode,omitempty"`
}

// Template defines an index template.
type Template struct {
	Name              string  `yaml:"name"`
	CollectionPattern string  `yaml:"collectionPattern"`
	Fields            []Field `yaml:"fields"`
	IncludeDeleted    bool    `yaml:"includeDeleted"`
}

// Identity returns a unique identifier for the template.
// Uses name if provided, otherwise generates from fields signature.
func (t *Template) Identity() string {
	if t.Name != "" {
		return t.Name
	}
	// Generate from fields
	var parts []string
	for _, f := range t.Fields {
		if f.Mode == Membership {
			parts = append(parts, fmt.Sprintf("%s:membership", f.Field))
		} else {
			parts = append(parts, fmt.Sprintf("%s:%s", f.Field, f.Order))
		}
	}
	return strings.Join(parts, ",")
}

// Fingerprint binds the full admitted template semantics to the scalar codec.
func (t *Template) Fingerprint() string {
	fields := append([]Field(nil), t.Fields...)
	for i := range fields {
		if fields[i].Mode == "" {
			fields[i].Mode = Scalar
		}
		if fields[i].Mode == Membership {
			fields[i].Order = Asc
		}
	}
	payload, _ := json.Marshal(struct {
		Pattern        string
		Fields         []Field
		IncludeDeleted bool
		Codec          byte
	}{t.NormalizedPattern(), fields, t.IncludeDeleted, encoding.Version})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// NormalizedPattern returns the pattern with variables replaced by *.
func (t *Template) NormalizedPattern() string {
	return NormalizePattern(t.CollectionPattern)
}

// Errors
var (
	ErrEmptyPattern       = errors.New("collection pattern cannot be empty")
	ErrInvalidPattern     = errors.New("invalid collection pattern")
	ErrEmptySegment       = errors.New("pattern contains empty segment")
	ErrDocumentLevel      = errors.New("pattern targets document level, not collection")
	ErrNoFields           = errors.New("template must have at least one field")
	ErrInvalidDirection   = errors.New("field order must be 'asc' or 'desc'")
	ErrDuplicateField     = errors.New("duplicate field in template")
	ErrDuplicateTemplate  = errors.New("duplicate template definition")
	ErrConflictingPattern = errors.New("conflicting patterns with same priority")
	ErrEmptyDatabase      = errors.New("database field cannot be empty")
	ErrNotDirectory       = errors.New("path is not a directory")
)

// varPattern matches {anything} in collection patterns.
var varPattern = regexp.MustCompile(`^\{[^}]+\}$`)

// Config represents the templates configuration file.
type Config struct {
	Database  string     `yaml:"database"`
	Templates []Template `yaml:"templates"`
}

// LoadFromFile loads templates from a YAML file.
func LoadFromFile(path string) ([]Template, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read template file: %w", err)
	}
	return LoadFromBytes(data)
}

// LoadFromBytes parses templates from YAML bytes.
func LoadFromBytes(data []byte) ([]Template, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse templates: %w", err)
	}

	// Validate each template
	for i := range cfg.Templates {
		if err := ValidateTemplate(&cfg.Templates[i]); err != nil {
			return nil, fmt.Errorf("template %q: %w", cfg.Templates[i].Name, err)
		}
	}

	// Validate no duplicates
	if err := ValidateTemplates(cfg.Templates); err != nil {
		return nil, err
	}

	return cfg.Templates, nil
}

// ValidateTemplate validates a single template definition.
func ValidateTemplate(t *Template) error {
	// Validate pattern
	if t.CollectionPattern == "" {
		return ErrEmptyPattern
	}

	segments := strings.Split(t.CollectionPattern, "/")
	for _, seg := range segments {
		if seg == "" {
			return ErrEmptySegment
		}
	}

	// Check if pattern targets document level (even number of segments typically means document)
	// Actually, collection patterns should have odd number of segments or end with variable
	// For simplicity, we allow any valid segment pattern

	// Validate fields
	if len(t.Fields) == 0 {
		return ErrNoFields
	}

	seen := make(map[string]bool)
	membershipFields := 0
	for _, f := range t.Fields {
		if err := model.ValidateQueryField(f.Field); err != nil {
			return err
		}
		switch f.Mode {
		case "", Scalar:
			if f.Order != Asc && f.Order != Desc {
				return fmt.Errorf("field %q: %w", f.Field, ErrInvalidDirection)
			}
		case Membership:
			membershipFields++
			if membershipFields > 1 {
				return fmt.Errorf("template allows at most one membership field")
			}
			if f.Order != "" && f.Order != Asc {
				return fmt.Errorf("membership field %q requires ascending encoding", f.Field)
			}
			switch f.Field {
			case "id", "collection", "version", "createdAt", "updatedAt", "deleted":
				return fmt.Errorf("metadata field %q cannot be a membership dimension", f.Field)
			}
		default:
			return fmt.Errorf("field %q: invalid mode %q", f.Field, f.Mode)
		}
		if seen[f.Field] {
			return fmt.Errorf("field %q: %w", f.Field, ErrDuplicateField)
		}
		seen[f.Field] = true
	}

	return nil
}

// ValidateTemplates checks for duplicate template definitions.
// Duplicate = same (normalizedPattern, templateIdentity).
func ValidateTemplates(templates []Template) error {
	seen := make(map[string]*Template)
	fingerprints := make(map[string]bool)
	for i := range templates {
		t := &templates[i]
		norm := t.NormalizedPattern()
		identity := t.Identity()
		key := norm + "|" + identity

		if existing, ok := seen[key]; ok {
			return fmt.Errorf("%w: %q and %q have same pattern and identity",
				ErrDuplicateTemplate, existing.Name, t.Name)
		}
		if fingerprints[t.Fingerprint()] {
			return fmt.Errorf("%w: duplicate template semantics", ErrDuplicateTemplate)
		}
		fingerprints[t.Fingerprint()] = true
		seen[key] = t
	}
	return nil
}

// NormalizePattern replaces {var} placeholders with * for comparison.
func NormalizePattern(pattern string) string {
	segments := strings.Split(pattern, "/")
	for i, seg := range segments {
		if varPattern.MatchString(seg) {
			segments[i] = "*"
		}
	}
	return strings.Join(segments, "/")
}

// MatchResult contains a matched template with its priority score.
type MatchResult struct {
	Template *Template
	Score    PatternScore
}

// PatternScore represents the priority of a pattern match.
type PatternScore struct {
	FixedSegments int // Number of non-variable segments
	TotalSegments int // Total number of segments
}

// Less returns true if this score is lower priority than other.
func (s PatternScore) Less(other PatternScore) bool {
	if s.FixedSegments != other.FixedSegments {
		return s.FixedSegments < other.FixedSegments
	}
	return s.TotalSegments < other.TotalSegments
}

// Equal returns true if scores are equal.
func (s PatternScore) Equal(other PatternScore) bool {
	return s.FixedSegments == other.FixedSegments && s.TotalSegments == other.TotalSegments
}

// MatchTemplates finds all templates that match a collection path.
// Returns templates sorted by priority (highest first).
func MatchTemplates(path string, templates []Template) []MatchResult {
	pathSegments := strings.Split(path, "/")
	var results []MatchResult

	for i := range templates {
		t := &templates[i]
		patternSegments := strings.Split(t.CollectionPattern, "/")

		// Must have same number of segments
		if len(pathSegments) != len(patternSegments) {
			continue
		}

		// Check each segment matches
		if !segmentsMatch(pathSegments, patternSegments) {
			continue
		}

		// Calculate score
		score := calculateScore(patternSegments)
		results = append(results, MatchResult{Template: t, Score: score})
	}

	// Sort by score (highest first)
	sort.Slice(results, func(i, j int) bool {
		return results[j].Score.Less(results[i].Score)
	})

	return results
}

// segmentsMatch checks if path segments match pattern segments.
func segmentsMatch(path, pattern []string) bool {
	for i := range path {
		if varPattern.MatchString(pattern[i]) || pattern[i] == "*" {
			// Variable matches any single segment
			continue
		}
		if path[i] != pattern[i] {
			return false
		}
	}
	return true
}

// calculateScore computes the priority score for a pattern.
func calculateScore(segments []string) PatternScore {
	fixed := 0
	for _, seg := range segments {
		if !varPattern.MatchString(seg) && seg != "*" {
			fixed++
		}
	}
	return PatternScore{
		FixedSegments: fixed,
		TotalSegments: len(segments),
	}
}

// SelectBestTemplates returns the templates with highest priority.
// If multiple templates have the same priority, all are returned.
func SelectBestTemplates(results []MatchResult) []MatchResult {
	if len(results) == 0 {
		return nil
	}

	bestScore := results[0].Score
	var best []MatchResult
	for _, r := range results {
		if r.Score.Equal(bestScore) {
			best = append(best, r)
		} else {
			break // Results are sorted, so we can stop
		}
	}
	return best
}

// DatabaseTemplates holds templates grouped by database.
type DatabaseTemplates map[string][]Template

// LoadFromDir loads templates from all YAML files in a directory.
// Templates are grouped by the database field in each file.
// Same database can span multiple files; templates are merged.
// Returns error if duplicate (database, name) is found.
func LoadFromDir(dirPath string) (DatabaseTemplates, error) {
	info, err := os.Stat(dirPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s", ErrNotDirectory, dirPath)
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	result := make(DatabaseTemplates)
	// Track (database, name) -> source file for conflict detection
	seen := make(map[string]string) // key: "database|name", value: filename

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}

		filePath := filepath.Join(dirPath, name)
		cfg, err := loadConfigFromFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("file %s: %w", name, err)
		}

		if cfg.Database == "" {
			return nil, fmt.Errorf("file %s: %w", name, ErrEmptyDatabase)
		}

		// Validate each template
		for i := range cfg.Templates {
			if err := ValidateTemplate(&cfg.Templates[i]); err != nil {
				return nil, fmt.Errorf("file %s, template %q: %w", name, cfg.Templates[i].Name, err)
			}

			// Check for duplicate (database, name)
			tmplName := cfg.Templates[i].Identity()
			key := cfg.Database + "|" + tmplName
			if existingFile, ok := seen[key]; ok {
				return nil, fmt.Errorf("%w: template %q in database %q defined in both %s and %s",
					ErrDuplicateTemplate, tmplName, cfg.Database, existingFile, name)
			}
			seen[key] = name
		}

		// Merge templates into result
		result[cfg.Database] = append(result[cfg.Database], cfg.Templates...)
	}

	// Validate within each database (pattern+identity uniqueness)
	for db, templates := range result {
		if err := ValidateTemplates(templates); err != nil {
			return nil, fmt.Errorf("database %q: %w", db, err)
		}
	}

	return result, nil
}

// loadConfigFromFile loads a Config from a YAML file.
func loadConfigFromFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	return &cfg, nil
}

// AllTemplates returns all templates from all databases as a flat list.
func (dt DatabaseTemplates) AllTemplates() []Template {
	var all []Template
	for _, templates := range dt {
		all = append(all, templates...)
	}
	return all
}

// Databases returns a sorted list of database names.
func (dt DatabaseTemplates) Databases() []string {
	dbs := make([]string, 0, len(dt))
	for db := range dt {
		dbs = append(dbs, db)
	}
	sort.Strings(dbs)
	return dbs
}
