package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/pkg/model"
)

func TestEngine_GetDocument_TableDriven(t *testing.T) {
	type testCase struct {
		name        string
		path        string
		mockSetup   func(*MockStorageBackend)
		expectedDoc model.Document
		expectError bool
	}

	tests := []testCase{
		{
			name: "Success",
			path: "test/1",
			mockSetup: func(m *MockStorageBackend) {
				storedDoc := &storage.StoredDoc{
					Fullpath:   "test/1",
					Collection: "test",
					Data:       map[string]interface{}{"foo": "bar"},
					Version:    2,
					UpdatedAt:  5,
					CreatedAt:  4,
				}
				m.On("Get", mock.Anything, "default", "test/1").Return(storedDoc, nil)
			},
			expectedDoc: model.Document{
				"foo":        "bar",
				"id":         "1",
				"collection": "test",
				"version":    int64(2),
				"updatedAt":  int64(5),
				"createdAt":  int64(4),
			},
			expectError: false,
		},
		{
			name: "Not Found",
			path: "test/missing",
			mockSetup: func(m *MockStorageBackend) {
				m.On("Get", mock.Anything, "default", "test/missing").Return(nil, model.ErrNotFound)
			},
			expectError: true,
		},
		{
			name: "Storage Error",
			path: "test/error",
			mockSetup: func(m *MockStorageBackend) {
				m.On("Get", mock.Anything, "default", "test/error").Return(nil, assert.AnError)
			},
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mockStorage := new(MockStorageBackend)
			if tc.mockSetup != nil {
				tc.mockSetup(mockStorage)
			}
			engine := newTestEngine(mockStorage)
			ctx := context.Background()

			doc, err := engine.GetDocument(ctx, "default", tc.path)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedDoc, doc)
			}
			mockStorage.AssertExpectations(t)
		})
	}
}

func TestEngine_CreateDocument_TableDriven(t *testing.T) {
	type testCase struct {
		name        string
		doc         model.Document
		mockSetup   func(*MockStorageBackend)
		expectError bool
	}

	tests := []testCase{
		{
			name: "Success",
			doc: model.Document{
				"id":         "1",
				"collection": "test",
				"foo":        "bar",
				"version":    int64(99),   // Should be stripped
				"updatedAt":  int64(1234), // Should be stripped
				"createdAt":  int64(5678), // Should be stripped
			},
			mockSetup: func(m *MockStorageBackend) {
				m.On("Create", mock.Anything, "default", mock.MatchedBy(func(d storage.StoredDoc) bool {
					_, hasVersion := d.Data["version"]
					_, hasUpdated := d.Data["updatedAt"]
					_, hasCreated := d.Data["createdAt"]
					_, hasCollection := d.Data["collection"]

					return d.Fullpath == "test/1" &&
						d.Collection == "test" &&
						d.Data["foo"] == "bar" &&
						!hasVersion && !hasUpdated && !hasCreated && !hasCollection
				})).Return(nil)
			},
			expectError: false,
		},
		{
			name: "Storage Error",
			doc:  model.Document{"id": "1", "collection": "test", "foo": "bar"},
			mockSetup: func(m *MockStorageBackend) {
				m.On("Create", mock.Anything, "default", mock.MatchedBy(func(d storage.StoredDoc) bool {
					return d.Fullpath == "test/1"
				})).Return(assert.AnError)
			},
			expectError: true,
		},
		{
			name:        "Nil Document",
			doc:         nil,
			mockSetup:   nil,
			expectError: true,
		},
		{
			name:        "Missing Collection",
			doc:         model.Document{"id": "1"},
			mockSetup:   nil,
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mockStorage := new(MockStorageBackend)
			if tc.mockSetup != nil {
				tc.mockSetup(mockStorage)
			}
			engine := newTestEngine(mockStorage)
			ctx := context.Background()

			err := engine.CreateDocument(ctx, "default", tc.doc)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			mockStorage.AssertExpectations(t)
		})
	}
}

