package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/syntrixbase/syntrix/internal/config"
	"github.com/syntrixbase/syntrix/internal/logging"
	"github.com/syntrixbase/syntrix/internal/services"
	services_config "github.com/syntrixbase/syntrix/internal/services/config"
)

func main() {
	// 0. Parse Command Line Flags
	configDir := flag.String("config-dir", "", "Configuration directory (default: configs, or SYNTRIX_CONFIG_DIR env var)")
	runAPI := flag.Bool("api", false, "Run API Gateway (REST + Realtime)")
	runQuery := flag.Bool("query", false, "Run Query Service")
	runTriggerEvaluator := flag.Bool("trigger-evaluator", false, "Run Trigger Evaluator Service")
	runTriggerWorker := flag.Bool("trigger-worker", false, "Run Trigger Worker Service")
	runIndexer := flag.Bool("indexer", false, "Run Indexer Service")
	runPuller := flag.Bool("puller", false, "Run Change Stream Puller Service")
	runStreamer := flag.Bool("streamer", false, "Run Streamer Service")
	runAll := flag.Bool("all", false, "Run All Services")
	standalone := flag.Bool("standalone", false, "Run in standalone mode (single process, no inter-service HTTP)")
	bootstrapIndexes := flag.Bool("bootstrap-indexes", false, "Rebuild indexes during maintenance, verify subscription readiness, then keep services running")
	writesQuiesced := flag.Bool("writes-quiesced", false, "Attest all external writers, including HTTP/gRPC clients and MongoDB tombstone TTL deletion, remain paused until bootstrap readiness")
	resetDerived := flag.Bool("reset-derived", false, "Archive configured index and Puller buffer directories before bootstrap; stop other processes using them")
	bootstrapTimeout := flag.Duration("bootstrap-timeout", 30*time.Minute, "Maximum duration for index bootstrap and subscription readiness")
	flag.Parse()

	// 1. Load Configuration early to check deployment mode from config
	cfg := config.LoadConfigFrom(*configDir)

	// Initialize logging (before any other services)
	if err := logging.Initialize(cfg.Logging); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logging: %v\n", err)
		os.Exit(1)
	}
	defer logging.Shutdown()

	slog.Info("Configuration loaded", "config_dir", cfg.ConfigDir)

	// Determine deployment mode: CLI flag takes precedence over config file
	mode := cfg.Deployment.Mode
	if *standalone {
		mode = services_config.ModeStandalone
	}

	// Standalone mode: all services in-process
	if mode.IsStandalone() {
		slog.Info("Starting Syntrix in Standalone Mode...")
		slog.Info("- All services running in-process")
		opts := services.Options{
			Mode:             mode,
			RunAPI:           true,
			BootstrapIndexes: *bootstrapIndexes,
			WritesQuiesced:   *writesQuiesced,
			ResetDerived:     *resetDerived,
			BootstrapTimeout: *bootstrapTimeout,
		}
		runServer(cfg, opts)
		return
	}

	// Default to running all if no specific flags are provided or if --all is set
	if *runAll || (!*runAPI && !*runQuery && !*runTriggerEvaluator && !*runTriggerWorker && !*runPuller && !*runIndexer && !*runStreamer) {
		*runAPI = true
		*runQuery = true
		*runTriggerEvaluator = true
		*runTriggerWorker = true
		*runPuller = true
		*runIndexer = true
		*runStreamer = true
	}

	slog.Info("Starting Syntrix Services...")
	if *runAPI {
		slog.Info("- API Gateway (REST + Realtime): Enabled")
	}
	if *runQuery {
		slog.Info("- Query Service: Enabled")
	}
	if *runTriggerEvaluator {
		slog.Info("- Trigger Evaluator Service: Enabled")
	}
	if *runTriggerWorker {
		slog.Info("- Trigger Worker Service: Enabled")
	}
	if *runPuller {
		slog.Info("- Change Stream Puller Service: Enabled")
	}
	if *runIndexer {
		slog.Info("- Indexer Service: Enabled")
	}

	// 2. Initialize Service Manager
	opts := services.Options{
		RunAPI:              *runAPI,
		RunQuery:            *runQuery,
		RunTriggerEvaluator: *runTriggerEvaluator,
		RunTriggerWorker:    *runTriggerWorker,
		RunPuller:           *runPuller,
		RunIndexer:          *runIndexer,
		RunStreamer:         *runStreamer,
		Mode:                mode,
		BootstrapIndexes:    *bootstrapIndexes,
		WritesQuiesced:      *writesQuiesced,
		ResetDerived:        *resetDerived,
		BootstrapTimeout:    *bootstrapTimeout,
	}
	runServer(cfg, opts)
}

// runServer starts the service manager with the given configuration and options.
func runServer(cfg *config.Config, opts services.Options) {
	if err := serve(cfg, opts); err != nil {
		logging.Fatal("Service startup failed", "error", err)
	}
}

func serve(cfg *config.Config, opts services.Options) error {
	mgr := services.NewManager(cfg, opts)
	if err := mgr.PrepareIndexBootstrap(); err != nil {
		return err
	}

	bgCtx, bgCancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer bgCancel()
	defer func() {
		bgCancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		mgr.Shutdown(shutdownCtx)
	}()

	ctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
	defer cancel()

	if err := mgr.Init(ctx); err != nil {
		return fmt.Errorf("initialize services: %w", err)
	}

	if opts.BootstrapIndexes {
		slog.Info("Index maintenance active; keep all external writers and MongoDB tombstone TTL deletion paused until readiness is confirmed")
		bootstrapCtx, bootstrapCancel := context.WithTimeout(bgCtx, opts.BootstrapTimeout)
		err := mgr.BootstrapIndexes(bgCtx, bootstrapCtx)
		bootstrapCancel()
		if err != nil {
			return fmt.Errorf("index maintenance failed; keep writes paused: %w", err)
		}
	}

	mgr.Start(bgCtx)
	if opts.BootstrapIndexes {
		slog.Info("Index bootstrap and subscription application are ready; external writers and MongoDB tombstone TTL deletion may resume")
	}

	<-bgCtx.Done()
	slog.Info("Shutting down services...")
	return nil
}
