package manager

import (
	"bytes"
	"container/heap"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/syntrixbase/syntrix/internal/indexer/encoding"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
	"github.com/syntrixbase/syntrix/internal/indexer/template"
	"github.com/syntrixbase/syntrix/pkg/model"
)

var ErrStaleCursor = errors.New("stale index cursor")

const MaxCandidateBranches = 128
const MaxCandidatePositionBytes = 4096

// CandidateStream owns a read view and returns complete groups at a common order position.
type CandidateStream interface {
	Metadata() CandidateMetadata
	Next() (CandidateGroup, bool, error)
	Examined() int64
	Close() error
}

type CandidateGroup struct {
	ID       string
	Position []byte
	Branches []int
}

type CandidateBranch struct {
	Prefix      []byte
	FixedFields int
	Lower       []byte
	Upper       []byte
}

type PredicateAssignment struct {
	Predicate int
	Access    bool
	Residual  bool
}

type CandidateMetadata struct {
	Template            template.Template
	TemplateFingerprint string
	Generation          string
	BranchHash          string
	EffectiveOrder      []OrderField
	Branches            []CandidateBranch
	Assignments         []PredicateAssignment
}

// tupleParts preserves the encoded direction of each prefix-free component.
func tupleParts(key []byte) ([][]byte, error) {
	if len(key) == 0 || key[0] != encoding.Version {
		return nil, encoding.ErrInvalidOrderKey
	}
	var parts [][]byte
	for offset := 1; offset < len(key); {
		start := offset
		desc := key[offset] > 0x80
		done := false
		for offset < len(key) {
			b := key[offset]
			offset++
			if desc {
				b = ^b
			}
			if b != 0 {
				continue
			}
			if offset == len(key) {
				return nil, encoding.ErrInvalidOrderKey
			}
			b = key[offset]
			offset++
			if desc {
				b = ^b
			}
			if b == 0 {
				done = true
				break
			}
			if b != 1 {
				return nil, encoding.ErrInvalidOrderKey
			}
		}
		if !done {
			return nil, encoding.ErrInvalidOrderKey
		}
		parts = append(parts, key[start:offset])
	}
	return parts, nil
}

func orderComponents(order []OrderField) []OrderField {
	if len(order) > 0 && order[len(order)-1].Field == "id" && order[len(order)-1].Direction == encoding.Asc {
		return order[:len(order)-1]
	}
	return order
}

// PostingKey reconstructs a physical posting from its bounded branch proof and K.
func (m CandidateMetadata) PostingKey(group CandidateGroup, branchID int) ([]byte, error) {
	if branchID < 0 || branchID >= len(m.Branches) {
		return nil, ErrInvalidPlan
	}
	parts, err := tupleParts(group.Position)
	if err != nil {
		return nil, err
	}
	order := orderComponents(m.EffectiveOrder)
	if len(parts) != len(order)+1 {
		return nil, encoding.ErrInvalidOrderKey
	}
	values := make(map[string][]byte, len(order)+1)
	for i, f := range order {
		values[f.Field] = parts[i]
	}
	logicalID := parts[len(parts)-1]
	if _, ok := values["id"]; !ok {
		values["id"] = parts[len(parts)-1]
	}
	branch := m.Branches[branchID]
	key := bytes.Clone(branch.Prefix)
	for _, field := range m.Template.Fields[branch.FixedFields:] {
		part, ok := values[field.Field]
		if !ok {
			return nil, ErrInvalidPlan
		}
		key = append(key, part...)
	}
	return append(key, logicalID...), nil
}

