package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	authconfig "github.com/codetreker/syntrix/console-server/internal/auth/config"
	dbconfig "github.com/codetreker/syntrix/console-server/internal/database/config"
	provisionconfig "github.com/codetreker/syntrix/console-server/internal/provisioning/config"
	serverconfig "github.com/codetreker/syntrix/console-server/internal/server/config"
	workloadconfig "github.com/codetreker/syntrix/console-server/internal/workload/config"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Database     dbconfig.Config        `yaml:"database"`
	Auth         authconfig.Config      `yaml:"auth"`
	Server       serverconfig.Config    `yaml:"server"`
	Provisioning provisionconfig.Config `yaml:"provisioning"`
	Workload     workloadconfig.Config  `yaml:"workload"`
}

func Load(path string) (Config, error) {
	cfg := Config{Database: dbconfig.DefaultConfig(), Auth: authconfig.DefaultConfig(), Server: serverconfig.DefaultConfig(), Provisioning: provisionconfig.DefaultConfig(), Workload: workloadconfig.DefaultConfig()}
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("read Console configuration: %w", err)
	}
	defer f.Close()
	decoder := yaml.NewDecoder(f)
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode Console configuration: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Config{}, fmt.Errorf("Console configuration must contain one YAML document")
	}
	if dsn := os.Getenv("CONSOLE_POSTGRES_DSN"); dsn != "" {
		cfg.Database.DSN = dsn
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return Config{}, err
	}
	if err := cfg.Provisioning.ResolvePaths(base); err != nil {
		return Config{}, err
	}
	for _, validate := range []func() error{cfg.Database.Validate, cfg.Auth.Validate, cfg.Server.Validate, cfg.Provisioning.Validate, cfg.Workload.Validate} {
		if err := validate(); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}
