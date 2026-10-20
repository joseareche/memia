// devbrain-chat: authenticated /chat for chat.example.com. Runs on the web host at
// 127.0.0.1:8093 behind nginx. Server-side proxy to DevBrain (private HTTPS endpoint,
// pinned internal CA). The browser never sees the DevBrain URL, token, DB or keys.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

//go:embed static
var static embed.FS

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// cred reads a systemd credential ($CREDENTIALS_DIRECTORY/name).
func cred(name string) string {
	b, err := os.ReadFile(filepath.Join(os.Getenv("CREDENTIALS_DIRECTORY"), name))
	if err != nil {
		slog.Error("missing credential", "name", name)
		os.Exit(1)
	}
	return strings.TrimSpace(string(b))
}

type session struct {
	created, seen time.Time
	email         string // actor identity forwarded (HMAC-signed) to the commercial API
}

type app struct {
	ents     entCache
	user     string
	hash     []byte
	dummy    []byte
	token    string
	upstream string
	origin   string
	hc       *http.Client
	pub      *http.Client // public internet (Google)
	oidc     *oidcConfig
	log      *slog.Logger
	hist     *histStore
	com      *commercialProxy

	mu       sync.Mutex
	sessions map[string]*session
	fails    map[string][]time.Time
	global   []time.Time
}

const (
	cookieName  = "__Host-dbchat"
	sessAbs     = 12 * time.Hour
	sessIdle    = 2 * time.Hour
	failWindow  = 15 * time.Minute
	failPerIP   = 5
	failGlobal  = 30
	maxAPIBody  = 256 << 10
	upstreamTTL = 90 * time.Second
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "hash" { // reads password from stdin, prints bcrypt hash
		pw, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		h, err := bcrypt.GenerateFromPassword([]byte(strings.TrimRight(pw, "\r\n")), 12)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(string(h))
		return
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ca, err := os.ReadFile(env("DEVBRAIN_CA_FILE", "/etc/devbrain-chat/devbrain-ca.crt"))
	pool := x509.NewCertPool()
	if err != nil || !pool.AppendCertsFromPEM(ca) {
		log.Error("cannot load DevBrain CA")
		os.Exit(1)
	}
	dummy, _ := bcrypt.GenerateFromPassword([]byte("dummy-password-for-timing"), 12)
	a := &app{
		user: env("CHAT_USER", "default"), hash: []byte(cred("admin_password_hash")), dummy: dummy,
		token: cred("devbrain_token"), upstream: env("DEVBRAIN_URL", "https://memia.internal.example"),
		origin: env("CHAT_ORIGIN", "https://chat.example.com"), log: log,
		hc: &http.Client{Timeout: upstreamTTL, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: env("DEVBRAIN_SERVER_NAME", "memia.internal.example"), MinVersion: tls.VersionTLS12}}},
		sessions: map[string]*session{}, fails: map[string][]time.Time{},
		pub: &http.Client{Timeout: 15 * time.Second},
	}
	if id, sec := os.Getenv("GOOGLE_CLIENT_ID"), os.Getenv("GOOGLE_CLIENT_SECRET"); id != "" && sec != "" {
		allowed := map[string]bool{}
		for _, e := range strings.Split(env("CHAT_ALLOWED_EMAILS", ""), ",") {
			if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
				allowed[e] = true
			}
		}
		a.oidc = &oidcConfig{clientID: id, clientSecret: sec, redirect: env("GOOGLE_REDIRECT_URL", "https://chat.example.com/chat/auth/google/callback"),
			allowed: allowed, tokenURL: googleTokenURL, pend: map[string]pending{},
			keys: &jwks{url: googleJWKSURL, hc: a.pub}}
		a.oidc.allow = func(ctx context.Context, email string) bool {
			e, err := a.fetchEnt(ctx, email)
			return err == nil && e.has(modChat)
		}
		log.Info("google login enabled", "breakglass_count", len(allowed), "managed_users", true)
	}
	if dir := env("STATE_DIRECTORY", ""); dir != "" {
		if h, err := openHistory(dir); err != nil {
			log.Error("history disabled", "err", err)
		} else {
			a.hist = h
			log.Info("history enabled")
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /chat/api/conversations", a.convList)
	mux.HandleFunc("POST /chat/api/conversations", a.convCreate)
	mux.HandleFunc("GET /chat/api/conversations/{id}", a.convGet)
	mux.HandleFunc("PATCH /chat/api/conversations/{id}", a.convRename)
	mux.HandleFunc("DELETE /chat/api/conversations/{id}", a.convDelete)
	mux.HandleFunc("POST /chat/api/conversations/{id}/messages", a.convAppend)
	a.mountCommercial(mux)
	mux.HandleFunc("GET /chat/api/me", a.me)
	mux.HandleFunc("GET /chat", a.page)
	mux.HandleFunc("GET /chat/", a.page)
	mux.HandleFunc("GET /chat/static/", a.staticFile)
	mux.HandleFunc("POST /chat/login", a.login)
	mux.HandleFunc("POST /chat/logout", a.logout)
	mux.HandleFunc("GET /chat/auth/google/start", a.googleStart)
	mux.HandleFunc("GET /chat/auth/google/callback", a.googleCallback)
	// all notes/answer calls hit the owner's tenant knowledge base: require Notas + matching tenant
	mux.HandleFunc("POST /chat/api/ingest", a.needNotes(a.proxy("/v1/documents")))
	mux.HandleFunc("POST /chat/api/search", a.needNotes(a.proxy("/v1/search")))
	mux.HandleFunc("POST /chat/api/answer", a.needNotes(a.proxy("/v1/answer")))
	mux.HandleFunc("GET /chat/api/projects", a.needNotes(a.proxyGet("/v1/projects")))
	srv := &http.Server{Addr: env("CHAT_LISTEN", "127.0.0.1:8093"), Handler: secHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 120 * time.Second}
	log.Info("listening", "addr", srv.Addr, "upstream", a.upstream)
	if err := srv.ListenAndServe(); err != nil {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}

func secHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Referrer-Policy", "same-origin")
		hd.Set("Cache-Control", "no-store")
		hd.Set("X-Robots-Tag", "noindex, nofollow")
		h.ServeHTTP(w, r)
	})
}