// scanOptions maps an arbitrary common boundary into each branch's physical order.
// A fixed field can sort before or after the cursor even when its suffix is equal.
func (m CandidateMetadata) scanOptions(branchID int, after []byte) (store.SearchOptions, error) {
	branch := m.Branches[branchID]
	opts := store.SearchOptions{Lower: branch.Lower, Upper: branch.Upper}
	if len(after) == 0 {
		return opts, nil
	}
	parts, err := tupleParts(after)
	if err != nil {
		return opts, err
	}
	order := orderComponents(m.EffectiveOrder)
	if len(parts) != len(order)+1 {
		return opts, encoding.ErrInvalidOrderKey
	}
	fixedParts, err := tupleParts(branch.Prefix)
	if err != nil {
		return opts, err
	}
	fixed := make(map[string][]byte, len(fixedParts))
	for i, part := range fixedParts {
		fixed[m.Template.Fields[i].Field] = part
	}
	seek := bytes.Clone(branch.Prefix)
	for i, field := range order {
		if value, ok := fixed[field.Field]; ok {
			cmp := bytes.Compare(value, parts[i])
			if cmp == 0 {
				continue
			}
			if cmp < 0 {
				seek = PrefixSuccessor(seek)
			}
			if seek == nil {
				opts.Lower = branch.Upper
			} else if bytes.Compare(seek, opts.Lower) > 0 {
				opts.Lower = seek
			}
			return opts, nil
		}
		seek = append(seek, parts[i]...)
	}
	seek, err = m.PostingKey(CandidateGroup{Position: after}, branchID)
	if err != nil {
		return opts, err
	}
	opts.StartAfter = seek
	return opts, nil
}

func (m CandidateMetadata) position(posting []byte) ([]byte, error) {
	parts, err := tupleParts(posting)
	if err != nil {
		return nil, err
	}
	if len(parts) != len(m.Template.Fields)+1 {
		return nil, encoding.ErrInvalidOrderKey
	}
	values := make(map[string][]byte, len(parts))
	for i, f := range m.Template.Fields {
		values[f.Field] = parts[i]
	}
	values["id"] = parts[len(parts)-1]
	out := []byte{encoding.Version}
	for _, f := range orderComponents(m.EffectiveOrder) {
		part, ok := values[f.Field]
		if !ok {
			return nil, ErrInvalidPlan
		}
		// An explicitly descending ID can be backed by an indexed ID dimension.
		if f.Field == "id" && f.Direction == encoding.Desc {
			for i, tf := range m.Template.Fields {
				if tf.Field == "id" {
					part = parts[i]
				}
			}
		}
		out = append(out, part...)
	}
	out = append(out, parts[len(parts)-1]...)
	if len(out) > MaxCandidatePositionBytes {
		return nil, store.ErrWorkLimit
	}
	return out, nil
}

func PrefixSuccessor(prefix []byte) []byte {
	for i := len(prefix) - 1; i >= 0; i-- {
		if prefix[i] != 0xff {
			out := bytes.Clone(prefix[:i+1])
			out[i]++
			return out
		}
	}
	return nil
}

func candidateFilters(filters []Filter) (model.Filters, error) {
	out := make(model.Filters, len(filters))
	ops := map[FilterOp]model.FilterOp{FilterEq: model.OpEq, FilterNe: model.OpNe, FilterGt: model.OpGt, FilterGte: model.OpGte, FilterLt: model.OpLt, FilterLte: model.OpLte, FilterIn: model.OpIn, FilterContains: model.OpContains}
	for i, f := range filters {
		op, ok := ops[f.Op]
		if !ok {
			return nil, fmt.Errorf("%w: unknown operator %q", ErrInvalidPlan, f.Op)
		}
		out[i] = model.Filter{Field: f.Field, Op: op, Value: f.Value}
	}
	out, err := model.NormalizeFilters(out)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidPlan, err)
	}
	for _, f := range out {
		if f.Op == model.OpIn && len(f.Value.([]any)) > 256 {
			return nil, fmt.Errorf("%w: too many in values", ErrInvalidPlan)
		}
	}
	return out, nil
}

func fieldDirection(f template.Field) encoding.Direction {
	if f.Order == template.Desc {
		return encoding.Desc
	}
	return encoding.Asc
}
func fieldBytes(value any, dir encoding.Direction) ([]byte, error) {
	key, err := encoding.EncodePrefix([]encoding.Field{{Value: value, Direction: dir}})
	if err != nil {
		return nil, err
	}
	return key[1:], nil
}

type candidateChoice struct {
	metadata CandidateMetadata
	score    int
	pattern  template.PatternScore
}

