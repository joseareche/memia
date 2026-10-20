// Package tenant carries the authenticated tenant in the request context and
// runs queries inside a transaction with app.tenant_id set, so PostgreSQL RLS applies.
package tenant

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Info struct {
	TenantID string   `json:"tenant_id"`
	UserID   string   `json:"user_id"`
	Scopes   []string `json:"scopes"`
}

type ctxKey struct{}

var ErrNoTenant = errors.New("no tenant in context")

func With(ctx context.Context, i Info) context.Context { return context.WithValue(ctx, ctxKey{}, i) }

func From(ctx context.Context) (Info, bool) {
	i, ok := ctx.Value(ctxKey{}).(Info)
	return i, ok && i.TenantID != ""
}

// WithTx runs fn in a transaction scoped to the context's tenant (SET LOCAL semantics).
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	i, ok := From(ctx)
	if !ok {
		return ErrNoTenant
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", i.TenantID); err != nil {
			return err
		}
		return fn(tx)
	})
}
