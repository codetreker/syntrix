package manager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/indexer/encoding"
	"github.com/syntrixbase/syntrix/internal/indexer/mem_store"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
	"github.com/syntrixbase/syntrix/pkg/model"
)

func TestCandidatesRejectInvalidOperands(t *testing.T) {
	st := mem_store.New()
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	m, _ := candidateManager(t, st, "      - {field: value, order: asc}\n")
	values := make([]any, 257)
	for i := range values {
		values[i] = i
	}
	for _, tc := range []struct {
		name   string
		filter Filter
		want   error
	}{
		{"unknown-operator", Filter{Field: "value", Op: "unknown", Value: 1}, ErrInvalidPlan},
		{"invalid-in-shape", Filter{Field: "value", Op: FilterIn, Value: 1}, ErrInvalidPlan},
		{"in-operand-budget", Filter{Field: "value", Op: FilterIn, Value: values}, ErrInvalidPlan},
		{"encoded-string-budget", Filter{Field: "value", Op: FilterEq, Value: strings.Repeat("x", 65536)}, encoding.ErrStringTooLong},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := m.OpenCandidates(context.Background(), "db", Plan{Collection: "items", Filters: []Filter{tc.filter}})
			require.ErrorIs(t, err, tc.want)
		})
	}
}

func TestCandidatePointIntervals(t *testing.T) {
	for _, direction := range []encoding.Direction{encoding.Asc, encoding.Desc} {
		t.Run(fmt.Sprint(direction), func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				filter model.Filter
				want   []string
			}{
				{"equality", model.Filter{Op: model.OpEq, Value: "b"}, []string{"b"}},
				{"union", model.Filter{Op: model.OpIn, Value: []any{"a", "c"}}, []string{"a", "c"}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					intervals, err := filterIntervals([]byte{encoding.Version}, direction, tc.filter)
					require.NoError(t, err)
					var admitted []string
					for _, value := range []string{"", "a", "aa", "b", "c", "cc"} {
						key, err := encoding.Encode([]encoding.Field{{Value: value, Direction: direction}}, "document")
						require.NoError(t, err)
						for _, interval := range intervals {
							if bytes.Compare(key, interval.lower) >= 0 && (interval.upper == nil || bytes.Compare(key, interval.upper) < 0) {
								admitted = append(admitted, value)
							}
						}
					}
					require.Equal(t, tc.want, admitted)
				})
			}
		})
	}
	t.Run("unencodable-union-member", func(t *testing.T) {
		_, err := filterIntervals([]byte{encoding.Version}, encoding.Asc, model.Filter{Op: model.OpIn, Value: []any{"valid", strings.Repeat("x", 65536)}})
		require.ErrorIs(t, err, encoding.ErrStringTooLong)
	})
	t.Run("unencodable-bound", func(t *testing.T) {
		_, err := filterIntervals([]byte{encoding.Version}, encoding.Asc, model.Filter{Op: model.OpGt, Value: strings.Repeat("x", 65536)})
		require.ErrorIs(t, err, encoding.ErrStringTooLong)
	})
}

func TestCandidatesDescendingIDProofAndResume(t *testing.T) {
	candidateStores(t, func(t *testing.T, st store.Store) {
		m, ref := candidateManager(t, st, "      - {field: id, order: desc}\n")
		postings := make(map[string][]byte)
		for _, id := range []string{"a", "aa", "z"} {
			postings[id] = candidatePosting(t, st, ref, id, encoding.Field{Value: id, Direction: encoding.Desc})
		}
		require.NoError(t, st.PublishGeneration(ref, "ready"))
		plan := Plan{Collection: "items", OrderBy: []OrderField{{Field: "id", Direction: encoding.Desc}}}
		stream, err := m.OpenCandidates(context.Background(), "db", plan)
		require.NoError(t, err)
		metadata := stream.Metadata()
		groups := collectCandidates(t, stream)
		require.NoError(t, stream.Close())
		require.Equal(t, []string{"z", "aa", "a"}, candidateIDs(groups))
		for i, group := range groups {
			key, err := metadata.PostingKey(group, 0)
			require.NoError(t, err)
			require.Equal(t, postings[group.ID], key)
			plan.AfterPosition = group.Position
			resumed, err := m.OpenCandidates(context.Background(), "db", plan)
			require.NoError(t, err)
			require.Equal(t, candidateIDs(groups[i+1:]), candidateIDs(collectCandidates(t, resumed)))
			require.NoError(t, resumed.Close())
		}
	})
}

func TestCandidatesRejectCorruptPostingShape(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  []byte
	}{
		{"unterminated-component", []byte{encoding.Version, encoding.TypeString, 'a'}},
		{"wrong-field-count", []byte{encoding.Version, encoding.TypeString, 'a', 0, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidateStores(t, func(t *testing.T, st store.Store) {
				m, ref := candidateManager(t, st, "      - {field: value, order: asc}\n")
				require.NoError(t, st.ApplyDocumentProjection([]store.Projection{{Index: ref, DocumentID: "a", PostingKeys: [][]byte{tc.key}}}, ""))
				require.NoError(t, st.PublishGeneration(ref, "ready"))
				stream, err := m.OpenCandidates(context.Background(), "db", Plan{Collection: "items", OrderBy: []OrderField{{Field: "value", Direction: encoding.Asc}}})
				require.NoError(t, err)
				defer stream.Close()
				_, _, err = stream.Next()
				require.ErrorIs(t, err, encoding.ErrInvalidOrderKey)
			})
		})
	}
}

type candidateFailingCloseIterator struct {
	store.Iterator
	failure error
	closed  func()
}

func (i *candidateFailingCloseIterator) Close() error {
	i.closed()
	return errors.Join(i.Iterator.Close(), i.failure)
}

type candidateFailingCloseView struct {
	store.ReadView
	failure error
	closed  func()
}

func (v *candidateFailingCloseView) Close() error {
	v.closed()
	return errors.Join(v.ReadView.Close(), v.failure)
}

func TestCandidatesCloseReleasesAllResourcesOnFailure(t *testing.T) {
	candidateStores(t, func(t *testing.T, st store.Store) {
		m, ref := candidateManager(t, st, "      - {field: value, order: asc}\n")
		require.NoError(t, st.PublishGeneration(ref, "ready"))
		stream, err := m.OpenCandidates(context.Background(), "db", Plan{Collection: "items", Filters: []Filter{{Field: "value", Op: FilterIn, Value: []any{"a", "b"}}}})
		require.NoError(t, err)
		internal := stream.(*candidateStream)
		var closed []string
		iteratorFailure, viewFailure := errors.New("iterator close failed"), errors.New("view close failed")
		for i, iterator := range internal.iterators {
			internal.iterators[i] = &candidateFailingCloseIterator{Iterator: iterator, failure: iteratorFailure, closed: func() { closed = append(closed, fmt.Sprintf("iterator-%d", i)) }}
		}
		internal.view = &candidateFailingCloseView{ReadView: internal.view, failure: viewFailure, closed: func() { closed = append(closed, "view") }}
		err = stream.Close()
		require.ErrorIs(t, err, iteratorFailure)
		require.ErrorIs(t, err, viewFailure)
		require.NoError(t, stream.Close())
		require.Equal(t, []string{"iterator-0", "iterator-1", "view"}, closed)
		_, ok, err := stream.Next()
		require.NoError(t, err)
		require.False(t, ok)
	})
}
