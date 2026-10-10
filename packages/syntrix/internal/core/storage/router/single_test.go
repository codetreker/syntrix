package router

import (
	"context"
	"testing"

	"github.com/codetreker/syntrix/internal/core/storage/types"
	"github.com/codetreker/syntrix/pkg/model"
	"github.com/stretchr/testify/assert"
)

type fakeDocumentStore struct{}

func (f *fakeDocumentStore) Get(ctx context.Context, database string, path string, opts ...types.ReadOptions) (*types.StoredDoc, error) {
	return nil, nil
}
func (f *fakeDocumentStore) Create(ctx context.Context, database string, doc types.StoredDoc) error {
	return nil
}
func (f *fakeDocumentStore) Update(ctx context.Context, database string, path string, data map[string]interface{}, pred model.Filters) error {
	return nil
}
func (f *fakeDocumentStore) Patch(ctx context.Context, database string, path string, data map[string]interface{}, pred model.Filters) error {
	return nil
}
func (f *fakeDocumentStore) Delete(ctx context.Context, database string, path string, pred model.Filters) error {
	return nil
}
func (f *fakeDocumentStore) DeleteByDatabase(ctx context.Context, database string, limit int) (int, error) {
	return 0, nil
}
func (f *fakeDocumentStore) Query(ctx context.Context, database string, q model.Query) ([]*types.StoredDoc, error) {
	return nil, nil
}
func (f *fakeDocumentStore) GetMany(ctx context.Context, database string, paths []string, opts ...types.ReadOptions) ([]*types.StoredDoc, error) {
	return nil, nil
}
func (f *fakeDocumentStore) Watch(ctx context.Context, database string, collection string, after types.WatchCheckpoint, opts types.WatchOptions) (types.WatchStream, error) {
	return nil, nil
}
func (f *fakeDocumentStore) Close(ctx context.Context) error { return nil }

// Ensure indexes isn't part of DocumentStore; we call it on concrete implementations during provider init.

func TestSingleSplitRouters(t *testing.T) {
	doc := &fakeDocumentStore{}
	database := "default"

	rd := NewSingleDocumentRouter(doc)
	d, err := rd.Select(database, types.OpRead)
	assert.NoError(t, err)
	assert.Equal(t, doc, d)

}