// clientIP: nginx sets X-Real-IP to CF-Connecting-IP (Cloudflare) or $remote_addr.
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); net.ParseIP(ip) != nil {
		return ip
	}
	h, _, _ := net.SplitHostPort(r.RemoteAddr)
	return h
}

var randRead = rand.Read

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (a *app) authed(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[c.Value]
	now := time.Now()
	if !ok || now.Sub(s.created) > sessAbs || now.Sub(s.seen) > sessIdle {
		delete(a.sessions, c.Value)
		return false
	}
	s.seen = now
	return true
}

// sameOrigin: CSRF defence in addition to SameSite=Strict.
func (a *app) sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	return o == a.origin
}

func (a *app) page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/chat" && r.URL.Path != "/chat/" {
		http.NotFound(w, r)
		return
	}
	name := "static/login.html"
	var e ent
	if a.authed(r) {
		if e = a.reqEnt(r); !e.has(modChat) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>Sin acceso</title><p>Tu usuario no tiene habilitado el módulo Chat. Pide acceso al administrador.</p><form method="post" action="/chat/logout"><button>Cerrar sesión</button></form>`))
			return
		}
		name = "static/chat.html"
	}
	b, _ := static.ReadFile(name)
	if e.has(modCotizador) {
		b = a.withCommercialUI(b)
	}
	if name == "static/chat.html" {
		b = personalize(b, e)
	}
	b = versioned(b)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// assetVer: short content hash per static asset, appended as ?v= so every deploy gets new URLs
// (defeats stale browser/Cloudflare copies; versioned URLs are immutable).
var assetVer = func() map[string]string {
	m := map[string]string{}
	for name := range staticTypes {
		b, _ := static.ReadFile(name)
		h := sha256.Sum256(b)
		m[name] = hex.EncodeToString(h[:6])
	}
	return m
}()

func versioned(b []byte) []byte {
	for name, v := range assetVer {
		u := "/chat/" + name
		b = bytes.ReplaceAll(b, []byte(`"`+u+`"`), []byte(`"`+u+`?v=`+v+`"`))
	}
	return b
}

var staticTypes = map[string]string{
	"static/app.js":                "text/javascript; charset=utf-8",
	"static/app.css":               "text/css; charset=utf-8",
	"static/quotes.js":             "text/javascript; charset=utf-8",
	"static/quotes.css":            "text/css; charset=utf-8",
	"static/logo-mark.png":         "image/png",
	"static/fonts/bricolage.woff2": "font/woff2",
	"static/fonts/manrope.woff2":   "font/woff2",
}

func (a *app) staticFile(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/chat/")
	ct, ok := staticTypes[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	b, _ := static.ReadFile(name)
	w.Header().Set("Content-Type", ct)
	if v := r.URL.Query().Get("v"); v != "" && v == assetVer[name] {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	_, _ = w.Write(b)
}

func prune(ts []time.Time, now time.Time) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if now.Sub(t) < failWindow {
			out = append(out, t)
		}
	}
	return out
}

func (a *app) limited(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	a.fails[ip] = prune(a.fails[ip], now)
	a.global = prune(a.global, now)
	return len(a.fails[ip]) >= failPerIP || len(a.global) >= failGlobal
}

func (a *app) recordFail(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	a.fails[ip] = append(a.fails[ip], now)
	a.global = append(a.global, now)
}

func (a *app) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !a.sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if a.limited(ip) {
		a.log.Warn("login rate limited", "ip", ip)
		w.Header().Set("Retry-After", "900")
		http.Error(w, "demasiados intentos, espera 15 minutos", http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	u, p := r.PostFormValue("username"), r.PostFormValue("password")
	userOK := subtle.ConstantTimeCompare([]byte(u), []byte(a.user)) == 1
	h := a.hash
	if !userOK {
		h = a.dummy // equalize timing
	}
	pwOK := bcrypt.CompareHashAndPassword(h, []byte(p)) == nil
	if !userOK || !pwOK {
		a.recordFail(ip)
		a.log.Warn("login failed", "ip", ip)
		time.Sleep(400 * time.Millisecond)
		http.Redirect(w, r, "/chat?e=1", http.StatusSeeOther)
		return
	}
	a.startSession(w, env("CHAT_ACTOR_EMAIL", "admin@example.com"))
	a.log.Info("login ok", "ip", ip, "method", "password")
	http.Redirect(w, r, "/chat", http.StatusSeeOther)
}

func (a *app) startSession(w http.ResponseWriter, email string) {
	id := randHex(32)
	a.mu.Lock()
	now := time.Now()
	for k, s := range a.sessions { // housekeeping
		if now.Sub(s.created) > sessAbs || now.Sub(s.seen) > sessIdle {
			delete(a.sessions, k)
		}
	}
	a.sessions[id] = &session{created: now, seen: now, email: strings.ToLower(email)}
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: id, Path: "/", Secure: true, HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(sessAbs.Seconds())})
}

func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	if !a.sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if c, err := r.Cookie(cookieName); err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	http.Redirect(w, r, "/chat", http.StatusSeeOther)
}

// proxy forwards a JSON body to a fixed DevBrain path with the server-side token.
func (a *app) proxy(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.authed(r) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if !a.sameOrigin(r) || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAPIBody))
		if err != nil {
			http.Error(w, `{"error":"body too large"}`, http.StatusRequestEntityTooLarge)
			return
		}
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, a.upstream+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+a.token)
		resp, err := a.hc.Do(req)
		if err != nil {
			a.log.Error("upstream", "path", path, "err", err)
			http.Error(w, `{"error":"devbrain unavailable"}`, http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		a.log.Info("proxy", "path", path, "status", resp.StatusCode)
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		code, out := sanitize(path, resp.StatusCode, raw)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(out)
	}
}

func (a *app) proxyGet(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.authed(r) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, a.upstream+path, nil)
		req.Header.Set("Authorization", "Bearer "+a.token)
		resp, err := a.hc.Do(req)
		if err != nil {
			http.Error(w, `{"error":"devbrain unavailable"}`, http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		code, out := sanitize(path, resp.StatusCode, raw)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(out)
	}
}

// sanitize whitelists fields returned to the browser: no model/provider/index names,
// internal notes, ids or upstream error details ever reach the client.
func sanitize(path string, status int, raw []byte) (int, any) {
	if status/100 != 2 {
		msg := "Algo salió mal. Inténtalo de nuevo."
		switch status {
		case 400:
			msg = "La solicitud no es válida."
		case 401:
			msg = "Tu sesión no es válida."
		case 403:
			msg = "Esta acción no está permitida para tu cuenta."
		case 413:
			msg = "El texto es demasiado largo."
		case 429:
			msg = "Demasiadas solicitudes. Espera un momento."
		}
		if status >= 500 {
			status = http.StatusBadGateway
			msg = "El asistente no está disponible ahora mismo."
		}
		return status, map[string]string{"error": msg}
	}
	type src struct {
		N              int     `json:"n"`
		Title          string  `json:"title"`
		Project        string  `json:"project"`
		Date           string  `json:"date"`
		Source         string  `json:"source"`
		DocumentID     string  `json:"document_id"`
		ChunkID        string  `json:"chunk_id"`
		ConversationID string  `json:"conversation_id"`
		Excerpt        string  `json:"excerpt"`
		Score          float64 `json:"score"`
	}
	var in struct {
		Answer     string `json:"answer"`
		AnsweredBy string `json:"answered_by"`
		Sources    []src  `json:"sources"`
		Usage      struct {
			In   int     `json:"input_tokens"`
			Out  int     `json:"output_tokens"`
			Cost float64 `json:"cost_usd"`
		} `json:"usage"`
		Diagnostics struct {
			Reason     string `json:"reason"`
			TenantDocs int64  `json:"tenant_documents"`
		} `json:"diagnostics"`
		Hits []struct {
			src
			Content string `json:"content"`
			Ord     int    `json:"chunk_ord"`
		} `json:"hits"`
		Projects []struct {
			Name      string `json:"name"`
			Documents int64  `json:"documents"`
		} `json:"projects"`
		Mode       string `json:"mode"`
		Chunks     int    `json:"chunks"`
		Embedded   bool   `json:"embedded"`
		DocumentID string `json:"document_id"`
		CreatedAt  string `json:"created_at"`
		Title      string `json:"title"`
		Skipped    bool   `json:"skipped"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return http.StatusBadGateway, map[string]string{"error": "El asistente no está disponible ahora mismo."}
	}
	short := func(s string) string {
		if len(s) > 8 {
			return s[:8]
		}
		return s
	}
	ref := func(x src) string {
		if x.Source == "note" {
			return "nota " + short(x.DocumentID)
		}
		if x.Source == "pending" {
			r := "pendiente " + short(x.ChunkID)
			if x.ConversationID != "" {
				r += " · conv " + short(x.ConversationID)
			}
			return r
		}
		r := "doc " + short(x.DocumentID) + " · frag " + short(x.ChunkID)
		if x.ConversationID != "" {
			r += " · conv " + short(x.ConversationID)
		}
		return r
	}
	by := map[string]string{"memory": "memoria", "local_model": "modelo local", "external_ai": "IA externa"}
	switch path {
	case "/v1/answer":
		out := make([]map[string]any, 0, len(in.Sources))
		for _, x := range in.Sources {
			out = append(out, map[string]any{"n": x.N, "title": x.Title, "project": x.Project, "date": x.Date, "kind": x.Source, "ref": ref(x), "excerpt": x.Excerpt})
		}
		label := by[in.AnsweredBy]
		if label == "" {
			label = "memoria"
		}
		return status, map[string]any{"answer": in.Answer, "answered_by": label, "sources": out, "reason": in.Diagnostics.Reason,
			"usage": map[string]any{"tokens": in.Usage.In + in.Usage.Out, "cost_usd": in.Usage.Cost}}
	case "/v1/search":
		hits := make([]map[string]any, 0, len(in.Hits))
		for _, h := range in.Hits {
			hits = append(hits, map[string]any{"title": h.Title, "content": h.Content, "score": h.Score, "project": h.Project, "date": h.Date, "kind": h.Source, "ref": ref(h.src)})
		}
		return status, map[string]any{"hits": hits, "reason": in.Diagnostics.Reason}
	case "/v1/projects":
		ps := make([]map[string]any, 0, len(in.Projects))
		for _, p := range in.Projects {
			ps = append(ps, map[string]any{"name": p.Name, "documents": p.Documents})
		}
		return status, map[string]any{"projects": ps}
	default:
		// ingest confirmation: short doc ref (8 chars, same as source refs) so the user can verify the save
		return status, map[string]any{"chunks": in.Chunks, "indexed": in.Embedded, "ref": short(in.DocumentID), "created_at": in.CreatedAt, "title": in.Title, "duplicate": in.Skipped}
	}
}