func compileCandidate(t template.Template, filters model.Filters, plan Plan) (CandidateMetadata, int, error) {
	metadata := CandidateMetadata{Template: t, TemplateFingerprint: t.Fingerprint()}
	metadata.Assignments = make([]PredicateAssignment, len(filters))
	for i := range filters {
		metadata.Assignments[i] = PredicateAssignment{Predicate: i, Residual: true}
	}
	branches := []CandidateBranch{{Prefix: []byte{encoding.Version}}}
	fixed := 0
	access := 0
	empty := false
	expansionExceeded := false
	for index, field := range t.Fields {
		var points []any
		pointConstrained := false
		if field.Mode == template.Membership {
			for i, f := range filters {
				if f.Field == field.Field && f.Op == model.OpContains {
					points = []any{f.Value}
					pointConstrained = true
					metadata.Assignments[i].Access = true
					access++
					break
				}
			}
			if !pointConstrained {
				return metadata, 0, ErrNoMatchingIndex
			}
		} else {
			for _, f := range filters {
				if f.Field == field.Field && (f.Op == model.OpEq || f.Op == model.OpIn) {
					pointConstrained = true
					if f.Op == model.OpEq {
						points = []any{f.Value}
					} else {
						points = f.Value.([]any)
					}
					break
				}
			}
			if pointConstrained {
				admitted := make([]any, 0, len(points))
				for _, value := range points {
					matches := true
					for _, f := range filters {
						if f.Field == field.Field {
							ok, err := model.EvaluateFilter(f, value, true)
							if err != nil {
								return metadata, 0, err
							}
							matches = matches && ok
						}
					}
					if matches {
						admitted = append(admitted, value)
					}
				}
				points = admitted
				for i, f := range filters {
					if f.Field == field.Field {
						metadata.Assignments[i].Access = true
						access++
					}
				}
			}
		}
		if !pointConstrained {
			break
		}
		fixed = index + 1
		if len(points) == 0 {
			empty = true
			points = []any{nil}
		}
		sort.Slice(points, func(i, j int) bool {
			a, _ := model.ScalarKey(points[i])
			b, _ := model.ScalarKey(points[j])
			return bytes.Compare(a, b) < 0
		})
		if len(branches)*len(points) > MaxCandidateBranches {
			expansionExceeded = true
			branches = branches[:1]
			points = points[:1]
		}
		next := make([]CandidateBranch, 0, len(branches)*len(points))
		for _, branch := range branches {
			for _, value := range points {
				part, err := fieldBytes(value, fieldDirection(field))
				if err != nil {
					return metadata, 0, err
				}
				prefix := append(bytes.Clone(branch.Prefix), part...)
				next = append(next, CandidateBranch{Prefix: prefix, FixedFields: fixed})
			}
		}
		branches = next
	}
	if expansionExceeded && !empty {
		return metadata, 0, fmt.Errorf("%w: too many candidate branches", ErrInvalidPlan)
	}
	for _, field := range t.Fields[fixed:] {
		if field.Mode == template.Membership {
			return metadata, 0, ErrNoMatchingIndex
		}
	}
	if len(plan.OrderBy) == 0 {
		for _, f := range t.Fields {
			if f.Mode != template.Membership {
				metadata.EffectiveOrder = append(metadata.EffectiveOrder, OrderField{Field: f.Field, Direction: fieldDirection(f)})
			}
		}
	} else {
		metadata.EffectiveOrder = append([]OrderField(nil), plan.OrderBy...)
	}
	hasID := false
	for _, f := range metadata.EffectiveOrder {
		hasID = hasID || f.Field == "id"
	}
	if !hasID {
		metadata.EffectiveOrder = append(metadata.EffectiveOrder, OrderField{Field: "id", Direction: encoding.Asc})
	}
	fixedFields := map[string]bool{}
	for _, f := range t.Fields[:fixed] {
		fixedFields[f.Field] = true
	}
	var variableOrder []OrderField
	for _, f := range orderComponents(metadata.EffectiveOrder) {
		found := f.Field == "id" && f.Direction == encoding.Asc
		for _, tf := range t.Fields {
			if tf.Field == f.Field {
				found = tf.Mode != template.Membership && fieldDirection(tf) == f.Direction
				break
			}
		}
		if !found {
			return metadata, 0, ErrNoMatchingIndex
		}
		if !fixedFields[f.Field] {
			variableOrder = append(variableOrder, f)
		}
	}
	remaining := t.Fields[fixed:]
	if len(remaining) > 0 && remaining[len(remaining)-1].Field == "id" && fieldDirection(remaining[len(remaining)-1]) == encoding.Asc {
		remaining = remaining[:len(remaining)-1]
	}
	if len(variableOrder) != len(remaining) {
		return metadata, 0, ErrNoMatchingIndex
	}
	for i, f := range remaining {
		if variableOrder[i].Field != f.Field || variableOrder[i].Direction != fieldDirection(f) {
			return metadata, 0, ErrNoMatchingIndex
		}
	}
	bounded := make([]CandidateBranch, 0, len(branches))
	for _, branch := range branches {
		intervals := []keyInterval{{lower: branch.Prefix, upper: PrefixSuccessor(branch.Prefix)}}
		if fixed < len(t.Fields) {
			field := t.Fields[fixed]
			for i, f := range filters {
				if f.Field != field.Field || f.Op == model.OpContains {
					continue
				}
				constraint, err := filterIntervals(branch.Prefix, fieldDirection(field), f)
				if err != nil {
					return metadata, 0, err
				}
				intervals = intersectIntervals(intervals, constraint)
				metadata.Assignments[i].Access = true
				access++
			}
		}
		for _, interval := range intervals {
			b := branch
			b.Lower = interval.lower
			b.Upper = interval.upper
			bounded = append(bounded, b)
		}
	}
	if access == 0 && len(plan.OrderBy) == 0 {
		return metadata, 0, ErrNoMatchingIndex
	}
	if len(bounded) > MaxCandidateBranches {
		return metadata, 0, fmt.Errorf("%w: too many candidate branches", ErrInvalidPlan)
	}
	if empty {
		bounded = nil
	}
	encodedBytes := 0
	for _, branch := range bounded {
		encodedBytes += len(branch.Prefix) + len(branch.Lower) + len(branch.Upper)
	}
	if encodedBytes > 1<<20 {
		return metadata, 0, fmt.Errorf("%w: candidate manifest exceeds byte limit", ErrInvalidPlan)
	}
	metadata.Branches = bounded
	payload, err := json.Marshal(struct {
		Branches []CandidateBranch
		Order    []OrderField
	}{bounded, metadata.EffectiveOrder})
	if err != nil {
		return metadata, 0, err
	}
	hash := sha256.Sum256(payload)
	metadata.BranchHash = hex.EncodeToString(hash[:])
	return metadata, access, nil
}

