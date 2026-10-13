package config

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"net/url"
	"regexp"
	"strings"

	pubsubconfig "github.com/codetreker/syntrix/internal/core/pubsub/config"

	"github.com/codetreker/syntrix/internal/core/origin"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Mode               string                      `yaml:"mode"`
	ResourceMode       string                      `yaml:"resource_mode"`
	Namespace          string                      `yaml:"namespace"`
	NamePrefix         string                      `yaml:"name_prefix"`
	Image              string                      `yaml:"image"`
	StorageClass       string                      `yaml:"storage_class"`
	StorageGiB         int                         `yaml:"storage_gib"`
	SigningKeySecret   string                      `yaml:"signing_key_secret"`
	BootstrapResources Resources                   `yaml:"bootstrap_resources"`
	Connections        Connections                 `yaml:"connections"`
	Broker             *pubsubconfig.BrokerBinding `yaml:"broker"`
	Identity           Identity                    `yaml:"identity"`
	AllowedOrigins     []string                    `yaml:"allowed_origins"`
	Workloads          map[string]Workload         `yaml:"workloads"`
}

type Connections struct {
	MongoDBURI      string `yaml:"mongodb_uri"`
	MongoDBDatabase string `yaml:"mongodb_database"`
	PostgresDSN     string `yaml:"postgres_dsn"`
	NATSURL         string `yaml:"nats_url"`
}

type Identity struct {
	AdminUsername string `yaml:"admin_username"`
	AdminPassword string `yaml:"admin_password"`
}

type Workload struct {
	Name      string    `yaml:"name"`
	Replicas  *int      `yaml:"replicas"`
	Resources Resources `yaml:"resources"`
}

type Resources struct {
	Requests Quantities `yaml:"requests"`
	Limits   Quantities `yaml:"limits"`
}

type Quantities struct {
	CPU    string `yaml:"cpu"`
	Memory string `yaml:"memory"`
}

var distributedRoles = []string{"api", "query", "indexer", "puller", "streamer", "trigger_evaluator", "trigger_worker"}
var dnsLabel = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)

type sanitizedError struct {
	message string
	cause   error
}

func (e sanitizedError) Error() string { return e.message }
func (e sanitizedError) Unwrap() error { return e.cause }

func (c Config) Roles() []string {
	if c.Mode == "standalone" {
		return []string{"standalone"}
	}
	return append([]string(nil), distributedRoles...)
}

func Load(reader io.Reader) (Config, error) {
	var c Config
	data, err := io.ReadAll(reader)
	if err != nil {
		return c, sanitizedError{message: "read deployment configuration", cause: err}
	}
	var syntax yaml.Node
	if err := yaml.Unmarshal(data, &syntax); err != nil {
		return c, sanitizedError{message: "deployment configuration must use valid YAML, known fields, unique keys, and correct value types", cause: err}
	}
	if err := validateIntegerNodes(&syntax, make(map[*yaml.Node]bool)); err != nil {
		return c, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&c); err != nil {
		return c, sanitizedError{message: "deployment configuration must use valid YAML, known fields, unique keys, and correct value types", cause: err}
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		return c, sanitizedError{message: "deployment configuration must contain exactly one YAML document", cause: err}
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	return c, nil
}

func validateIntegerNodes(node *yaml.Node, seen map[*yaml.Node]bool) error {
	if seen[node] {
		return nil
	}
	seen[node] = true
	if node.Kind == yaml.AliasNode {
		return validateIntegerNodes(node.Alias, seen)
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			if key.Kind == yaml.AliasNode {
				key = key.Alias
			}
			if key.Value != "storage_gib" && key.Value != "replicas" && key.Value != "spec_revision" && key.Value != "max_bytes" && key.Value != "max_messages" {
				continue
			}
			if value.Kind == yaml.AliasNode {
				value = value.Alias
			}
			if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!int" {
				return fmt.Errorf("%s must be a YAML integer", key.Value)
			}
		}
	}
	for _, child := range node.Content {
		if err := validateIntegerNodes(child, seen); err != nil {
			return err
		}
	}
	return nil
}

func (c Config) Validate() error {
	if c.Mode != "standalone" && c.Mode != "distributed" {
		return fmt.Errorf("mode must be standalone or distributed")
	}
	if c.ResourceMode != "managed" && c.ResourceMode != "operator" {
		return fmt.Errorf("resource_mode must be managed or operator")
	}
	for _, item := range []struct{ field, value string }{
		{"namespace", c.Namespace}, {"name_prefix", c.NamePrefix},
	} {
		if err := validateName(item.field, item.value, 63); err != nil {
			return err
		}
	}
	if c.Image == "" || strings.ContainsAny(c.Image, " \t\r\n\x00") {
		return fmt.Errorf("image must be a nonempty image reference without whitespace")
	}
	if c.StorageGiB <= 0 {
		return fmt.Errorf("storage_gib must be positive")
	}
	if c.StorageClass != "" {
		if err := validateName("storage_class", c.StorageClass, 63); err != nil {
			return err
		}
	}
	if err := c.validateConnections(); err != nil {
		return err
	}
	if c.Mode == "distributed" {
		if c.Broker == nil {
			return fmt.Errorf("broker is required in distributed mode")
		}
		if c.Broker.ResourceMode != c.ResourceMode {
			return fmt.Errorf("broker.resource_mode must match resource_mode")
		}
		if _, err := c.Broker.Specification(); err != nil {
			return fmt.Errorf("broker: %w", err)
		}
	} else if c.Broker != nil {
		return fmt.Errorf("broker is only supported in distributed mode")
	}
	if err := c.BootstrapResources.Validate(); err != nil {
		return fmt.Errorf("bootstrap_resources: %w", err)
	}
	if c.Identity.AdminUsername == "" || c.Identity.AdminPassword == "" {
		return fmt.Errorf("identity.admin_username and identity.admin_password must be nonempty")
	}
	if len(c.AllowedOrigins) == 0 {
		return fmt.Errorf("allowed_origins must contain at least one HTTP or HTTPS origin")
	}
	if err := (origin.Policy{AllowedOrigins: c.AllowedOrigins}).Validate(c.ResourceMode == "managed"); err != nil {
		return fmt.Errorf("allowed_origins: %w", err)
	}
	return c.validateWorkloads()
}

