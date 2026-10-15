package origin

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPolicyExactAuthorityAndNativeClients(t *testing.T) {
	p := Policy{AllowedOrigins: []string{"https://app.example:8443"}}
	cases := []struct {
		value, host                 string
		tls, requireOrigin, allowed bool
	}{
		{"", "api.example", false, false, true},
		{"", "api.example", false, true, false},
		{"http://api.example", "api.example:80", false, false, true},
		{"https://api.example", "api.example:443", true, false, true},
		{"https://API.example:00443", "api.example", true, false, true},
		{"http://api.example", "api.example", true, false, false},
		{"http://api.example:3000", "api.example:8080", false, false, false},
		{"https://app.example:8443", "api.example", false, false, true},
		{"https://app.example:443", "api.example", false, false, false},
		{"http://[::1]:8080", "[::1]:8080", false, false, true},
		{"null", "api.example", false, false, false},
		{"http://api.example/", "api.example", false, false, false},
		{"http://user:pass@api.example", "api.example", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.value+tc.host, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://api.example/", nil)
			r.Host = tc.host
			if tc.tls {
				r.TLS = &tls.ConnectionState{}
			}
			if tc.value != "" {
				r.Header.Set("Origin", tc.value)
			}
			require.Equal(t, tc.allowed, p.Check(r, tc.requireOrigin) == nil)
		})
	}
	r := httptest.NewRequest("GET", "http://api.example/", nil)
	r.Header.Add("Origin", "http://api.example")
	r.Header.Add("Origin", "http://api.example")
	require.ErrorIs(t, p.Check(r, false), ErrNotAllowed)
}

func TestPolicyRejectsInvalidConfigurationAndOpaqueOrigins(t *testing.T) {
	invalid := []string{"", "*", "null", "file:///app", "http://user:password@example.com", "http://example.com/", "http://example.com?", "http://example.com?q=1", "http://example.com#fragment", "http://example.com#", "http://[example.com]", "http://example.com:", "http://example.com:0", "http://example.com:65536", "http://example.com:-1", "http://*.example.com", "http://[::1%25eth0]", "http://host:8080:90", "http://bad host", "http://host\\evil", "://invalid"}
	for _, value := range invalid {
		t.Run(value, func(t *testing.T) {
			require.Error(t, (Policy{AllowedOrigins: []string{value}}).Validate(false))
			r := httptest.NewRequest("GET", "http://example.com/", nil)
			r.Header.Set("Origin", value)
			if value != "" {
				require.Error(t, (Policy{AllowDevelopment: true, AllowedOrigins: []string{"*"}}).Check(r, false))
			}
		})
	}
	require.NoError(t, (Policy{AllowedOrigins: []string{"https://example.com"}}).Validate(true))
	require.Error(t, (Policy{AllowedOrigins: []string{"*"}, AllowDevelopment: true}).Validate(true))
	require.Error(t, (Policy{AllowDevelopment: true}).Validate(true))
	require.NoError(t, (Policy{AllowedOrigins: []string{"*"}, AllowDevelopment: true}).Validate(false))
}

func TestOperatorDevelopmentIsExplicit(t *testing.T) {
	for _, value := range []string{"http://localhost:9000", "http://127.0.0.1:7777", "https://[::1]:5555"} {
		r := httptest.NewRequest("GET", "http://api.example/", nil)
		r.Header.Set("Origin", value)
		require.Error(t, (Policy{}).Check(r, false))
		require.NoError(t, (Policy{AllowDevelopment: true}).Check(r, false))
	}
	r := httptest.NewRequest("GET", "http://api.example/", nil)
	r.Header.Set("Origin", "https://external.example")
	require.Error(t, (Policy{AllowDevelopment: true}).Check(r, false))
	require.NoError(t, (Policy{AllowDevelopment: true, AllowedOrigins: []string{"*"}}).Check(r, false))
	require.Error(t, (Policy{AllowedOrigins: []string{"*"}}).Check(r, false))
	defaults := DefaultAllowedOrigins()
	defaults[0] = "changed"
	require.Equal(t, "http://localhost:8080", DefaultAllowedOrigins()[0])
}
