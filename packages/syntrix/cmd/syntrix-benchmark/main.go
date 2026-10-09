// Package main provides the CLI entry point for the benchmark tool.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/codetreker/syntrix/pkg/benchmark/config"
	"github.com/codetreker/syntrix/pkg/benchmark/metrics"
	"github.com/codetreker/syntrix/pkg/benchmark/reporter"
	"github.com/codetreker/syntrix/pkg/benchmark/runner"
	"github.com/codetreker/syntrix/pkg/benchmark/scenario"
	"github.com/codetreker/syntrix/pkg/benchmark/types"
	"github.com/codetreker/syntrix/pkg/benchmark/utils"
)

// Version is the benchmark tool version (can be overridden at build time).
var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "run":
		if err := runBenchmark(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "version":
		fmt.Printf("syntrix-benchmark version %s\n", Version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		printUsage()
		os.Exit(1)
	}
}

func runBenchmark(args []string) error {
	// Parse flags
	configFile := ""
	target := ""
	duration := ""
	workers := 0
	noColor := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c", "--config":
			if i+1 >= len(args) {
				return fmt.Errorf("missing value for %s", args[i])
			}
			configFile = args[i+1]
			i++
		case "-t", "--target":
			if i+1 >= len(args) {
				return fmt.Errorf("missing value for %s", args[i])
			}
			target = args[i+1]
			i++
		case "-d", "--duration":
			if i+1 >= len(args) {
				return fmt.Errorf("missing value for %s", args[i])
			}
			duration = args[i+1]
			i++
		case "-w", "--workers":
			if i+1 >= len(args) {
				return fmt.Errorf("missing value for %s", args[i])
			}
			fmt.Sscanf(args[i+1], "%d", &workers)
			i++
		case "--no-color":
			noColor = true
		case "-h", "--help":
			printRunUsage()
			return nil
		default:
			return fmt.Errorf("unknown flag: %s", args[i])
		}
	}

	// Load configuration
	var cfg *types.Config
	var err error

	if configFile != "" {
		cfg, err = config.Load(configFile)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
	} else {
		// Create default config
		cfg = &types.Config{
			Name:     "benchmark",
			Target:   "http://localhost:8080",
			Duration: 10 * time.Second,
			Workers:  5,
			Auth: types.AuthConfig{
				Token:          "",
				Database:       "default",
				PrivateKeyFile: "configs/keys/auth_private.pem", // Use full path for default config
			},
			Scenario: types.ScenarioConfig{
				Type: "crud",
				Operations: []types.OperationConfig{
					{Type: "create", Weight: 25},
					{Type: "read", Weight: 25},
					{Type: "update", Weight: 25},
					{Type: "delete", Weight: 25},
				},
			},
			Data: types.DataConfig{
				FieldsCount:  10,
				DocumentSize: "1KB",
				SeedData:     0,
				Cleanup:      true,
			},
			Output: types.OutputConfig{
				Format:  "json",
				Console: true,
			},
			BaseDir: "configs", // Set base directory for default config
		}
	}

	// Override with command-line flags
	if target != "" {
		cfg.Target = target
	}
	if duration != "" {
		d, err := time.ParseDuration(duration)
		if err != nil {
			return fmt.Errorf("invalid duration: %w", err)
		}
		cfg.Duration = d
	}
	if workers > 0 {
		cfg.Workers = workers
	}

	// Always auto-generate token
	fmt.Println("Generating authentication token...")
	// Use the resolved private key path from config
	cfg.Auth.Token, err = utils.GenerateBenchmarkToken(cfg.Auth.PrivateKeyFile, "benchmark", 365*24*time.Hour)
	if err != nil {
		return fmt.Errorf("failed to generate token: %w", err)
	}
	fmt.Println("✓ Token generated successfully")

	// Validate configuration
	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// Create scenario
	var benchScenario types.Scenario
	switch cfg.Scenario.Type {
	case "crud", "":
		benchScenario, err = scenario.NewCRUDScenario(cfg)
		if err != nil {
			return fmt.Errorf("failed to create CRUD scenario: %w", err)
		}
	default:
		return fmt.Errorf("unsupported scenario type: %s", cfg.Scenario.Type)
	}

	// Create metrics collector
	collector := metrics.NewCollector()

	// Create runner
	benchRunner := runner.NewBasicRunner()

	// Initialize runner first (before setting scenario and metrics)
	ctx := context.Background()
	if err := benchRunner.Initialize(ctx, cfg); err != nil {
		return fmt.Errorf("failed to initialize runner: %w", err)
	}
	defer benchRunner.Cleanup(ctx)

	// Set scenario and metrics collector after initialization
	benchRunner.SetScenario(benchScenario)
	benchRunner.SetMetricsCollector(collector)

	// Create reporter
	rep := reporter.NewConsoleReporter(os.Stdout, !noColor)

	fmt.Println("Starting benchmark...")
	fmt.Printf("Target: %s\n", cfg.Target)
	fmt.Printf("Duration: %s\n", cfg.Duration)
	fmt.Printf("Workers: %d\n", cfg.Workers)
	fmt.Printf("Scenario: %s\n", cfg.Scenario.Type)
	fmt.Println()

	// Run benchmark with progress reporting
	runCtx, cancel := context.WithTimeout(ctx, cfg.Duration+10*time.Second)
	defer cancel()

	// Start progress ticker
	progressTicker := time.NewTicker(1 * time.Second)
	defer progressTicker.Stop()

	startTime := time.Now()

	// Run in goroutine
	resultChan := make(chan *types.Result, 1)
	errChan := make(chan error, 1)

	go func() {
		result, err := benchRunner.Run(runCtx)
		if err != nil {
			errChan <- err
			return
		}
		resultChan <- result
	}()

	// Progress loop
	running := true
	for running {
		select {
		case result := <-resultChan:
			running = false
			elapsed := time.Since(startTime)
			rep.ReportProgress(elapsed, collector.GetSnapshot())
			fmt.Println() // New line after progress

			if err := rep.ReportSummary(result); err != nil {
				return fmt.Errorf("failed to report summary: %w", err)
			}
			return nil

		case err := <-errChan:
			running = false
			return err

		case <-progressTicker.C:
			elapsed := time.Since(startTime)
			rep.ReportProgress(elapsed, collector.GetSnapshot())

		case <-runCtx.Done():
			running = false
			return fmt.Errorf("benchmark timeout")
		}
	}

	return nil
}

func printUsage() {
	fmt.Println("Syntrix Benchmark Tool")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  syntrix-benchmark <command> [flags]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  run       Run a benchmark")
	fmt.Println("  version   Show version information")
	fmt.Println("  help      Show this help message")
	fmt.Println()
	fmt.Println("Run 'syntrix-benchmark <command> --help' for more information about a command.")
}

func printRunUsage() {
	fmt.Println("Run a benchmark")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  syntrix-benchmark run [flags]")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  -c, --config <file>     Configuration file (YAML)")
	fmt.Println("  -t, --target <url>      Target Syntrix URL")
	fmt.Println("  -d, --duration <time>   Benchmark duration (e.g., 10s, 1m)")
	fmt.Println("  -w, --workers <n>       Number of concurrent workers")
	fmt.Println("      --no-color          Disable colored output")
	fmt.Println("  -h, --help              Show this help message")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  syntrix-benchmark run --config configs/benchmark.yaml")
	fmt.Println("  syntrix-benchmark run --target http://localhost:8080 --duration 30s --workers 10")
	fmt.Println()
	fmt.Println("Note: Authentication tokens are generated automatically")
}
