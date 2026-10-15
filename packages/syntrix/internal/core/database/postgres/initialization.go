package postgres

import "github.com/codetreker/syntrix/internal/core/storage/postgres/schema"

func SchemaSpecification() schema.Specification {
	return schema.Specification{Table: "databases", Columns: []schema.Column{
		{Name: "id", Type: "character varying(16)"},
		{Name: "slug", Type: "character varying(63)", Nullable: true},
		{Name: "display_name", Type: "character varying(255)"},
		{Name: "description", Type: "text", Nullable: true},
		{Name: "owner_id", Type: "character varying(64)"},
		{Name: "created_at", Type: "timestamp with time zone", Default: "now()"},
		{Name: "updated_at", Type: "timestamp with time zone", Default: "now()"},
		{Name: "max_documents", Type: "bigint", Default: "0"},
		{Name: "max_storage_bytes", Type: "bigint", Default: "0"},
		{Name: "status", Type: "character varying(20)", Default: "'active'::character varying"},
	}, Indexes: []schema.Index{
		{Name: "databases_pkey", Keys: []string{"id"}, Unique: true},
		{Name: "databases_slug_key", Keys: []string{"slug"}, Unique: true},
		{Name: "idx_databases_owner", Keys: []string{"owner_id"}},
		{Name: "idx_databases_status", Keys: []string{"status"}},
		{Name: "idx_databases_created_at", Keys: []string{"created_at"}, Descending: []bool{true}},
		{Name: "idx_databases_owner_status", Keys: []string{"owner_id", "status"}},
		{Name: "idx_databases_slug", Keys: []string{"slug"}, Predicate: "(slug IS NOT NULL)"},
	}, Constraints: []schema.Constraint{
		{Name: "databases_pkey", Definition: "PRIMARY KEY (id)"},
		{Name: "databases_slug_key", Definition: "UNIQUE (slug)"},
		{Name: "chk_databases_id", Definition: "CHECK (((id)::text ~ '^[0-9a-f]{16}$'::text))"},
		{Name: "chk_databases_slug", Definition: "CHECK (((slug IS NULL) OR ((slug)::text ~ '^[a-z][a-z0-9-]{2,62}$'::text)))"},
		{Name: "chk_databases_status", Definition: "CHECK (((status)::text = ANY ((ARRAY['active'::character varying, 'suspended'::character varying, 'deleting'::character varying])::text[])))"},
		{Name: "chk_databases_max_documents", Definition: "CHECK ((max_documents >= 0))"},
		{Name: "chk_databases_max_storage", Definition: "CHECK ((max_storage_bytes >= 0))"},
	}}
}
