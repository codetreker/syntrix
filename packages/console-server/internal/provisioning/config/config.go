package config

import (
	"fmt"
	"path/filepath"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

type Config struct {
	Mode           string        `yaml:"mode"`
	Kubeconfig     string        `yaml:"kubeconfig"`
	Context        string        `yaml:"context"`
	Namespace      string        `yaml:"namespace"`
	InstallationID string        `yaml:"installation_id"`
	RequestTimeout time.Duration `yaml:"request_timeout"`
}

func DefaultConfig() Config {
	return Config{RequestTimeout: 10 * time.Second}
}

func (c *Config) ResolvePaths(base string) error {
	if c.Kubeconfig != "" && !filepath.IsAbs(c.Kubeconfig) {
		c.Kubeconfig = filepath.Join(base, c.Kubeconfig)
	}
	return nil
}

func (c Config) Validate() error {
	switch c.Mode {
	case "in_cluster":
		if c.Kubeconfig != "" || c.Context != "" {
			return fmt.Errorf("in_cluster mode forbids kubeconfig and context")
		}
	case "kubeconfig":
		if c.Kubeconfig == "" || c.Context == "" {
			return fmt.Errorf("kubeconfig mode requires an explicit file and context")
		}
	default:
		return fmt.Errorf("kubernetes mode must be in_cluster or kubeconfig")
	}
	if len(validation.IsDNS1123Label(c.Namespace)) != 0 {
		return fmt.Errorf("kubernetes namespace must be a DNS label")
	}
	if len(validation.IsDNS1123Label(c.InstallationID)) != 0 {
		return fmt.Errorf("kubernetes installation_id must be a DNS label")
	}
	if c.RequestTimeout <= 0 || c.RequestTimeout > time.Minute {
		return fmt.Errorf("kubernetes request_timeout must be positive and at most one minute")
	}
	return nil
}
