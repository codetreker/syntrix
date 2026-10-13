package origin

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var ErrNotAllowed = errors.New("origin not allowed")

// Policy is shared by HTTP and realtime transports. Development allowances must
// be explicitly enabled and are forbidden for managed deployments.
type Policy struct {
	AllowedOrigins   []string
	AllowDevelopment bool
}

func DefaultAllowedOrigins() []string {
	return []string{"http://localhost:8080", "http://localhost:3000", "http://localhost:5173"}
}

func (p Policy) Validate(managed bool) error {
	if managed && p.AllowDevelopment {
		return errors.New("managed origins cannot enable development allowances")
	}
	for _, value := range p.AllowedOrigins {
		if value == "*" && p.AllowDevelopment && !managed {
			continue
		}
		if _, err := parse(value); err != nil {
			return errors.New("allowed origins must be HTTP or HTTPS origins without credentials, paths, queries, or fragments")
		}
	}
	return nil
}

// Check accepts an absent Origin only when the transport permits native clients.
// Opaque origins, including null, never acquire authority from development mode.
func (p Policy) Check(r *http.Request, requireOrigin bool) error {
	values := r.Header.Values("Origin")
	if len(values) == 0 || (len(values) == 1 && values[0] == "") {
		if requireOrigin {
			return errors.New("origin required when using credentials")
		}
		return nil
	}
	if len(values) != 1 {
		return ErrNotAllowed
	}
	requested, err := parse(values[0])
	if err != nil {
		return ErrNotAllowed
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if own, err := parse(scheme + "://" + r.Host); err == nil && own == requested {
		return nil
	}
	for _, value := range p.AllowedOrigins {
		if value == "*" && p.AllowDevelopment {
			return nil
		}
		if allowed, err := parse(value); err == nil && requested == allowed {
			return nil
		}
	}
	if p.AllowDevelopment && (requested.host == "localhost" || net.ParseIP(requested.host).IsLoopback()) {
		return nil
	}
	return ErrNotAllowed
}

type authority struct {
	scheme, host, port string
}

func parse(value string) (authority, error) {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || strings.ContainsAny(value, "# \t\r\n\\") {
		return authority{}, ErrNotAllowed
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || strings.ContainsAny(host, "*%") || strings.HasSuffix(u.Host, ":") {
		return authority{}, ErrNotAllowed
	}
	if (strings.Contains(host, ":") || strings.HasPrefix(u.Host, "[")) && net.ParseIP(host) == nil {
		return authority{}, ErrNotAllowed
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	} else {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return authority{}, fmt.Errorf("%w: invalid port", ErrNotAllowed)
		}
		port = strconv.Itoa(n)
	}
	return authority{scheme: u.Scheme, host: host, port: port}, nil
}
