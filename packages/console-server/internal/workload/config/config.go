package config

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/codetreker/syntrix/console-server/internal/workload/types"
	"k8s.io/apimachinery/pkg/api/resource"
)

type Config struct {
	Templates         []types.Template `yaml:"templates"`
	ReconcileInterval time.Duration    `yaml:"reconcile_interval"`
	OperationTimeout  time.Duration    `yaml:"operation_timeout"`
}

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)
var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var imageDigest = regexp.MustCompile(`^sha256:[a-fA-F0-9]{64}$`)

func DefaultConfig() Config {
	return Config{ReconcileInterval: 2 * time.Second, OperationTimeout: 20 * time.Second}
}

func (c *Config) ApplyDefaults() {
	defaults := DefaultConfig()
	if c.ReconcileInterval == 0 {
		c.ReconcileInterval = defaults.ReconcileInterval
	}
	if c.OperationTimeout == 0 {
		c.OperationTimeout = defaults.OperationTimeout
	}
}

func (c Config) Validate() error {
	if c.ReconcileInterval <= 0 || c.OperationTimeout <= 0 || c.OperationTimeout > 2*time.Minute {
		return fmt.Errorf("workload interval must be positive and operation timeout must be between zero and two minutes")
	}
	if len(c.Templates) == 0 {
		return fmt.Errorf("at least one workload template is required")
	}
	ids := make(map[string]bool, len(c.Templates))
	for _, template := range c.Templates {
		if !identifier.MatchString(template.ID) || ids[template.ID] {
			return fmt.Errorf("workload template IDs must be valid and unique")
		}
		ids[template.ID] = true
		if !versionedImage(template.Image) || template.Port < 1 || template.Port > 65535 || !strings.HasPrefix(template.ProbePath, "/") || strings.ContainsAny(template.ProbePath, "\r\n?#") {
			return fmt.Errorf("template %q requires an image, valid port, and HTTP probe path", template.ID)
		}
		if err := validateResources(template.CPURequest, template.CPULimit); err != nil {
			return fmt.Errorf("template %q CPU: %w", template.ID, err)
		}
		if err := validateResources(template.MemoryRequest, template.MemoryLimit); err != nil {
			return fmt.Errorf("template %q memory: %w", template.ID, err)
		}
		for key := range template.Env {
			if !environmentName.MatchString(key) {
				return fmt.Errorf("template %q contains an invalid environment variable name", template.ID)
			}
		}
	}
	return nil
}

func versionedImage(image string) bool {
	if image == "" || strings.ContainsAny(image, " \t\r\n") {
		return false
	}
	if name, digest, hasDigest := strings.Cut(image, "@"); hasDigest {
		return name != "" && imageDigest.MatchString(digest)
	}
	imageName := image[strings.LastIndex(image, "/")+1:]
	name, tag, tagged := strings.Cut(imageName, ":")
	return tagged && name != "" && tag != "" && tag != "latest" && !strings.Contains(tag, ":")
}

func validateResources(request, limit string) error {
	r, err := resource.ParseQuantity(request)
	if err != nil || r.Sign() <= 0 {
		return fmt.Errorf("request must be a positive Kubernetes quantity")
	}
	l, err := resource.ParseQuantity(limit)
	if err != nil || l.Sign() <= 0 || r.Cmp(l) > 0 {
		return fmt.Errorf("limit must be a positive Kubernetes quantity at least equal to request")
	}
	return nil
}