func TestEngine_ReplaceDocument_TableDriven(t *testing.T) {
	type testCase struct {
		name        string
		doc         model.Document
		filters     model.Filters
		mockSetup   func(*MockStorageBackend)
		expectedDoc model.Document
		expectError bool
	}

	tests := []testCase{
		{
			name: "Create (Not Found)",
			doc: model.Document{
				"id":         "1",
				"collection": "test",
				"foo":        "bar",
				"version":    int64(99),
				"updatedAt":  int64(1),
				"createdAt":  int64(2),
			},
			mockSetup: func(m *MockStorageBackend) {
				m.On("Get", mock.Anything, "default", "test/1").Return(nil, model.ErrNotFound)
				m.On("Create", mock.Anything, "default", mock.MatchedBy(func(d storage.StoredDoc) bool {
					_, hasVersion := d.Data["version"]
					_, hasUpdated := d.Data["updatedAt"]
					_, hasCreated := d.Data["createdAt"]
					_, hasCollection := d.Data["collection"]

					return d.Fullpath == "test/1" &&
						d.Collection == "test" &&
						d.Data["foo"] == "bar" &&
						!hasVersion && !hasUpdated && !hasCreated && !hasCollection
				})).Return(nil)
			},
			expectedDoc: model.Document{
				"id":         "1",
				"collection": "test",
				"foo":        "bar",
			},
			expectError: false,
		},
		{
			name: "Update (Found)",
			doc: model.Document{
				"id":         "1",
				"collection": "test",
				"foo":        "new",
				"version":    int64(99),
			},
			mockSetup: func(m *MockStorageBackend) {
				existingDoc := &storage.StoredDoc{Id: "test/1", Version: 1, Data: map[string]interface{}{"foo": "old"}}
				m.On("Get", mock.Anything, "default", "test/1").Return(existingDoc, nil).Once()

				expectedUpdate := map[string]interface{}{"foo": "new", "id": "1"}
				m.On("Update", mock.Anything, "default", "test/1", expectedUpdate, model.Filters(nil)).Return(nil)

				m.On("Get", mock.Anything, "default", "test/1").Return(&storage.StoredDoc{Id: "test/1", Fullpath: "test/1", Version: 2, Data: expectedUpdate}, nil).Once()
			},
			expectedDoc: model.Document{
				"id":      "1",
				"foo":     "new",
				"version": int64(2),
			},
			expectError: false,
		},
		{
			name: "Create Error",
			doc:  model.Document{"id": "1", "collection": "test", "foo": "bar"},
			mockSetup: func(m *MockStorageBackend) {
				m.On("Get", mock.Anything, "default", "test/1").Return(nil, model.ErrNotFound)
				m.On("Create", mock.Anything, "default", mock.Anything).Return(assert.AnError)
			},
			expectError: true,
		},
		{
			name: "Update Error",
			doc:  model.Document{"id": "1", "collection": "test", "foo": "new"},
			mockSetup: func(m *MockStorageBackend) {
				m.On("Get", mock.Anything, "default", "test/1").Return(&storage.StoredDoc{Id: "test/1", Version: 1}, nil).Once()
				m.On("Update", mock.Anything, "default", "test/1", mock.Anything, model.Filters(nil)).Return(assert.AnError)
			},
			expectError: true,
		},
		{
			name:        "Nil Document",
			doc:         nil,
			expectError: true,
		},
		{
			name:        "Missing Collection",
			doc:         model.Document{"id": "1"},
			expectError: true,
		},
		{
			name:        "Missing ID",
			doc:         model.Document{"collection": "test"},
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mockStorage := new(MockStorageBackend)
			if tc.mockSetup != nil {
				tc.mockSetup(mockStorage)
			}
			engine := newTestEngine(mockStorage)
			ctx := context.Background()

			doc, err := engine.ReplaceDocument(ctx, "default", tc.doc, tc.filters)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				// Check fields
				for k, v := range tc.expectedDoc {
					assert.Equal(t, v, doc[k])
				}
			}
			mockStorage.AssertExpectations(t)
		})
	}
}

