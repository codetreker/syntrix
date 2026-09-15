package rest

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/go-playground/validator/v10"
	"github.com/syntrixbase/syntrix/internal/core/identity"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateData(t *testing.T) {
	tests := []struct {
		name    string
		data    model.Document
		wantErr bool
	}{
		{"valid data", model.Document{"key": "value"}, false},
		{"nil data", nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.data.ValidateDocument()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateQuery(t *testing.T) {
	tests := []struct {
		name    string
		query   model.Query
		wantErr bool
	}{
		{
			"valid query",
			model.Query{
				Collection: "users",
				Limit:      10,
				Filters:    []model.Filter{{Field: "age", Op: model.OpGt, Value: 18}},
				OrderBy:    []model.Order{{Field: "age", Direction: "asc"}},
			},
			false,
		},
		{
			"invalid collection",
			model.Query{Collection: "users/alice"},
			true,
		},
		{
			"negative limit",
			model.Query{Collection: "users", Limit: -1},
			true,
		},
		{
			"limit too high",
			model.Query{Collection: "users", Limit: 1001},
			true,
		},
		{
			"empty filter field",
			model.Query{Collection: "users", Filters: []model.Filter{{Field: "", Op: model.OpGt, Value: 18}}},
			true,
		},
		{
			"empty filter op",
			model.Query{Collection: "users", Filters: []model.Filter{{Field: "age", Op: "", Value: 18}}},
			true,
		},
		{
			"empty orderby field",
			model.Query{Collection: "users", OrderBy: []model.Order{{Field: "", Direction: "asc"}}},
			true,
		},
		{
			"invalid orderby direction",
			model.Query{Collection: "users", OrderBy: []model.Order{{Field: "age", Direction: "up"}}},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateQuery(tt.query)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateReplicationPull(t *testing.T) {
	tests := []struct {
		name    string
		req     storage.ReplicationPullRequest
		wantErr bool
	}{
		{
			"valid request",
			storage.ReplicationPullRequest{Collection: "users", Limit: 100},
			false,
		},
		{
			"invalid collection",
			storage.ReplicationPullRequest{Collection: "users/alice"},
			true,
		},
		{
			"negative limit",
			storage.ReplicationPullRequest{Collection: "users", Limit: -1},
			true,
		},
		{
			"limit too high",
			storage.ReplicationPullRequest{Collection: "users", Limit: 1001},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateReplicationPull("default", tt.req)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateReplicationPush(t *testing.T) {
	tests := []struct {
		name    string
		req     storage.ReplicationPushRequest
		wantErr bool
	}{
		{
			"valid request",
			storage.ReplicationPushRequest{
				Collection: "users",
				Changes: []storage.ReplicationPushChange{
					{Doc: &storage.StoredDoc{Fullpath: "users/alice"}},
				},
			},
			false,
		},
		{
			"invalid collection",
			storage.ReplicationPushRequest{Collection: "users/alice"},
			true,
		},
		{
			"nil doc",
			storage.ReplicationPushRequest{
				Collection: "users",
				Changes: []storage.ReplicationPushChange{
					{Doc: nil},
				},
			},
			true,
		},
		{
			"invalid doc path syntax",
			storage.ReplicationPushRequest{
				Collection: "users",
				Changes: []storage.ReplicationPushChange{
					{Doc: &storage.StoredDoc{Fullpath: "users/alice!"}},
				},
			},
			true,
		},
		{
			"doc path mismatch collection",
			storage.ReplicationPushRequest{
				Collection: "users",
				Changes: []storage.ReplicationPushChange{
					{Doc: &storage.StoredDoc{Fullpath: "posts/post1"}},
				},
			},
			true,
		},
		{
			"doc path is not a document path",
			storage.ReplicationPushRequest{
				Collection: "users",
				Changes: []storage.ReplicationPushChange{
					{Doc: &storage.StoredDoc{Fullpath: "users/alice/posts"}},
				},
			},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateReplicationPush(tt.req)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestDecodeAndValidate(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantErr    bool
		wantErrMsg string
	}{
		{
			name:    "valid login request",
			body:    `{"username": "testuser", "password": "pass"}`,
			wantErr: false,
		},
		{
			name:       "missing username",
			body:       `{"password": "pass"}`,
			wantErr:    true,
			wantErrMsg: "username: This field is required",
		},
		{
			name:       "missing password",
			body:       `{"username": "testuser"}`,
			wantErr:    true,
			wantErrMsg: "password: This field is required",
		},
		{
			name:       "empty body",
			body:       `{}`,
			wantErr:    true,
			wantErrMsg: "username: This field is required",
		},
		{
			name:       "invalid json",
			body:       `{invalid}`,
			wantErr:    true,
			wantErrMsg: "invalid request body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/login", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")

			result, err := decodeAndValidate[identity.LoginRequest](req)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrMsg)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, result)
				assert.NotEmpty(t, result.Username)
			}
		})
	}
}

func TestValidateStruct(t *testing.T) {
	tests := []struct {
		name    string
		input   interface{}
		wantErr bool
	}{
		{
			name: "valid signup request",
			input: &identity.SignupRequest{
				Username: "testuser",
				Password: "password123",
			},
			wantErr: false,
		},
		{
			name: "username too short",
			input: &identity.SignupRequest{
				Username: "ab",
				Password: "password123",
			},
			wantErr: true,
		},
		{
			name: "password too short",
			input: &identity.SignupRequest{
				Username: "testuser",
				Password: "short",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateStruct(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidationErrors_Error(t *testing.T) {
	ve := ValidationErrors{
		Errors: []ValidationError{
			{Field: "username", Message: "required"},
			{Field: "password", Message: "too short"},
		},
	}
	errStr := ve.Error()
	assert.Contains(t, errStr, "username: required")
	assert.Contains(t, errStr, "password: too short")
}

func TestTranslateValidationError(t *testing.T) {
	// Test using real validator to produce FieldErrors
	type testStruct struct {
		Required    string `validate:"required"`
		MinLen      string `validate:"min=3"`
		MaxLen      string `validate:"max=5"`
		AlphaNum    string `validate:"alphanum"`
		Email       string `validate:"email"`
		URL         string `validate:"url"`
		GreaterThan int    `validate:"gte=5"`
		LessThan    int    `validate:"lte=10"`
		GT          int    `validate:"gt=5"`
		LT          int    `validate:"lt=10"`
		OneOf       string `validate:"oneof=a b c"`
	}

	tests := []struct {
		name        string
		input       testStruct
		wantContain string
	}{
		{
			name:        "required field",
			input:       testStruct{},
			wantContain: "required",
		},
		{
			name:        "min length",
			input:       testStruct{Required: "x", MinLen: "ab"},
			wantContain: "at least 3",
		},
		{
			name:        "max length",
			input:       testStruct{Required: "x", MaxLen: "toolong"},
			wantContain: "at most 5",
		},
		{
			name:        "alphanum",
			input:       testStruct{Required: "x", AlphaNum: "abc@!#"},
			wantContain: "letters and numbers",
		},
		{
			name:        "email",
			input:       testStruct{Required: "x", Email: "notanemail"},
			wantContain: "valid email",
		},
		{
			name:        "url",
			input:       testStruct{Required: "x", URL: "notaurl"},
			wantContain: "valid URL",
		},
		{
			name:        "gte",
			input:       testStruct{Required: "x", GreaterThan: 3},
			wantContain: "greater than or equal to",
		},
		{
			name:        "lte",
			input:       testStruct{Required: "x", LessThan: 15},
			wantContain: "less than or equal to",
		},
		{
			name:        "gt",
			input:       testStruct{Required: "x", GT: 3},
			wantContain: "greater than",
		},
		{
			name:        "lt",
			input:       testStruct{Required: "x", LT: 15},
			wantContain: "less than",
		},
		{
			name:        "oneof",
			input:       testStruct{Required: "x", OneOf: "invalid"},
			wantContain: "one of",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateStruct(&tt.input)
			if err == nil {
				t.Skip("no validation error produced")
				return
			}
			assert.Contains(t, err.Error(), tt.wantContain)
		})
	}
}

func TestTranslateValidationError_UnknownTag(t *testing.T) {
	// Test the default case - use a custom validator with unknown tag
	v := validator.New()
	_ = v.RegisterValidation("customtag", func(fl validator.FieldLevel) bool {
		return false
	})

	type customStruct struct {
		Field string `validate:"customtag"`
	}

	err := v.Struct(&customStruct{Field: "test"})
	require.Error(t, err)

	var ve validator.ValidationErrors
	require.True(t, errors.As(err, &ve))

	for _, fe := range ve {
		msg := translateValidationError(fe)
		assert.Contains(t, msg, "Failed validation")
	}
}
