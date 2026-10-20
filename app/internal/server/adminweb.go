package server

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed adminui/*
var adminFS embed.FS

const pbkdfIter = 600_000

// HashPassword returns "pbkdf2-sha256$iter$salt$hash" (base64 raw std).
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	k, err := pbkdf2.Key(sha256.New, pw, salt, pbkdfIter, 32)
	if err != nil {
		return "", err
	}
	e := base64.RawStdEncoding
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdfIter, e.EncodeToString(salt), e.EncodeToString(k)), nil
}

func checkPassword(enc, pw string) bool {
	p := strings.Split(enc, "$")
	if len(p) != 4 || p[0] != "pbkdf2-sha256" {
		return false
	}
	it, err := strconv.Atoi(p[1])
	e := base64.RawStdEncoding
	salt, err1 := e.DecodeString(p[2])
	want, err2 := e.DecodeString(p[3])
	if err != nil || err1 != nil || err2 != nil || it < 100_000 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, it, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// AdminAuth guards /admin: source-IP allowlist (as seen by Caddy) + HTTP Basic (PBKDF2 hash) + failure rate limit.
type AdminAuth struct {
	Allow []*net.IPNet
	Hash  string
	mu    sync.Mutex
	fails map[string][]time.Time
}

func NewAdminAuth(allowCSV, hash string) (*AdminAuth, error) {
	if hash == "" || allowCSV == "" {
		return nil, errors.New("admin disabled")
	}
	a := &AdminAuth{Hash: hash, fails: map[string][]time.Time{}}
	for _, s := range strings.Split(allowCSV, ",") {
		s = strings.TrimSpace(s)
		if !strings.Contains(s, "/") {
			s += "/32"
		}
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, err
		}
		a.Allow = append(a.Allow, n)
	}
	return a, nil
}

// clientIP: the app listens on loopback behind Caddy, which overwrites X-Forwarded-For with the peer address.
func clientIP(r *http.Request) net.IP {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return ip
	}
	xff := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0])
	return net.ParseIP(xff)
}

func (a *AdminAuth) limited(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	cut := time.Now().Add(-15 * time.Minute)
	l := a.fails[ip][:0]
	for _, t := range a.fails[ip] {
		if t.After(cut) {
			l = append(l, t)
		}
	}
	a.fails[ip] = l
	return len(l) >= 10
}

func (a *AdminAuth) fail(ip string) {
	a.mu.Lock()
	a.fails[ip] = append(a.fails[ip], time.Now())
	a.mu.Unlock()
}

func (a *AdminAuth) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; form-action 'none'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		ip := clientIP(r)
		ok := false
		for _, n := range a.Allow {
			if ip != nil && n.Contains(ip) {
				ok = true
			}
		}
		if !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if a.limited(ip.String()) {
			http.Error(w, "too many attempts", http.StatusTooManyRequests)
			return
		}
		u, p, has := r.BasicAuth()
		if !has || subtle.ConstantTimeCompare([]byte(u), []byte("admin")) != 1 || !checkPassword(a.Hash, p) {
			if has {
				a.fail(ip.String())
			}
			h.Set("WWW-Authenticate", `Basic realm="DevBrain admin", charset="UTF-8"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

var adminTypes = map[string]string{"admin.js": "text/javascript; charset=utf-8", "users.js": "text/javascript; charset=utf-8", "admin.css": "text/css; charset=utf-8",
	"logo-mark.png": "image/png", "manrope.woff2": "font/woff2", "bricolage.woff2": "font/woff2"}

func (a *AdminAuth) mount(route func(string, http.Handler), api *api) {
	route("GET /admin", a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := adminFS.ReadFile("adminui/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})))
	route("GET /admin/users", a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := adminFS.ReadFile("adminui/users.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})))
	route("GET /admin/static/{f}", a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f := r.PathValue("f")
		ct, ok := adminTypes[f]
		if !ok {
			http.NotFound(w, r)
			return
		}
		b, err := adminFS.ReadFile("adminui/" + f)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", ct)
		_, _ = w.Write(b)
	})))
	route("GET /admin/api/stats", a.wrap(http.HandlerFunc(api.stats)))
	route("GET /admin/api/stats2", a.wrap(http.HandlerFunc(api.statsV2)))
	a.mountUsers(route, api)
}