type keyInterval struct{ lower, upper []byte }

func intersectIntervals(left, right []keyInterval) []keyInterval {
	var out []keyInterval
	for _, a := range left {
		for _, b := range right {
			lo := a.lower
			if bytes.Compare(b.lower, lo) > 0 {
				lo = b.lower
			}
			hi := a.upper
			if hi == nil || (b.upper != nil && bytes.Compare(b.upper, hi) < 0) {
				hi = b.upper
			}
			if hi == nil || bytes.Compare(lo, hi) < 0 {
				out = append(out, keyInterval{lo, hi})
			}
		}
	}
	return out
}
func filterIntervals(prefix []byte, dir encoding.Direction, f model.Filter) ([]keyInterval, error) {
	point, err := fieldBytes(f.Value, dir)
	if f.Op == model.OpIn {
		var out []keyInterval
		for _, v := range f.Value.([]any) {
			part, e := fieldBytes(v, dir)
			if e != nil {
				return nil, e
			}
			p := append(bytes.Clone(prefix), part...)
			out = append(out, keyInterval{p, PrefixSuccessor(p)})
		}
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	p := append(bytes.Clone(prefix), point...)
	next := PrefixSuccessor(p)
	whole := keyInterval{prefix, PrefixSuccessor(prefix)}
	if f.Op == model.OpEq {
		return []keyInterval{{p, next}}, nil
	}
	if f.Op == model.OpNe {
		missing, _ := encoding.EncodePrefix([]encoding.Field{{Missing: true, Direction: dir}})
		missingPoint := append(bytes.Clone(prefix), missing[1:]...)
		present := whole
		if dir == encoding.Asc {
			present.lower = PrefixSuccessor(missingPoint)
		} else {
			present.upper = missingPoint
		}
		return intersectIntervals([]keyInterval{present}, []keyInterval{{whole.lower, p}, {next, whole.upper}}), nil
	}
	raw, err := model.ScalarKey(f.Value)
	if err != nil {
		return nil, err
	}
	tag := raw[0]
	var family keyInterval
	if dir == encoding.Asc {
		family = keyInterval{append(bytes.Clone(prefix), tag), append(bytes.Clone(prefix), tag+1)}
	} else {
		family = keyInterval{append(bytes.Clone(prefix), ^tag), append(bytes.Clone(prefix), ^tag+1)}
	}
	op := f.Op
	if dir == encoding.Desc {
		switch op {
		case model.OpGt:
			op = model.OpLt
		case model.OpGte:
			op = model.OpLte
		case model.OpLt:
			op = model.OpGt
		case model.OpLte:
			op = model.OpGte
		}
	}
	bound := whole
	switch op {
	case model.OpGt:
		if next == nil {
			return nil, nil
		}
		bound.lower = next
	case model.OpGte:
		bound.lower = p
	case model.OpLt:
		bound.upper = p
	case model.OpLte:
		bound.upper = next
	default:
		return nil, ErrInvalidPlan
	}
	return intersectIntervals([]keyInterval{family}, []keyInterval{bound}), nil
}

func (m *Manager) OpenCandidates(ctx context.Context, database string, plan Plan) (CandidateStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if database == "" || plan.Collection == "" || plan.MaxExamined < 0 {
		return nil, ErrInvalidPlan
	}
	filters, err := candidateFilters(plan.Filters)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, order := range plan.OrderBy {
		if err := model.ValidateQueryField(order.Field); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidPlan, err)
		}
		if seen[order.Field] || (order.Direction != encoding.Asc && order.Direction != encoding.Desc) {
			return nil, ErrInvalidPlan
		}
		seen[order.Field] = true
	}
	if len(plan.AfterPosition) > MaxCandidatePositionBytes {
		return nil, ErrInvalidPlan
	}
	m.mu.RLock()
	templates := m.templates
	if len(m.dbTemplates) > 0 {
		templates = m.dbTemplates[database]
	}
	templates = append([]template.Template(nil), templates...)
	m.mu.RUnlock()
	var best *candidateChoice
	var planningErr error
	for _, match := range template.MatchTemplates(plan.Collection, templates) {
		if plan.ShowDeleted && !match.Template.IncludeDeleted {
			continue
		}
		if plan.TemplateFingerprint != "" && plan.TemplateFingerprint != match.Template.Fingerprint() {
			continue
		}
		metadata, score, err := compileCandidate(*match.Template, filters, plan)
		if err != nil {
			if !errors.Is(err, ErrNoMatchingIndex) {
				planningErr = err
			}
			continue
		}
		choice := candidateChoice{metadata: metadata, score: score, pattern: match.Score}
		if best == nil || best.pattern.Less(choice.pattern) || (best.pattern.Equal(choice.pattern) && (choice.score > best.score || (choice.score == best.score && choice.metadata.Template.Name < best.metadata.Template.Name))) {
			best = &choice
		}
	}
	if best == nil {
		if plan.TemplateFingerprint != "" {
			return nil, ErrStaleCursor
		}
		if planningErr != nil {
			return nil, planningErr
		}
		return nil, ErrNoMatchingIndex
	}
	metadata := best.metadata
	if len(plan.AfterPosition) > 0 {
		parts, err := tupleParts(plan.AfterPosition)
		if err != nil || len(parts) != len(orderComponents(metadata.EffectiveOrder))+1 {
			return nil, fmt.Errorf("%w: invalid position", ErrInvalidPlan)
		}
		if _, err := encoding.ExtractDocID(plan.AfterPosition); err != nil {
			return nil, fmt.Errorf("%w: invalid position", ErrInvalidPlan)
		}
	}
	generation, found, err := m.store.ReadGeneration(database, plan.Collection, metadata.TemplateFingerprint)
	if err != nil {
		return nil, err
	}
	if !found || !generation.Ready || generation.Failure != "" {
		return nil, ErrIndexNotReady
	}
	metadata.Generation = generation.ID
	if (plan.Generation != "" && plan.Generation != metadata.Generation) || (plan.BranchHash != "" && plan.BranchHash != metadata.BranchHash) {
		return nil, ErrStaleCursor
	}
	stream := &candidateStream{ctx: ctx, metadata: metadata, maxExamined: plan.MaxExamined}
	if stream.maxExamined == 0 {
		stream.maxExamined = 100000
	}
	if len(metadata.Branches) == 0 {
		return stream, nil
	}
	view, err := m.store.ReadView(ctx, store.QueryIndexRef{Database: database, Collection: plan.Collection, TemplateFingerprint: metadata.TemplateFingerprint, Generation: metadata.Generation}, store.ReadBudget{MaxExamined: stream.maxExamined})
	if err != nil {
		return nil, err
	}
	stream.view = view
	for i := range metadata.Branches {
		opts, err := metadata.scanOptions(i, plan.AfterPosition)
		if err != nil {
			stream.Close()
			return nil, fmt.Errorf("%w: invalid position", ErrInvalidPlan)
		}
		iterator, err := view.Scan(ctx, opts)
		if err != nil {
			stream.Close()
			return nil, err
		}
		stream.iterators = append(stream.iterators, iterator)
		stream.heads = append(stream.heads, nil)
	}
	return stream, nil
}

