package postgres

import "github.com/codetreker/syntrix/internal/core/storage/postgres/schema"

func SchemaSpecification() schema.Specification {
	return schema.Specification{Table: "auth_users", Columns: []schema.Column{
		{Name: "id", Type: "character varying(64)"},
		{Name: "username", Type: "character varying(255)"},
		{Name: "password_hash", Type: "text"},
		{Name: "password_algo", Type: "character varying(32)", Default: "'argon2id'::character varying"},
		{Name: "created_at", Type: "timestamp with time zone", Default: "now()"},
		{Name: "updated_at", Type: "timestamp with time zone", Default: "now()"},
		{Name: "disabled", Type: "boolean", Default: "false"},
		{Name: "roles", Type: "text[]", Default: "'{}'::text[]"},
		{Name: "db_admin", Type: "text[]", Default: "'{}'::text[]"},
		{Name: "profile", Type: "jsonb", Default: "'{}'::jsonb"},
		{Name: "last_login_at", Type: "timestamp with time zone", Nullable: true},
		{Name: "login_attempts", Type: "integer", Default: "0"},
		{Name: "lockout_until", Type: "timestamp with time zone", Nullable: true},
	}, Indexes: []schema.Index{
		{Name: "auth_users_pkey", Keys: []string{"id"}, Unique: true},
		{Name: "idx_auth_users_username", Keys: []string{"username"}, Unique: true},
		{Name: "idx_auth_users_disabled", Keys: []string{"disabled"}, Predicate: "(disabled = true)"},
	}, Constraints: []schema.Constraint{{Name: "auth_users_pkey", Definition: "PRIMARY KEY (id)"}}}
}
