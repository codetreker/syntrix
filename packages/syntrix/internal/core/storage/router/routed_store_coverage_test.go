package router

import (
	"context"
	"errors"
	"testing"

	"github.com/codetreker/syntrix/internal/core/storage/types"
	"github.com/codetreker/syntrix/pkg/model"
	"github.com/stretchr/testify/assert"
)

func TestRoutedDocumentStore_Coverage(t *testing.T) {
	ctx := context.Background()
	database := "default"
	errSelect := errors.New("select error")

	t.Run("Get Select Error", func(t *testing.T) {
		router := new(mockDocRouter)
		router.On("Select", database, types.OpRead).Return(nil, errSelect)

		rs := NewRoutedDocumentStore(router)
		_, err := rs.Get(ctx, database, "path")

		assert.ErrorIs(t, err, errSelect)
	})

	t.Run("Create Select Error", func(t *testing.T) {
		router := new(mockDocRouter)
		router.On("Select", database, types.OpWrite).Return(nil, errSelect)

		rs := NewRoutedDocumentStore(router)
		err := rs.Create(ctx, database, types.StoredDoc{})

		assert.ErrorIs(t, err, errSelect)
	})

	t.Run("Update Select Error", func(t *testing.T) {
		router := new(mockDocRouter)
		router.On("Select", database, types.OpWrite).Return(nil, errSelect)

		rs := NewRoutedDocumentStore(router)
		err := rs.Update(ctx, database, "path", nil, nil)

		assert.ErrorIs(t, err, errSelect)
	})

	t.Run("Patch Select Error", func(t *testing.T) {
		router := new(mockDocRouter)
		router.On("Select", database, types.OpWrite).Return(nil, errSelect)

		rs := NewRoutedDocumentStore(router)
		err := rs.Patch(ctx, database, "path", nil, nil)

		assert.ErrorIs(t, err, errSelect)
	})

	t.Run("Delete Select Error", func(t *testing.T) {
		router := new(mockDocRouter)
		router.On("Select", database, types.OpWrite).Return(nil, errSelect)

		rs := NewRoutedDocumentStore(router)
		err := rs.Delete(ctx, database, "path", nil)

		assert.ErrorIs(t, err, errSelect)
	})

	t.Run("Query Select Error", func(t *testing.T) {
		router := new(mockDocRouter)
		router.On("Select", database, types.OpRead).Return(nil, errSelect)

		rs := NewRoutedDocumentStore(router)
		_, err := rs.Query(ctx, database, model.Query{})

		assert.ErrorIs(t, err, errSelect)
	})

	t.Run("Watch Select Error", func(t *testing.T) {
		router := new(mockDocRouter)
		router.On("Select", database, types.OpWatch).Return(nil, errSelect).Once()

		rs := NewRoutedDocumentStore(router)
		stream, err := rs.Watch(ctx, database, "coll", "", types.WatchOptions{})

		assert.Nil(t, stream)
		assert.Same(t, errSelect, err)
		assert.ErrorIs(t, err, errSelect)
		router.AssertExpectations(t)
	})

	t.Run("GetMany Select Error", func(t *testing.T) {
		router := new(mockDocRouter)
		router.On("Select", database, types.OpRead).Return(nil, errSelect)

		rs := NewRoutedDocumentStore(router)
		_, err := rs.GetMany(ctx, database, []string{"path1", "path2"})

		assert.ErrorIs(t, err, errSelect)
	})
}
