package database

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/codetreker/syntrix/console-server/internal/database/config"
	_ "github.com/lib/pq"
)

func Open(ctx context.Context, cfg config.Config) (*sql.DB, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	db, err := sql.Open("postgres", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("open Console PostgreSQL: %w", err)
	}
	db.SetMaxOpenConns(cfg.MaxOpenConnections)
	db.SetMaxIdleConns(cfg.MaxOpenConnections)
	connectCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := db.PingContext(connectCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect Console PostgreSQL: %w", err)
	}
	return db, nil
}
