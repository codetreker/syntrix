package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/codetreker/syntrix/console-server/internal/auth"
	"github.com/codetreker/syntrix/console-server/internal/config"
	"github.com/codetreker/syntrix/console-server/internal/database"
	"github.com/codetreker/syntrix/console-server/internal/provisioning"
	"github.com/codetreker/syntrix/console-server/internal/server"
	"github.com/codetreker/syntrix/console-server/internal/workload"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := command(ctx, os.Args[1:], logger); err != nil {
		os.Exit(1)
	}
}

func command(ctx context.Context, args []string, logger *slog.Logger) error {
	flags := flag.NewFlagSet("console-server", flag.ContinueOnError)
	path := flags.String("config", "configs/config.yml", "Console configuration file")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("console-server accepts only flags")
	}
	if err := run(ctx, *path, logger); err != nil {
		var failure *stageError
		phase, reason := "runtime", "runtime_failed"
		if errors.As(err, &failure) {
			phase, reason = failure.phase, failure.reason
		}
		logger.Error("Console stopped", "phase", phase, "reason", reason, "error_category", "runtime", "error_type", fmt.Sprintf("%T", err))
		return err
	}
	return nil
}

type stageError struct {
	phase, reason string
	cause         error
}

func (e *stageError) Error() string { return e.phase + ": " + e.reason }
func (e *stageError) Unwrap() error { return e.cause }
func stage(phase, reason string, err error) error {
	return &stageError{phase: phase, reason: reason, cause: err}
}

func run(ctx context.Context, path string, logger *slog.Logger) error {
	cfg, err := config.Load(path)
	if err != nil {
		return stage("configuration", "configuration_invalid", err)
	}
	db, err := database.Open(ctx, cfg.Database)
	if err != nil {
		return stage("postgres_connect", "postgres_connection_failed", err)
	}
	defer db.Close()
	accounts, err := auth.NewService(db, cfg.Auth)
	if err != nil {
		return stage("auth_initialize", "auth_configuration_invalid", err)
	}
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := accounts.Initialize(initCtx); err != nil {
		return stage("auth_bootstrap", "account_initialization_failed", err)
	}
	executor, err := provisioning.New(cfg.Provisioning)
	if err != nil {
		return stage("kubernetes_client", "kubernetes_configuration_failed", err)
	}
	workloads, err := workload.New(db, executor, cfg.Workload, workload.Target{Namespace: cfg.Provisioning.Namespace, InstallationID: cfg.Provisioning.InstallationID})
	if err != nil {
		return stage("workload_initialize", "workload_configuration_invalid", err)
	}
	if err := workloads.Initialize(initCtx); err != nil {
		return stage("workload_schema", "schema_initialization_failed", err)
	}
	templates := make([]string, 0, len(cfg.Workload.Templates))
	for _, template := range cfg.Workload.Templates {
		templates = append(templates, template.ID)
	}
	api, err := server.New(cfg.Server, accounts, workloads, templates, logger, db.PingContext)
	if err != nil {
		return stage("http_initialize", "http_configuration_invalid", err)
	}
	httpServer := &http.Server{Addr: cfg.Server.Address, Handler: api.Handler(), ReadHeaderTimeout: cfg.Server.ReadTimeout, ReadTimeout: cfg.Server.ReadTimeout, WriteTimeout: cfg.Server.WriteTimeout, IdleTimeout: 60 * time.Second}
	workCtx, stop := context.WithCancel(ctx)
	defer stop()
	workerDone := make(chan error, 1)
	go func() { workerDone <- workloads.Run(workCtx) }()
	httpDone := make(chan error, 1)
	go func() { httpDone <- httpServer.ListenAndServe() }()
	logger.Info("Console started", "address", cfg.Server.Address, "namespace", cfg.Provisioning.Namespace, "installation_id", cfg.Provisioning.InstallationID)
	var result error
	workerFinished, httpFinished := false, false
	select {
	case <-ctx.Done():
	case err := <-workerDone:
		workerFinished = true
		if !errors.Is(err, context.Canceled) {
			result = stage("reconciler", "reconciler_failed", err)
		}
	case err := <-httpDone:
		httpFinished = true
		if !errors.Is(err, http.ErrServerClosed) {
			result = stage("http_listen", "http_listener_failed", err)
		}
	}
	stop()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		result = errors.Join(result, err, httpServer.Close())
	}
	if !httpFinished {
		select {
		case err := <-httpDone:
			if !errors.Is(err, http.ErrServerClosed) {
				result = errors.Join(result, err)
			}
		case <-shutdownCtx.Done():
			result = errors.Join(result, shutdownCtx.Err())
		}
	}
	if !workerFinished {
		select {
		case err := <-workerDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				result = errors.Join(result, err)
			}
		case <-shutdownCtx.Done():
			result = errors.Join(result, shutdownCtx.Err())
		}
	}
	return result
}