func (c Config) validateConnections() error {
	for _, connection := range []struct {
		field, value string
		schemes      []string
	}{
		{"connections.mongodb_uri", c.Connections.MongoDBURI, []string{"mongodb", "mongodb+srv"}},
		{"connections.postgres_dsn", c.Connections.PostgresDSN, []string{"postgres", "postgresql"}},
	} {
		if err := validateURI(connection.field, connection.value, connection.schemes); err != nil {
			return err
		}
	}
	if c.Connections.MongoDBDatabase == "" || strings.ContainsAny(c.Connections.MongoDBDatabase, "/\\. \"$\x00") {
		return fmt.Errorf("connections.mongodb_database must be a valid nonempty MongoDB database name")
	}
	if c.Mode == "distributed" || c.Connections.NATSURL != "" {
		if err := validateURI("connections.nats_url", c.Connections.NATSURL, []string{"nats", "tls"}); err != nil {
			return err
		}
	}
	if c.Mode == "distributed" && c.SigningKeySecret == "" {
		return fmt.Errorf("signing_key_secret is required in distributed mode")
	}
	if c.SigningKeySecret != "" {
		return validateName("signing_key_secret", c.SigningKeySecret, 63)
	}
	return nil
}

func validateURI(field, value string, schemes []string) error {
	u, err := url.Parse(value)
	if err == nil && u != nil && u.Hostname() != "" && u.Fragment == "" {
		for _, scheme := range schemes {
			if u.Scheme == scheme {
				return nil
			}
		}
	}
	return fmt.Errorf("%s must be an absolute %s URI with a host and no fragment", field, strings.Join(schemes, " or "))
}

func (c Config) validateWorkloads() error {
	roles := c.Roles()
	if len(c.Workloads) != len(roles) {
		return fmt.Errorf("workloads must contain exactly these roles: %s", strings.Join(roles, ", "))
	}
	names := make(map[string]bool)
	for _, suffix := range []string{"credentials", "bootstrap", "bootstrap-config"} {
		names[c.NamePrefix+"-"+suffix] = true
	}
	if c.Mode == "standalone" {
		names[c.NamePrefix+"-config"] = true
		names[c.NamePrefix+"-data"] = true
	} else {
		names[c.NamePrefix+"-puller-data"] = true
		names[c.NamePrefix+"-indexer-data"] = true
	}
	for name := range names {
		if err := validateName("derived resource name", name, 63); err != nil {
			return err
		}
	}
	if c.SigningKeySecret != "" {
		if names[c.SigningKeySecret] {
			return fmt.Errorf("signing_key_secret collides with a generated resource name")
		}
		names[c.SigningKeySecret] = true
	}
	for _, role := range roles {
		w, ok := c.Workloads[role]
		if !ok {
			return fmt.Errorf("workloads.%s is required", role)
		}
		if err := validateName("workloads."+role+".name", w.Name, 52); err != nil {
			return err
		}
		if names[w.Name] {
			return fmt.Errorf("workloads.%s.name collides with another resource name", role)
		}
		names[w.Name] = true
		if c.Mode == "distributed" {
			configName := w.Name + "-config"
			if names[configName] {
				return fmt.Errorf("workloads.%s.name produces a colliding ConfigMap name", role)
			}
			names[configName] = true
		}
		if w.Replicas == nil || *w.Replicas < 0 || int64(*w.Replicas) > math.MaxInt32 {
			return fmt.Errorf("workloads.%s.replicas must be explicitly set between 0 and 2147483647", role)
		}
		if (role == "standalone" || role == "indexer" || role == "puller" || role == "trigger_evaluator") && *w.Replicas > 1 {
			return fmt.Errorf("workloads.%s.replicas must be 0 or 1 because it owns singleton runtime state", role)
		}
		if err := w.Resources.Validate(); err != nil {
			return fmt.Errorf("workloads.%s.resources: %w", role, err)
		}
	}
	return nil
}

func validateName(field, value string, max int) error {
	if len(value) > max || !dnsLabel.MatchString(value) {
		return fmt.Errorf("%s must be a lowercase Kubernetes DNS label starting with a letter and at most %d characters", field, max)
	}
	return nil
}
