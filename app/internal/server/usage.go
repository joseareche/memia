package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"devbrain/internal/embedsvc"
	"devbrain/internal/provider"
	"devbrain/internal/redact"
	"devbrain/internal/store"
	"devbrain/internal/tenant"

	"github.com/jackc/pgx/v5"
)

// usageEvent is one provider call (or refusal). Never contains content, prompts or secrets.
type usageEvent struct {
	Op, Provider, Model, Status, ErrKind string
	Items, Hits, Misses, In, Out, Saved  int
	Latency                              time.Duration
	Redactions                           redact.Stats
}

func errKind(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, provider.ErrPolicy), errors.Is(err, errPolicyExplicit):
		return "policy_local_only"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	case strings.Contains(err.Error(), "provider HTTP 429"):
		return "provider_rate_limit"
	case strings.Contains(err.Error(), "provider"):
		return "provider_error"
	}
	return "internal"
}

// record writes the event in its own tenant-scoped transaction (survives request tx rollback).
func (a *api) record(ctx context.Context, e usageEvent) {
	ctx = context.WithoutCancel(ctx)
	if e.Status == "" {
		e.Status = "ok"
	}
	if e.Redactions == nil {
		e.Redactions = redact.Stats{}
	}
	red, _ := json.Marshal(e.Redactions)
	cost := provider.Cost(a.d.Prices, e.Provider, e.Model, e.In, e.Out)
	saved := provider.Cost(a.d.Prices, e.Provider, e.Model, e.Saved, 0)
	err := tenant.WithTx(ctx, a.d.DB, func(tx pgx.Tx) error {
		info, _ := tenant.From(ctx)
		_, err := tx.Exec(ctx, `INSERT INTO usage_events (tenant_id, operation, provider, model, status, error_kind, items, cache_hits,
			cache_misses, input_tokens, output_tokens, saved_tokens_est, latency_ms, cost_usd, saved_usd, redactions)
			VALUES ($1::uuid,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16::jsonb)`,
			info.TenantID, e.Op, e.Provider, e.Model, e.Status, e.ErrKind, e.Items, e.Hits, e.Misses, e.In, e.Out, e.Saved,
			e.Latency.Milliseconds(), cost, saved, string(red))
		return err
	})
	if err != nil {
		a.d.Log.Warn("usage record", "err", err)
	}
}

func statusOf(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errKind(err) == "policy_local_only":
		return "policy_refused"
	}
	return "error"
}

// embed = svc.Embed + usage recording.
func (a *api) embed(r *http.Request, tx pgx.Tx, op, tenantID string, pol provider.Policy, idx *store.Index, texts []string) (*embedsvc.Result, error) {
	ctx, m := provider.WithMeter(r.Context())
	t0 := time.Now()
	res, err := a.svc.Embed(ctx, tx, tenantID, pol, idx, texts)
	e := usageEvent{Op: op, Provider: idx.Provider, Model: idx.Model, Items: len(texts), In: m.Input,
		Latency: time.Since(t0), Status: statusOf(err), ErrKind: errKind(err)}
	if res != nil {
		e.Hits, e.Misses, e.Saved, e.Redactions = res.CacheHits, res.Computed, res.HitTokensEst, res.Redactions
	}
	a.record(r.Context(), e)
	return res, err
}

// GET /admin/api/stats and GET /v1/admin/stats (bearer token with "admin" scope).
func (a *api) stats(w http.ResponseWriter, r *http.Request) {
	var raw []byte
	if err := a.d.DB.QueryRow(r.Context(), "SELECT admin_stats()::text").Scan(&raw); err != nil {
		a.fail(w, "stats", err)
		return
	}
	prices := map[string]provider.Price{}
	for k, v := range a.d.Prices {
		prices[k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"stats": json.RawMessage(raw), "prices_per_1m_usd": prices,
		"prices_note": "estimates, configurable via DEVBRAIN_PRICES; savings use len/4 token estimate"})
}

// GET /admin/api/stats2?period=today|7d|30d&tenant=&project= (dashboard v2; aggregates only, no content).
func (a *api) statsV2(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	period := q.Get("period")
	if period != "today" && period != "7d" {
		period = "30d"
	}
	clip := func(s string) string {
		if len(s) > 200 {
			return s[:200]
		}
		return s
	}
	var raw []byte
	if err := a.d.DB.QueryRow(r.Context(), "SELECT admin_stats_v2($1,$2,$3)::text", period, clip(q.Get("tenant")), clip(q.Get("project"))).Scan(&raw); err != nil {
		a.fail(w, "stats2", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"stats": json.RawMessage(raw), "prices_per_1m_usd": a.d.Prices,
		"prices_note": "estimaciones configurables (DEVBRAIN_PRICES); el ahorro usa una estimación de tokens = caracteres/4"})
}

func hasScope(r *http.Request, s string) bool {
	info, _ := tenant.From(r.Context())
	for _, x := range info.Scopes {
		if x == s {
			return true
		}
	}
	return false
}
