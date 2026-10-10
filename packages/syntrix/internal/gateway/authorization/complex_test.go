package authorization

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codetreker/syntrix/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestEngine_ComplexScenarios(t *testing.T) {
	// Define a complex ruleset covering various scenarios
	complexRules := `
database: default
rules_version: '1'
service: syntrix
match:
  /databases/{database}/documents:
    match:
      /users/{userId}:
        allow:
          read: "request.auth.userId == userId"
          write: "request.auth.userId == userId"

        match:
          /private_info/{docId}:
            allow:
              read: "request.auth.userId == userId"
              write: "request.auth.userId == userId"

      /posts/{postId}:
        allow:
          read: "true"
          create: "request.resource.data.title.size() > 0 && request.resource.data.title.size() < 100 && request.auth.userId != null"
          update: "request.auth.userId == resource.data.authorId || (request.auth.roles != null && 'admin' in request.auth.roles)"
          delete: "request.auth.roles != null && 'admin' in request.auth.roles"

      /secrets/{secretId}:
        allow:
          read: "get('/databases/$(database)/documents/users/' + request.auth.userId).data.role == 'admin'"

      /time_limited/{docId}:
        allow:
          read: "request.time < resource.data.expires_at"

      /complex_logic/{docId}:
        allow:
          write: "(request.auth.userId == resource.data.owner || (request.auth.roles != null && 'editor' in request.auth.roles)) && request.resource.data.status == 'draft'"
`

	tmpDir := t.TempDir()
	err := os.WriteFile(filepath.Join(tmpDir, "default.yml"), []byte(complexRules), 0644)
	require.NoError(t, err)

	type testCase struct {
		name        string
		path        string
		action      string
		req         Request
		existingRes *Resource
		mockSetup   func(m *MockQueryService)
		expected    bool
		expectErr   bool
	}

	tests := []testCase{
		{
			name:   "Nested Path - User Access Own Private Info",
			path:   "/users/user1/private_info/profile",
			action: "read",
			req: Request{
				Auth: Authenticated{UID: "user1"},
			},
			expected: true,
		},
		{
			name:   "Nested Path - Other User Denied Private Info",
			path:   "/users/user1/private_info/profile",
			action: "read",
			req: Request{
				Auth: Authenticated{UID: "user2"},
			},
			expected: false,
		},
		{
			name:   "Create Post - Valid Data",
			path:   "/posts/post1",
			action: "create",
			req: Request{
				Auth: Authenticated{UID: "user1"},
				Resource: &Resource{
					Data: map[string]interface{}{
						"title": "My First Post",
					},
				},
			},
			expected: true,
		},
		{
			name:   "Create Post - Invalid Data (Title too short)",
			path:   "/posts/post1",
			action: "create",
			req: Request{
				Auth: Authenticated{UID: "user1"},
				Resource: &Resource{
					Data: map[string]interface{}{
						"title": "",
					},
				},
			},
			expected: false,
		},
		{
			name:   "Create Post - Unauthenticated",
			path:   "/posts/post1",
			action: "create",
			req: Request{
				Auth: Authenticated{UID: nil}, // Unauthenticated
				Resource: &Resource{
					Data: map[string]interface{}{
						"title": "Valid Title",
					},
				},
			},
			expected: false,
		},
		{
			name:   "Update Post - Author Allowed",
			path:   "/posts/post1",
			action: "update",
			req: Request{
				Auth: Authenticated{UID: "author1"},
			},
			existingRes: &Resource{
				Data: map[string]interface{}{
					"authorId": "author1",
				},
			},
			expected: true,
		},
		{
			name:   "Update Post - Non-Author Denied",
			path:   "/posts/post1",
			action: "update",
			req: Request{
				Auth: Authenticated{UID: "other"},
			},
			existingRes: &Resource{
				Data: map[string]interface{}{
					"authorId": "author1",
				},
			},
			expected: false,
		},
		{
			name:   "Update Post - Admin Allowed",
			path:   "/posts/post1",
			action: "update",
			req: Request{
				Auth: Authenticated{
					UID:   "admin1",
					Roles: []string{"admin"},
				},
			},
			existingRes: &Resource{
				Data: map[string]interface{}{
					"authorId": "author1",
				},
			},
			expected: true,
		},
		{
			name:   "Get Function - Admin Role Check via DB Lookup",
			path:   "/secrets/s1",
			action: "read",
			req: Request{
				Auth: Authenticated{UID: "user1"},
			},
			mockSetup: func(m *MockQueryService) {
				m.On("GetDocument", mock.Anything, "default", "users/user1").Return(model.Document{
					"id":         "user1",
					"collection": "users",
					"role":       "admin",
					"version":    int64(1),
				}, nil)
			},
			expected: true,
		},
		{
			name:   "Get Function - Non-Admin Role Check via DB Lookup",
			path:   "/secrets/s1",
			action: "read",
			req: Request{
				Auth: Authenticated{UID: "user2"},
			},
			mockSetup: func(m *MockQueryService) {
				m.On("GetDocument", mock.Anything, "default", "users/user2").Return(model.Document{
					"id":         "user2",
					"collection": "users",
					"role":       "user",
					"version":    int64(1),
				}, nil)
			},
			expected: false,
		},
		{
			name:   "Time Based Access - Allowed",
			path:   "/time_limited/doc1",
			action: "read",
			req: Request{
				Time: time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC),
			},
			existingRes: &Resource{
				Data: map[string]interface{}{
					"expires_at": time.Date(2023, 1, 2, 0, 0, 0, 0, time.UTC),
				},
			},
			expected: true,
		},
		{
			name:   "Time Based Access - Expired",
			path:   "/time_limited/doc1",
			action: "read",
			req: Request{
				Time: time.Date(2023, 1, 3, 0, 0, 0, 0, time.UTC),
			},
			existingRes: &Resource{
				Data: map[string]interface{}{
					"expires_at": time.Date(2023, 1, 2, 0, 0, 0, 0, time.UTC),
				},
			},
			expected: false,
		},
		{
			name:   "Complex Logic - Owner and Draft Status",
			path:   "/complex_logic/doc1",
			action: "write",
			req: Request{
				Auth: Authenticated{UID: "owner1"},
				Resource: &Resource{
					Data: map[string]interface{}{
						"status": "draft",
					},
				},
			},
			existingRes: &Resource{
				Data: map[string]interface{}{
					"owner": "owner1",
				},
			},
			expected: true,
		},
		{
			name:   "Complex Logic - Owner but Not Draft",
			path:   "/complex_logic/doc1",
			action: "write",
			req: Request{
				Auth: Authenticated{UID: "owner1"},
				Resource: &Resource{
					Data: map[string]interface{}{
						"status": "published",
					},
				},
			},
			existingRes: &Resource{
				Data: map[string]interface{}{
					"owner": "owner1",
				},
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mockQuery := new(MockQueryService)
			if tc.mockSetup != nil {
				tc.mockSetup(mockQuery)
			}

			engine, err := NewEngine(Config{RulesPath: tmpDir}, mockQuery)
			assert.NoError(t, err)

			allowed, err := engine.Evaluate(context.Background(), "default", tc.path, tc.action, tc.req, tc.existingRes)
			if tc.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expected, allowed)
			}

			mockQuery.AssertExpectations(t)
		})
	}
}
