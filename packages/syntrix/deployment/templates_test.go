package deployment

import (
	"bytes"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codetreker/syntrix/deployment/config"
	runtimeconfig "github.com/codetreker/syntrix/internal/config"
	"github.com/codetreker/syntrix/internal/services"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func renderedObjects(t *testing.T, manifest []byte) map[string]map[string]any {
	t.Helper()
	objects := make(map[string]map[string]any)
	decoder := yaml.NewDecoder(bytes.NewReader(manifest))
	for {
		var object map[string]any
		err := decoder.Decode(&object)
		if err == io.EOF {
			return objects
		}
		require.NoError(t, err)
		key := object["kind"].(string) + "/" + object["metadata"].(map[string]any)["name"].(string)
		require.NotContains(t, objects, key, "duplicate Kubernetes object")
		objects[key] = object
	}
}

func objectSpec(t *testing.T, objects map[string]map[string]any, kind, name string) map[string]any {
	t.Helper()
	object, ok := objects[kind+"/"+name]
	require.True(t, ok, "missing %s/%s", kind, name)
	return object["spec"].(map[string]any)
}

func namedVolume(volumes []any, name string) map[string]any {
	for _, raw := range volumes {
		volume := raw.(map[string]any)
		if volume["name"] == name {
			return volume
		}
	}
	return nil
}

func TestShippedTemplatesUseConfiguredTopologyAndSafetyPhases(t *testing.T) {
	for _, mode := range []string{"standalone", "distributed"} {
		for _, bootstrap := range []bool{false, true} {
			t.Run(mode+"/"+map[bool]string{false: "normal", true: "bootstrap"}[bootstrap], func(t *testing.T) {
				path := filepath.Join(mode, "kubernetes.template.yml")
				source, err := os.ReadFile(path)
				require.NoError(t, err)
				c := fixtureConfig(mode)
				c.Namespace, c.NamePrefix, c.Image = "customer-private", "tenant-red", "registry.example.test/syntrix:custom"
				c.StorageClass, c.StorageGiB = "fast-storage", 37
				c.AllowedOrigins = []string{"https://customer.example.test:9443"}
				c.Identity.AdminPassword = "Admin_Secret123!\n---\nkind: Pod\nmetadata: {name: injected}"
				c.BootstrapResources = config.Resources{Requests: config.Quantities{CPU: "500m", Memory: "512Mi"}, Limits: config.Quantities{CPU: "1500m", Memory: "2Gi"}}
				for role, w := range c.Workloads {
					w.Name = "custom-" + strings.ReplaceAll(role, "_", "-")
					w.Resources = config.Resources{Requests: config.Quantities{CPU: "0.2", Memory: "200M"}, Limits: config.Quantities{CPU: "750m", Memory: "400Mi"}}
					n := 1
					if role == "api" || role == "trigger_worker" {
						n = 3
					} else if role == "query" {
						n = 0
					}
					w.Replicas = &n
					c.Workloads[role] = w
				}
				manifest, err := RenderSource(source, c, Options{Bootstrap: bootstrap, WritesQuiesced: bootstrap})
				require.NoError(t, err)
				objects := renderedObjects(t, manifest)
				require.Contains(t, objects, "Namespace/customer-private")
				deployments, pods, claims := 0, 0, 0
				for key, object := range objects {
					if strings.HasPrefix(key, "Namespace/") {
						continue
					}
					assert.Equal(t, c.Namespace, object["metadata"].(map[string]any)["namespace"])
					if strings.HasPrefix(key, "Deployment/") {
						deployments++
					} else if strings.HasPrefix(key, "Pod/") {
						pods++
					} else if strings.HasPrefix(key, "PersistentVolumeClaim/") {
						claims++
						spec := object["spec"].(map[string]any)
						assert.Equal(t, "fast-storage", spec["storageClassName"])
						assert.Equal(t, "37Gi", spec["resources"].(map[string]any)["requests"].(map[string]any)["storage"])
					}
				}
				assert.Equal(t, len(c.Workloads), deployments)
				assert.Equal(t, map[bool]int{false: 0, true: 1}[bootstrap], pods)
				assert.Equal(t, map[string]int{"standalone": 1, "distributed": 2}[mode], claims)
				for role, workload := range c.Workloads {
					spec := objectSpec(t, objects, "Deployment", workload.Name)
					if bootstrap {
						assert.Equal(t, 0, spec["replicas"])
					} else {
						assert.Equal(t, *workload.Replicas, spec["replicas"])
					}
					podTemplate := spec["template"].(map[string]any)
					selector := spec["selector"].(map[string]any)["matchLabels"]
					assert.Equal(t, selector, podTemplate["metadata"].(map[string]any)["labels"])
					podSpec := podTemplate["spec"].(map[string]any)
					container := podSpec["containers"].([]any)[0].(map[string]any)
					assert.Equal(t, c.Image, container["image"])
					assert.Equal(t, "--"+strings.ReplaceAll(role, "_", "-"), container["args"].([]any)[0])
					resources := container["resources"].(map[string]any)
					assert.Equal(t, workload.Resources.Requests.CPU, resources["requests"].(map[string]any)["cpu"])
					assert.Equal(t, workload.Resources.Limits.Memory, resources["limits"].(map[string]any)["memory"])
					key := namedVolume(podSpec["volumes"].([]any), "signing-key")
					if mode == "distributed" && (role == "api" || role == "trigger_worker") {
						require.NotNil(t, key)
						assert.Equal(t, c.SigningKeySecret, key["secret"].(map[string]any)["secretName"])
					} else {
						assert.Nil(t, key)
					}
					configName := workload.Name + "-config"
					if mode == "standalone" {
						configName = c.NamePrefix + "-config"
						dataVolume := namedVolume(podSpec["volumes"].([]any), "data")
						assert.Equal(t, c.NamePrefix+"-data", dataVolume["persistentVolumeClaim"].(map[string]any)["claimName"])
					}
					publicConfig := objects["ConfigMap/"+configName]["data"].(map[string]any)["config.yml"].(string)
					assert.NotContains(t, publicConfig, c.Identity.AdminPassword)
					var runtime runtimeconfig.Config
					require.NoError(t, yaml.Unmarshal([]byte(publicConfig), &runtime))
					assert.Equal(t, c.AllowedOrigins, runtime.Gateway.Realtime.AllowedOrigins)
					if mode == "distributed" {
						assert.Equal(t, c.Workloads["query"].Name+":9000", runtime.Gateway.QueryServiceURL)
						assert.Equal(t, c.Workloads["streamer"].Name+":9000", runtime.Gateway.StreamerServiceURL)
						assert.Equal(t, c.Workloads["indexer"].Name+":9000", runtime.Query.IndexerAddr)
						assert.Equal(t, c.Workloads["puller"].Name+":9000", runtime.Indexer.PullerAddr)
						assert.Equal(t, c.Workloads["puller"].Name+":9000", runtime.Streamer.Server.PullerAddr)
						assert.Equal(t, c.Workloads["puller"].Name+":9000", runtime.Trigger.Evaluator.PullerAddr)
					}
				}
				secretConfig := objects["Secret/"+c.NamePrefix+"-credentials"]["stringData"].(map[string]any)["config.local.yml"].(string)
				var credentials runtimeconfig.Config
				require.NoError(t, yaml.Unmarshal([]byte(secretConfig), &credentials))
				assert.Equal(t, c.Identity.AdminPassword, credentials.Identity.Admin.Password)
				if bootstrap {
					podSpec := objectSpec(t, objects, "Pod", c.NamePrefix+"-bootstrap")
					assert.Equal(t, "Never", podSpec["restartPolicy"])
					container := podSpec["containers"].([]any)[0].(map[string]any)
					assert.Contains(t, container["args"], "--bootstrap-indexes")
					assert.Contains(t, container["args"], "--writes-quiesced")
					assert.NotContains(t, container["args"], "--initialization-acceptance")
					resources := container["resources"].(map[string]any)
					assert.Equal(t, c.BootstrapResources.Requests.CPU, resources["requests"].(map[string]any)["cpu"])
					assert.Equal(t, c.BootstrapResources.Limits.Memory, resources["limits"].(map[string]any)["memory"])
					bootstrapConfig := objects["ConfigMap/"+c.NamePrefix+"-bootstrap-config"]["data"].(map[string]any)["config.yml"].(string)
					var runtime runtimeconfig.Config
					require.NoError(t, yaml.Unmarshal([]byte(bootstrapConfig), &runtime))
					assert.Equal(t, "standalone", string(runtime.Deployment.Mode))
				}
			})
		}
	}
}

func TestShippedTemplatesRejectWrongModeAndSupportOptionalStandaloneKey(t *testing.T) {
	for _, mode := range []string{"standalone", "distributed"} {
		source, err := os.ReadFile(filepath.Join(mode, "kubernetes.template.yml"))
		require.NoError(t, err)
		other := "standalone"
		if mode == "standalone" {
			other = "distributed"
		}
		_, err = RenderSource(source, fixtureConfig(other), Options{})
		require.ErrorContains(t, err, "mode")
	}
	c := fixtureConfig("standalone")
	c.SigningKeySecret = "optional-standalone-key"
	source, err := os.ReadFile(filepath.Join("standalone", "kubernetes.template.yml"))
	require.NoError(t, err)
	manifest, err := RenderSource(source, c, Options{})
	require.NoError(t, err)
	objects := renderedObjects(t, manifest)
	spec := objectSpec(t, objects, "Deployment", c.Workloads["standalone"].Name)["template"].(map[string]any)["spec"].(map[string]any)
	key := namedVolume(spec["volumes"].([]any), "signing-key")
	require.NotNil(t, key)
	assert.Equal(t, c.SigningKeySecret, key["secret"].(map[string]any)["secretName"])
}

func TestAcceptanceHoldsWritersAndUsesRoleReadiness(t *testing.T) {
	for _, mode := range []string{"standalone", "distributed"} {
		c := fixtureConfig(mode)
		manifest, err := Render(c, Options{AcceptanceOnly: true})
		require.NoError(t, err)
		objects := renderedObjects(t, manifest)
		require.NotContains(t, objects, "Pod/"+c.NamePrefix+"-bootstrap")
		for role, w := range c.Workloads {
			spec := objectSpec(t, objects, "Deployment", w.Name)
			if role == "trigger_evaluator" || role == "trigger_worker" {
				assert.Equal(t, 0, spec["replicas"])
			} else {
				assert.Equal(t, *w.Replicas, spec["replicas"])
			}
			container := spec["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
			assert.Contains(t, container["args"], "--initialization-acceptance")
			assert.Contains(t, container["args"], "--writes-quiesced")
			assert.Equal(t, "IfNotPresent", container["imagePullPolicy"])
			for _, probe := range []string{"startupProbe", "readinessProbe"} {
				p := container[probe].(map[string]any)
				if role == "standalone" || role == "api" {
					assert.Equal(t, "/ready", p["httpGet"].(map[string]any)["path"])
				} else {
					g := p["grpc"].(map[string]any)
					assert.Equal(t, "syntrix."+role, g["service"])
					assert.Equal(t, 9000, g["port"])
				}
			}
			live := container["livenessProbe"].(map[string]any)
			if role == "standalone" || role == "api" {
				assert.Equal(t, "/health", live["httpGet"].(map[string]any)["path"])
			} else {
				assert.Equal(t, "syntrix.liveness", live["grpc"].(map[string]any)["service"])
			}
		}
	}
}

func TestGeneratedPhasesSatisfyRuntimeMaintenanceOptions(t *testing.T) {
	for _, mode := range []string{"standalone", "distributed"} {
		for _, options := range []Options{{}, {AcceptanceOnly: true}, {Bootstrap: true, WritesQuiesced: true}} {
			manifest, err := Render(fixtureConfig(mode), options)
			require.NoError(t, err)
			for name, object := range renderedObjects(t, manifest) {
				var pod map[string]any
				switch object["kind"] {
				case "Deployment":
					pod = object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
				case "Pod":
					pod = object["spec"].(map[string]any)
				default:
					continue
				}
				container := pod["containers"].([]any)[0].(map[string]any)
				var args []string
				for _, value := range container["args"].([]any) {
					args = append(args, value.(string))
				}
				flags := flag.NewFlagSet("runtime", flag.ContinueOnError)
				flags.SetOutput(io.Discard)
				for _, role := range []string{"standalone", "api", "query", "indexer", "puller", "streamer", "trigger-evaluator", "trigger-worker"} {
					flags.Bool(role, false, "")
				}
				flags.String("config-dir", "", "")
				var runtimeOptions services.Options
				flags.BoolVar(&runtimeOptions.BootstrapIndexes, "bootstrap-indexes", false, "")
				flags.BoolVar(&runtimeOptions.InitializationAcceptance, "initialization-acceptance", false, "")
				flags.BoolVar(&runtimeOptions.WritesQuiesced, "writes-quiesced", false, "")
				flags.DurationVar(&runtimeOptions.BootstrapTimeout, "bootstrap-timeout", 30*time.Minute, "")
				require.NoError(t, flags.Parse(args), name)
				require.Empty(t, flags.Args(), name)
				require.NoError(t, services.NewManager(runtimeconfig.DefaultConfig(), runtimeOptions).PrepareIndexBootstrap(), name)
				if object["kind"] == "Pod" {
					assert.True(t, runtimeOptions.BootstrapIndexes, name)
					assert.False(t, runtimeOptions.InitializationAcceptance, name)
				} else {
					assert.Equal(t, options.AcceptanceOnly, runtimeOptions.InitializationAcceptance, name)
				}
			}
		}
	}
}