func TestEngine_PatchDocument_TableDriven(t *testing.T) {
	type testCase struct {
		name        string
		doc         model.Document
		filters     model.Filters
		mockSetup   func(*MockStorageBackend)
		expectedDoc model.Document
		expectError bool
	}

	tests := []testCase{
		{
			name: "Success",
			doc: model.Document{
				"id":         "1",
				"collection": "test",
				"bar":        "baz",
				"version":    int64(2), // Should be stripped
			},
			mockSetup: func(m *MockStorageBackend) {
				patchFields := map[string]interface{}{"bar": "baz"}
				m.On("Patch", mock.Anything, "default", "test/1", patchFields, model.Filters(nil)).Return(nil).Once()

				expectedMergedData := map[string]interface{}{"foo": "old", "bar": "baz"}
				m.On("Get", mock.Anything, "default", "test/1").Return(&storage.StoredDoc{Id: "test/1", Fullpath: "test/1", Version: 2, Data: expectedMergedData}, nil).Once()
			},
			expectedDoc: model.Document{
				"foo": "old",
				"bar": "baz",
			},
			expectError: false,
		},
		{
			name: "Storage Error",
			doc:  model.Document{"id": "1", "collection": "test", "bar": "baz"},
			mockSetup: func(m *MockStorageBackend) {
				m.On("Patch", mock.Anything, "default", "test/1", mock.Anything, model.Filters(nil)).Return(assert.AnError).Once()
			},
			expectError: true,
		},
		{
			name:        "Nil Document",
			doc:         nil,
			expectError: true,
		},
		{
			name:        "Missing Collection",
			doc:         model.Document{"id": "1"},
			expectError: true,
		},
		{
			name:        "Missing ID",
			doc:         model.Document{"collection": "test"},
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mockStorage := new(MockStorageBackend)
			if tc.mockSetup != nil {
				tc.mockSetup(mockStorage)
			}
			engine := newTestEngine(mockStorage)
			ctx := context.Background()

			doc, err := engine.PatchDocument(ctx, "default", tc.doc, tc.filters)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				for k, v := range tc.expectedDoc {
					assert.Equal(t, v, doc[k])
				}
			}
			mockStorage.AssertExpectations(t)
		})
	}
}

func TestEngine_Push_TableDriven(t *testing.T) {
	type testCase struct {
		name              string
		req               storage.ReplicationPushRequest
		mockSetup         func(*MockStorageBackend)
		expectedConflicts []*storage.StoredDoc
		expectError       bool
	}

	tests := []testCase{
		{
			name: "Update Success",
			req: storage.ReplicationPushRequest{
				Collection: "test",
				Changes: []storage.ReplicationPushChange{
					{Doc: &storage.StoredDoc{Id: "test/1", Fullpath: "test/1", Collection: "test", Data: map[string]interface{}{"foo": "bar"}, Version: 1}},
				},
			},
			mockSetup: func(m *MockStorageBackend) {
				existingDoc := &storage.StoredDoc{Id: "test/1", Version: 1}
				m.On("Get", mock.Anything, "default", "test/1").Return(existingDoc, nil)
				m.On("Update", mock.Anything, "default", "test/1", map[string]interface{}{"foo": "bar"}, mock.Anything).Return(nil)
			},
			expectedConflicts: nil,
			expectError:       false,
		},
		{
			name: "Create Success (Not Found)",
			req: storage.ReplicationPushRequest{
				Collection: "test",
				Changes: []storage.ReplicationPushChange{
					{Doc: &storage.StoredDoc{Id: "test/2", Fullpath: "test/2", Collection: "test", Data: map[string]interface{}{"foo": "bar"}, Version: 0}},
				},
			},
			mockSetup: func(m *MockStorageBackend) {
				m.On("Get", mock.Anything, "default", "test/2").Return(nil, model.ErrNotFound)
				m.On("Create", mock.Anything, "default", mock.Anything).Return(nil)
			},
			expectedConflicts: nil,
			expectError:       false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mockStorage := new(MockStorageBackend)
			if tc.mockSetup != nil {
				tc.mockSetup(mockStorage)
			}
			engine := newTestEngine(mockStorage)
			ctx := context.Background()

			resp, err := engine.Push(ctx, "default", tc.req)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedConflicts, resp.Conflicts)
			}
			mockStorage.AssertExpectations(t)
		})
	}
}
