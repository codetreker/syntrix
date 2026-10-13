package deployment

import (
	"fmt"
	"strings"

	"github.com/codetreker/syntrix/deployment/config"
	"gopkg.in/yaml.v3"
)

type model struct {
	Mode, Namespace, NamePrefix, Image, ConfigName, CredentialsName    string
	SigningKeySecret, StorageClass, BootstrapName, BootstrapConfigName string
	StandalonePVCName, PullerPVCName, IndexerPVCName                   string
	StorageGiB                                                         int
	Bootstrap                                                          bool
	AcceptanceOnly                                                     bool
	BootstrapResources                                                 config.Resources
	Workloads                                                          []workload
	configuration                                                      config.Config
}

type workload struct {
	Role, Name, Flag, PVCName, ConfigName string
	Replicas                              int
	Resources                             config.Resources
	Stateful, HasKey                      bool
}

func buildModel(c config.Config, options Options) model {
	m := model{
		Mode: c.Mode, Namespace: c.Namespace, NamePrefix: c.NamePrefix, Image: c.Image,
		ConfigName: c.NamePrefix + "-config", CredentialsName: c.NamePrefix + "-credentials",
		SigningKeySecret: c.SigningKeySecret, StorageClass: c.StorageClass, StorageGiB: c.StorageGiB,
		Bootstrap: options.Bootstrap, AcceptanceOnly: options.AcceptanceOnly, BootstrapName: c.NamePrefix + "-bootstrap", BootstrapConfigName: c.NamePrefix + "-bootstrap-config",
		StandalonePVCName: c.NamePrefix + "-data", PullerPVCName: c.NamePrefix + "-puller-data", IndexerPVCName: c.NamePrefix + "-indexer-data",
		BootstrapResources: c.BootstrapResources, configuration: c,
	}
	for _, role := range c.Roles() {
		w := c.Workloads[role]
		item := workload{
			Role: role, Name: w.Name, Flag: "--" + strings.ReplaceAll(role, "_", "-"),
			Replicas: *w.Replicas, Resources: w.Resources, ConfigName: w.Name + "-config",
			HasKey: c.SigningKeySecret != "" && (role == "standalone" || role == "api" || role == "trigger_worker"),
		}
		if options.Bootstrap || (options.AcceptanceOnly && (role == "trigger_evaluator" || role == "trigger_worker")) {
			item.Replicas = 0
		}
		switch role {
		case "standalone":
			item.Stateful, item.PVCName, item.ConfigName = true, m.StandalonePVCName, m.ConfigName
		case "puller":
			item.Stateful, item.PVCName = true, m.PullerPVCName
		case "indexer":
			item.Stateful, item.PVCName = true, m.IndexerPVCName
		}
		m.Workloads = append(m.Workloads, item)
	}
	return m
}

func (m model) runtimeConfig(role string) (string, error) {
	mode := m.Mode
	if role == "bootstrap" {
		mode = "standalone"
	} else if _, ok := m.configuration.Workloads[role]; !ok {
		return "", fmt.Errorf("runtimeConfig requires a configured workload role or bootstrap")
	}
	keyPath := "/data/keys/auth_private.pem"
	if m.SigningKeySecret != "" {
		keyPath = "/etc/syntrix/keys/auth_private.pem"
	}
	values := map[string]any{
		"deployment":     map[string]any{"mode": mode},
		"initialization": map[string]any{"mode": m.configuration.ResourceMode},
		"data_dir":       "/data",
		"server":         map[string]any{"host": "0.0.0.0", "http_port": 8080, "grpc_port": 9000, "enable_cors": true, "allow_dev_origin": false, "allowed_origins": m.configuration.AllowedOrigins},
		"identity":       map[string]any{"authn": map[string]any{"private_key_file": keyPath}},
		"gateway": map[string]any{
			"authz":    map[string]any{"rules_path": "security_rules"},
			"realtime": map[string]any{"allow_dev_origin": false, "allowed_origins": m.configuration.AllowedOrigins},
		},
		"indexer": map[string]any{
			"template_path": "index_templates", "storage_mode": "pebble",
			"store": map[string]any{"path": "indexer/indexes.db"},
		},
		"puller": map[string]any{
			"backends": []any{map[string]any{"name": "default_mongo", "collections": []string{"documents", "sys"}}},
			"buffer":   map[string]any{"path": "puller/events", "max_size": "1GiB"},
		},
		"trigger": map[string]any{"evaluator": map[string]any{"rules_path": "triggers"}},
		"logging": map[string]any{
			"dir": "logs", "console": map[string]any{"enabled": true, "format": "json"},
			"file": map[string]any{"enabled": false},
		},
	}
	if mode == "distributed" {
		broker := m.configuration.Broker
		trigger := values["trigger"].(map[string]any)
		trigger["broker"] = broker
		evaluator := trigger["evaluator"].(map[string]any)
		evaluator["stream_name"], evaluator["storage_type"] = broker.StreamName, broker.Policy.Storage
		trigger["delivery"] = map[string]any{"stream_name": broker.StreamName, "consumer_name": broker.ConsumerName, "storage_type": broker.Policy.Storage}
		address := func(role string) string { return m.configuration.Workloads[role].Name + ":9000" }
		values["gateway"].(map[string]any)["query_service_url"] = address("query")
		values["gateway"].(map[string]any)["streamer_service_url"] = address("streamer")
		values["query"] = map[string]any{"indexer_addr": address("indexer")}
		values["indexer"].(map[string]any)["puller_addr"] = address("puller")
		values["streamer"] = map[string]any{"server": map[string]any{"puller_addr": address("puller")}}
		values["trigger"].(map[string]any)["evaluator"].(map[string]any)["puller_addr"] = address("puller")
	}
	data, err := yaml.Marshal(values)
	return string(data), err
}

func (m model) credentials() (string, error) {
	c := m.configuration
	values := map[string]any{
		"storage": map[string]any{"backends": map[string]any{
			"default_mongo":    map[string]any{"type": "mongo", "mongo": map[string]any{"uri": c.Connections.MongoDBURI, "database_name": c.Connections.MongoDBDatabase}},
			"default_postgres": map[string]any{"type": "postgres", "postgres": map[string]any{"dsn": c.Connections.PostgresDSN}},
		}},
		"identity": map[string]any{
			"admin": map[string]any{"username": c.Identity.AdminUsername, "password": c.Identity.AdminPassword},
			"authn": map[string]any{"admin_username": c.Identity.AdminUsername},
		},
	}
	if c.Connections.NATSURL != "" {
		values["trigger"] = map[string]any{"nats_url": c.Connections.NATSURL}
	}
	data, err := yaml.Marshal(values)
	return string(data), err
}
