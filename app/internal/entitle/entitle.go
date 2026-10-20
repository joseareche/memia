// Package entitle resolves which platform modules a user (by verified email) may use.
// Source of truth: platform_users + user_modules (migration 013), managed from the VPN-only admin panel.
package entitle

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	Chat      = "chat"
	Notes     = "notes"
	Cotizador = "cotizador"
)

type Ent struct {
	Email      string            `json:"email"`
	Known      bool              `json:"known"`
	Enabled    bool              `json:"enabled"`
	Superadmin bool              `json:"superadmin"`
	TenantID   string            `json:"tenant_id,omitempty"`
	Modules    map[string]string `json:"modules"` // enabled module -> role ("" when the module has no roles)
}

// Has: user enabled and module enabled (superadmin has everything).
func (e Ent) Has(m string) bool {
	if !e.Enabled {
		return false
	}
	if e.Superadmin {
		return true
	}
	_, ok := e.Modules[m]
	return ok
}

// Role within a module; superadmin is admin of role-based modules.
func (e Ent) Role(m string) string {
	if e.Superadmin && m == Cotizador {
		return "admin"
	}
	return e.Modules[m]
}

type Q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Load reads one user's entitlements (no cache).
func Load(ctx context.Context, q Q, email string) (Ent, error) {
	e := Ent{Email: email, Modules: map[string]string{}}
	var tid *string
	err := q.QueryRow(ctx, `SELECT enabled, superadmin, tenant_id::text FROM platform_users WHERE email=$1`, email).Scan(&e.Enabled, &e.Superadmin, &tid)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, nil
	}
	if err != nil {
		return e, err
	}
	e.Known = true
	if tid != nil {
		e.TenantID = *tid
	}
	rows, err := q.Query(ctx, `SELECT m.key, COALESCE(um.role, m.default_role, '') FROM platform_modules m
		LEFT JOIN user_modules um ON um.module=m.key AND um.email=$1
		WHERE $2 OR COALESCE(um.enabled,false) ORDER BY m.sort`, email, e.Superadmin)
	if err != nil {
		return e, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, r string
		if err := rows.Scan(&k, &r); err != nil {
			return e, err
		}
		e.Modules[k] = r
	}
	if !e.Enabled {
		e.Modules = map[string]string{}
	}
	return e, rows.Err()
}

// Cache: short TTL so admin changes apply within seconds; Invalidate on writes in-process.
type Cache struct {
	DB  *pgxpool.Pool
	TTL time.Duration
	mu  sync.Mutex
	m   map[string]cached
}
type cached struct {
	e  Ent
	at time.Time
}

func (c *Cache) Get(ctx context.Context, email string) (Ent, error) {
	ttl := c.TTL
	if ttl == 0 {
		ttl = 10 * time.Second
	}
	c.mu.Lock()
	if c.m == nil {
		c.m = map[string]cached{}
	}
	if x, ok := c.m[email]; ok && time.Since(x.at) < ttl {
		c.mu.Unlock()
		return x.e, nil
	}
	c.mu.Unlock()
	e, err := Load(ctx, c.DB, email)
	if err != nil {
		return e, err
	}
	c.mu.Lock()
	c.m[email] = cached{e, time.Now()}
	c.mu.Unlock()
	return e, nil
}

func (c *Cache) Invalidate(email string) {
	c.mu.Lock()
	delete(c.m, email)
	c.mu.Unlock()
}
