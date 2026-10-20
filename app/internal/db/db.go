// Package db opens the PostgreSQL pool. Auth is peer over the local unix socket (no password).
package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 10
	if cfg.ConnConfig.User == "postgres" { // admin CLI: act as owner, never as superuser
		cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
			_, err := c.Exec(ctx, "SET ROLE devbrain_owner")
			return err
		}
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}