// personalize: hide notes UI for users without Notas (+tenant); show the user's own name/initials.
func personalize(b []byte, e ent) []byte {
	if !e.has(modNotes) || !e.TenantMatch {
		b = bytes.Replace(b, []byte(`class="app" id="app"`), []byte(`class="app no-notes" id="app"`), 1)
	}
	if e.Superadmin || e.Email == "" {
		return b
	}
	local := strings.SplitN(e.Email, "@", 2)[0]
	ini := strings.ToUpper(local[:1])
	if len(local) > 1 {
		ini += strings.ToUpper(local[1:2])
	}
	esc := html.EscapeString
	b = bytes.ReplaceAll(b, []byte(`<span class="profile-name">Memia</span>`), []byte(`<span class="profile-name">`+esc(e.Email)+`</span>`))
	b = bytes.ReplaceAll(b, []byte(`title="Memia">JA<`), []byte(`title="`+esc(e.Email)+`">`+esc(ini)+`<`))
	b = bytes.ReplaceAll(b, []byte(`aria-hidden="true">JA<`), []byte(`aria-hidden="true">`+esc(ini)+`<`))
	b = bytes.ReplaceAll(b, []byte(`<span class="brand-mark sm">JA</span>`), []byte(`<span class="brand-mark sm">`+esc(ini)+`</span>`))
	return b
}
