// Command devbrain is the DevBrain MVP HTTP server (private, behind Caddy TLS).
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"devbrain/internal/admin"
	"devbrain/internal/auth"
	"devbrain/internal/commercial"
	"devbrain/internal/entitle"
	"devbrain/internal/db"
	"devbrain/internal/embedsvc"
	"devbrain/internal/mcp"
	"devbrain/internal/metrics"
	"devbrain/internal/provider"
	"devbrain/internal/server"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Set via -ldflags at build time.
var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(os.Args) > 1 && os.Args[1] == "hash-password" {
		// Reads the password from stdin (never argv) and prints only its PBKDF2 hash.
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 1024))
		pw := strings.TrimRight(string(b), "\r\n")
		if err != nil || len(pw) < 12 {
			fmt.Fprintln(os.Stderr, "password via stdin, min 12 chars")
			os.Exit(1)
		}
		h, err := server.HashPassword(pw)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(h)
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		router := buildRouter(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
		os.Exit(runAdmin(ctx, router, os.Args[2:]))
	}
	pool, err := db.Open(ctx, env("DEVBRAIN_DSN", "host=/var/run/postgresql dbname=devbrain user=devbrain_app"))
	if err != nil {
		log.Error("db open", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	router := buildRouter(log)
	prices := parsePrices(log, os.Getenv("DEVBRAIN_PRICES"))
	adm, err := server.NewAdminAuth(os.Getenv("DEVBRAIN_ADMIN_ALLOW"), os.Getenv("DEVBRAIN_ADMIN_PASSWORD_HASH"))
	if err != nil {
		log.Info("admin web disabled", "reason", err.Error())
		adm = nil
	}

	h := server.New(server.Deps{
		Log:          log,
		DB:           pool,
		Metrics:      metrics.New(),
		Auth:         auth.New(pool),
		MCP:          mcp.New(version, splitCSV(env("DEVBRAIN_ALLOWED_ORIGINS", ""))),
		Providers:    router,
		Version:      server.VersionInfo{Version: version, Commit: commit, BuildTime: buildTime},
		ChatProvider: env("DEVBRAIN_CHAT_PROVIDER", "openai"),
		ChatModel:    env("DEVBRAIN_CHAT_MODEL", "gpt-4o-mini"),
		Prices:       prices,
		Admin:        adm,
		Commercial:   commercialAPI(log, pool),
	})
	hs := &http.Server{Addr: env("DEVBRAIN_LISTEN", "127.0.0.1:8080"), Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(sc)
	}()
	log.Info("listening", "addr", hs.Addr, "version", version, "commit", commit)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
	log.Info("stopped")
}

// buildRouter registers only providers that are configured. Keys come from the
// environment (root-only EnvironmentFile) and are never logged.
func buildRouter(log *slog.Logger) *provider.Router {
	r := provider.NewRouter()
	if k := os.Getenv("OPENAI_API_KEY"); k != "" {
		o := provider.NewOpenAI(k)
		r.Register(o)
		r.RegisterChat(o)
	}
	if k := os.Getenv("GEMINI_API_KEY"); k != "" {
		r.Register(provider.NewGemini(k))
	}
	if u := os.Getenv("DEVBRAIN_OLLAMA_URL"); u != "" {
		r.Register(provider.NewOllama(u))
	}
	log.Info("providers", "embedders", r.Names())
	return r
}

func runAdmin(ctx context.Context, router *provider.Router, args []string) int {
	pool, err := db.Open(ctx, env("DEVBRAIN_ADMIN_DSN", "host=/var/run/postgresql dbname=devbrain user=postgres"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer pool.Close()
	// Admin runs as table owner (BYPASSRLS) to operate across tenants.
	if _, err := pool.Exec(ctx, "SELECT 1"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	a := &admin.Admin{DB: pool, Svc: &embedsvc.Service{Router: router}, Out: os.Stdout}
	if err := a.Run(ctx, args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// parsePrices: DEVBRAIN_PRICES="provider/model=in[:out],..." in USD per 1M tokens, overriding DefaultPrices.
func parsePrices(log *slog.Logger, s string) map[string]provider.Price {
	p := map[string]provider.Price{}
	for k, v := range provider.DefaultPrices {
		p[k] = v
	}
	for _, kv := range splitCSV(s) {
		k, v, ok := strings.Cut(kv, "=")
		in, out, _ := strings.Cut(v, ":")
		fi, err1 := strconv.ParseFloat(in, 64)
		fo, err2 := strconv.ParseFloat(out, 64)
		if out == "" {
			fo, err2 = 0, nil
		}
		if !ok || err1 != nil || err2 != nil {
			log.Warn("bad DEVBRAIN_PRICES entry", "entry", k)
			continue
		}
		p[strings.TrimSpace(k)] = provider.Price{In: fi, Out: fo}
	}
	return p
}

// commercialAPI enables /v1/commercial/* when DEVBRAIN_ACTOR_HMAC_KEY (hex, >= 32 bytes) is set.
func commercialAPI(log *slog.Logger, pool *pgxpool.Pool) *commercial.API {
	k, err := hex.DecodeString(strings.TrimSpace(os.Getenv("DEVBRAIN_ACTOR_HMAC_KEY")))
	if err != nil || len(k) < 32 {
		log.Info("commercial module disabled", "reason", "DEVBRAIN_ACTOR_HMAC_KEY missing or short")
		return nil
	}
	log.Info("commercial module enabled")
	api := &commercial.API{DB: pool, Key: k, Log: log, Ent: &entitle.Cache{DB: pool}}
	if bin := env("DEVBRAIN_SOFFICE", "/usr/bin/soffice"); bin != "off" {
		if _, err := os.Stat(bin); err == nil {
			api.PDF = commercial.SofficePDF(bin, 90*time.Second)
			log.Info("commercial pdf export enabled", "converter", "libreoffice")
		}
	}
	return api
}