type candidateStream struct {
	ctx         context.Context
	metadata    CandidateMetadata
	view        store.ReadView
	iterators   []store.Iterator
	heads       []*CandidateGroup
	queue       candidateHeap
	started     bool
	closed      bool
	maxExamined int64
}

func (s *candidateStream) Metadata() CandidateMetadata { return s.metadata }
func (s *candidateStream) Examined() int64 {
	var count int64
	for _, iterator := range s.iterators {
		count += iterator.Examined()
	}
	return count
}
func (s *candidateStream) advance(branch int) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	ref, ok, err := s.iterators[branch].Next()
	if err != nil {
		return err
	}
	if s.Examined() > s.maxExamined {
		return store.ErrWorkLimit
	}
	s.heads[branch] = nil
	if !ok {
		return nil
	}
	position, err := s.metadata.position(ref.OrderKey)
	if err != nil {
		return err
	}
	s.heads[branch] = &CandidateGroup{ID: ref.ID, Position: position, Branches: []int{branch}}
	return nil
}

type candidateHeap []CandidateGroup

func (h candidateHeap) Len() int           { return len(h) }
func (h candidateHeap) Less(i, j int) bool { return bytes.Compare(h[i].Position, h[j].Position) < 0 }
func (h candidateHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *candidateHeap) Push(value any)    { *h = append(*h, value.(CandidateGroup)) }
func (h *candidateHeap) Pop() any {
	last := len(*h) - 1
	value := (*h)[last]
	*h = (*h)[:last]
	return value
}

