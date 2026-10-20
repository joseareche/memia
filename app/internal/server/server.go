// Package server wires HTTP routes.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"devbrain/internal/auth"
	"devbrain/internal/commercial"
	"devbrain/internal/embedsvc"
	"devbrain/internal/mcp"
	"devbrain/internal/metrics"
	"devbrain/internal/provider"
	"devbrain/internal/tenant"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type VersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
}

type Deps struct {
	Log          *slog.Logger
	DB           *pgxpool.Pool
	Metrics      *metrics.Registry
	Auth         *auth.Authenticator
	MCP          *mcp.Handler
	Providers    *provider.Router
	Version      VersionInfo
	ChatProvider string
	ChatModel    string
	Prices       map[string]provider.Price
	Admin        *AdminAuth
	Commercial   *commercial.API
}

type api struct {
	d   Deps
	svc *embedsvc.Service
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func New(d Deps) http.Handler {
	mux := http.NewServeMux()
	route := func(p string, h http.Handler) { mux.Handle(p, d.Metrics.Wrap(p, h)) }

	route("GET /healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok"})
	}))
	route("GET /readyz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := d.DB.Ping(ctx); err != nil {
			d.Log.Warn("readyz db ping failed", "err", err)
			writeJSON(w, 503, map[string]string{"status": "db unavailable"})
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
	}))
	route("GET /version", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, d.Version)
	}))
	route("GET /metrics", d.Metrics.Handler())

	// Authenticated: returns the caller's tenant and RLS-scoped counts.
	route("GET /v1/whoami", d.Auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info, _ := tenant.From(r.Context())
		var docs, projects int64
		err := tenant.WithTx(r.Context(), d.DB, func(tx pgx.Tx) error {
			return tx.QueryRow(r.Context(), "SELECT (SELECT count(*) FROM documents), (SELECT count(*) FROM projects)").Scan(&docs, &projects)
		})
		if err != nil {
			d.Log.Error("whoami", "err", err)
			writeJSON(w, 500, map[string]string{"error": "internal"})
			return
		}
		writeJSON(w, 200, map[string]any{"tenant": info, "visible_documents": docs, "visible_projects": projects, "providers": d.Providers.Names()})
	})))
	d.MCP.Register(MCPTools(d)...)
	route("/mcp", d.Auth.Middleware(d.MCP))
	a := &api{d: d, svc: &embedsvc.Service{Router: d.Providers}}
	route("POST /v1/documents", d.Auth.Middleware(http.HandlerFunc(a.ingest)))
	route("POST /v1/search", d.Auth.Middleware(http.HandlerFunc(a.searchHandler)))
	route("POST /v1/answer", d.Auth.Middleware(http.HandlerFunc(a.answer)))
	route("GET /v1/projects", d.Auth.Middleware(http.HandlerFunc(a.projects)))
	route("POST /v1/pending/extract", d.Auth.Middleware(http.HandlerFunc(a.pendingExtract)))
	route("GET /v1/pending", d.Auth.Middleware(http.HandlerFunc(a.pendingHandler)))
	route("POST /v1/distill", d.Auth.Middleware(http.HandlerFunc(a.distill)))
	route("GET /v1/admin/stats", d.Auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hasScope(r, "admin") {
			writeJSON(w, 403, map[string]string{"error": "admin scope required"})
			return
		}
		a.stats(w, r)
	})))
	if d.Commercial != nil {
		d.Commercial.Mount(route, d.Auth.Middleware)
	}
	if d.Admin != nil {
		d.Admin.mount(route, a)
	}
	// TODO(api): list/delete documents, MCP tools wrapping ingest/search/answer.
	return mux
}

func newSvc(d Deps) *embedsvc.Service { return &embedsvc.Service{Router: d.Providers} }
