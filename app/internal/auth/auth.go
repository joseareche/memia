// Package auth implements bearer-token auth. Tokens are stored only as SHA-256
// hashes and resolved through the SECURITY DEFINER function auth_lookup_token().
package auth

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"

	"devbrain/internal/tenant"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Authenticator struct{ pool *pgxpool.Pool }

func New(p *pgxpool.Pool) *Authenticator { return &Authenticator{pool: p} }

func HashToken(tok string) []byte {
	h := sha256.Sum256([]byte(tok))
	return h[:]
}

func ParseBearer(h string) (string, bool) {
	const p = "Bearer "
	if len(h) <= len(p) || !strings.EqualFold(h[:len(p)], p) {
		return "", false
	}
	t := strings.TrimSpace(h[len(p):])
	return t, t != ""
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="devbrain"`)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
}

// TODO(auth): scope enforcement per route, last_used_at tracking, rate limiting.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := ParseBearer(r.Header.Get("Authorization"))
		if !ok {
			unauthorized(w)
			return
		}
		var i tenant.Info
		err := a.pool.QueryRow(r.Context(),
			"SELECT tenant_id::text, user_id::text, scopes FROM auth_lookup_token($1)", HashToken(tok)).
			Scan(&i.TenantID, &i.UserID, &i.Scopes)
		if err != nil {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(tenant.With(r.Context(), i)))
	})
}