func (s *candidateStream) Next() (CandidateGroup, bool, error) {
	if err := s.ctx.Err(); err != nil {
		return CandidateGroup{}, false, err
	}
	if s.closed {
		return CandidateGroup{}, false, nil
	}
	if !s.started {
		s.started = true
		for i := range s.iterators {
			if err := s.advance(i); err != nil {
				return CandidateGroup{}, false, err
			}
			if s.heads[i] != nil {
				heap.Push(&s.queue, *s.heads[i])
			}
		}
	}
	if len(s.queue) == 0 {
		return CandidateGroup{}, false, nil
	}
	group := CandidateGroup{ID: s.queue[0].ID, Position: bytes.Clone(s.queue[0].Position)}
	for len(s.queue) > 0 && bytes.Equal(s.queue[0].Position, group.Position) {
		head := heap.Pop(&s.queue).(CandidateGroup)
		if head.ID != group.ID {
			return CandidateGroup{}, false, fmt.Errorf("inconsistent index document identity")
		}
		branch := head.Branches[0]
		group.Branches = append(group.Branches, branch)
		if err := s.advance(branch); err != nil {
			return CandidateGroup{}, false, err
		}
		if s.heads[branch] != nil {
			heap.Push(&s.queue, *s.heads[branch])
		}
	}
	sort.Ints(group.Branches)
	return group, true, nil
}
func (s *candidateStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	var errs []error
	for _, iterator := range s.iterators {
		if err := iterator.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if s.view != nil {
		if err := s.view.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
