package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostgresEndpointIdentity(t *testing.T) {
	t.Setenv("PGHOST", "localhost")
	t.Setenv("PGPORT", "5432")
	baseline, err := PostgresEndpointIdentity("postgres://user:password@HOST.example:5432/private?sslmode=disable")
	require.NoError(t, err)
	for _, dsn := range []string{
		"postgresql://different:rotated@host.example:5432/private?sslmode=require",
		"host=host.example port=5432 user=user password=secret dbname=private",
		"host = 'host.example' port = '5432' password = 'host=foreign port=1 \\'quoted\\'' user = changed",
		"host=host.example port=5432 password=host\\=foreign\\ port\\=1",
	} {
		identity, err := PostgresEndpointIdentity(dsn)
		require.NoError(t, err)
		assert.Equal(t, baseline, identity)
	}
	for _, dsn := range []string{"host=other.example port=5432", "host=host.example port=6432", "host=host.example port=5432 hostaddr=127.0.0.2", "host=host.example port=5432 service=other"} {
		identity, err := PostgresEndpointIdentity(dsn)
		require.NoError(t, err)
		assert.NotEqual(t, baseline, identity)
	}
	a, err := PostgresEndpointIdentity("host='/tmp/Socket' port=5432")
	require.NoError(t, err)
	b, err := PostgresEndpointIdentity("host='/tmp/socket' port=5432")
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
	for _, dsn := range []string{"host", "host='unterminated", "host=ends\\", "=value", "postgres://user@invalid:port"} {
		_, err := PostgresEndpointIdentity(dsn)
		assert.Error(t, err)
	}
	t.Setenv("PGHOST", "")
	t.Setenv("PGPORT", "")
	implicit, err := PostgresEndpointIdentity("user=someone password=")
	require.NoError(t, err)
	explicit, err := PostgresEndpointIdentity("host=localhost port=5432")
	require.NoError(t, err)
	assert.Equal(t, explicit, implicit)
}
