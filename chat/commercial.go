package main

// Commercial (Cotizador IA) proxy: /chat/api/commercial/* -> DevBrain /v1/commercial/*.
// Strict method+path allowlist, session + Origin checks, body cap, HMAC-signed actor
// (the session's verified email). The browser never sees the DevBrain token or key.
// Feature flag CHAT_COMMERCIAL_QUOTES_AI only controls the UI (off until Phase B);
// the API proxy is enabled when the actor_hmac_key credential is present.

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type commercialProxy struct {
	key    []byte
	uiFlag bool
}

var comRoutes = []struct {
	method string
	re     *regexp.Regexp
}{}

func init() {
	id := `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`
	add := func(m, p string) {
		comRoutes = append(comRoutes, struct {
			method string
			re     *regexp.Regexp
		}{m, regexp.MustCompile("^" + p + "$")})
	}
	add("GET", "/me")
	for _, p := range []string{"/settings", "/company", "/series"} {
		add("GET", p)
		add("PATCH", p)
	}
	add("GET", "/customers")
	add("GET", "/customers/search")
	add("POST", "/customers")
	add("GET", "/customers/"+id)
	add("PATCH", "/customers/"+id)
	add("POST", "/calculate")
	add("POST", "/intent")
	add("GET", "/integrations")
	add("GET", "/catalog")
	add("POST", "/catalog")
	add("GET", "/catalog/"+id)
	add("PATCH", "/catalog/"+id)
	add("POST", "/catalog/"+id+"/prices")
	add("POST", "/quotes/"+id+"/export")
	add("GET", "/quotes/"+id+"/exports")
	add("GET", "/exports/"+id)
	add("GET", "/quotes")
	add("POST", "/quotes")
	add("GET", "/quotes/"+id)
	add("PATCH", "/quotes/"+id)
	add("GET", "/quotes/"+id+"/(versions|events)")
	add("POST", "/quotes/"+id+"/(duplicate|request-approval|approve|return|send|accept|reject|expire|cancel|revise)")
}

func comAllowed(method, sub string) bool {
	for _, r := range comRoutes {
		if r.method == method && r.re.MatchString(sub) {
			return true
		}
	}
	return false
}

var dispRe = regexp.MustCompile(`^attachment; filename="[A-Za-z0-9._-]{1,120}"$`)

var comQuery = map[string]bool{"q": true, "status": true, "all": true}

func signActor(key []byte, email string, ts time.Time) string {
	sec := strconv.FormatInt(ts.Unix(), 10)
	m := hmac.New(sha256.New, key)
	m.Write([]byte("v1|" + email + "|" + sec))
	return "v1." + email + "." + sec + "." + hex.EncodeToString(m.Sum(nil))
}

func (a *app) mountCommercial(mux *http.ServeMux) {
	dir := os.Getenv("CREDENTIALS_DIRECTORY")
	b, err := os.ReadFile(filepath.Join(dir, "actor_hmac_key"))
	if dir == "" || err != nil {
		a.log.Info("commercial proxy disabled", "reason", "no actor_hmac_key credential")
		return
	}
	k, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(k) < 32 {
		a.log.Error("commercial proxy disabled", "reason", "invalid actor_hmac_key")
		return
	}
	a.com = &commercialProxy{key: k, uiFlag: env("CHAT_COMMERCIAL_QUOTES_AI", "0") == "1"}
	for _, m := range []string{"GET", "POST", "PATCH"} { // method-qualified to coexist with "GET /chat/"
		mux.HandleFunc(m+" /chat/api/commercial/", a.commercial)
	}
	a.log.Info("commercial proxy enabled", "ui_flag", a.com.uiFlag)
}

func (a *app) sessionEmail(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.sessions[c.Value]; ok {
		return s.email
	}
	return ""
}

func (a *app) needNotes(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.authed(r) {
			if e := a.reqEnt(r); !e.has(modNotes) || !e.TenantMatch {
				moduleDenied(w, "Notas")
				return
			}
		}
		next(w, r)
	}
}

func comErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (a *app) commercial(w http.ResponseWriter, r *http.Request) {
	if !a.authed(r) {
		comErr(w, 401, "Tu sesión no es válida.")
		return
	}
	email := a.sessionEmail(r)
	sub := strings.TrimPrefix(r.URL.Path, "/chat/api/commercial")
	if email == "" || !comAllowed(r.Method, sub) {
		comErr(w, 404, "No encontrado.")
		return
	}
	if !a.reqEnt(r).has(modCotizador) {
		moduleDenied(w, "Cotizador")
		return
	}
	var body io.Reader
	if r.Method != http.MethodGet {
		if !a.sameOrigin(r) || (r.ContentLength != 0 && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json")) {
			comErr(w, 403, "Esta acción no está permitida.")
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAPIBody))
		if err != nil {
			comErr(w, 413, "La solicitud es demasiado grande.")
			return
		}
		body = bytes.NewReader(raw)
	}
	qs := ""
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		for k := range q {
			if !comQuery[k] {
				q.Del(k)
			}
		}
		if e := q.Encode(); e != "" {
			qs = "?" + e
		}
	}
	req, _ := http.NewRequestWithContext(r.Context(), r.Method, a.upstream+"/v1/commercial"+sub+qs, body)
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("X-DevBrain-Actor", signActor(a.com.key, email, time.Now()))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		a.log.Error("commercial upstream", "err", err)
		comErr(w, 502, "El servicio no está disponible ahora mismo.")
		return
	}
	defer resp.Body.Close()
	if st := resp.StatusCode; st == 200 && strings.HasPrefix(sub, "/exports/") { // file download: pdf/docx only
		ct := resp.Header.Get("Content-Type")
		if ct != "application/pdf" && ct != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
			comErr(w, 502, "El servicio no está disponible ahora mismo.")
			return
		}
		cd := resp.Header.Get("Content-Disposition")
		if !dispRe.MatchString(cd) {
			cd = "attachment"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Disposition", cd)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		n, _ := io.Copy(w, io.LimitReader(resp.Body, 20<<20))
		a.log.Info("commercial download", "path", sub, "bytes", n)
		return
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	a.log.Info("commercial proxy", "method", r.Method, "path", sub, "status", resp.StatusCode)
	st := resp.StatusCode
	switch {
	case st/100 == 2:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(st)
		_, _ = w.Write(raw)
	case st == 400 || st == 404 || st == 409: // business validation messages authored by the commercial module
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) != nil || e.Error == "" || len(e.Error) > 400 {
			e.Error = "La solicitud no es válida."
		}
		comErr(w, st, e.Error)
	case st == 401:
		comErr(w, 401, "Tu sesión no es válida.")
	case st == 403:
		comErr(w, 403, "Esta acción no está permitida para tu cuenta.")
	default:
		comErr(w, 502, "El servicio no está disponible ahora mismo.")
	}
}

// withCommercialUI injects the Cotizador IA assets only when the UI flag is on.
func (a *app) withCommercialUI(b []byte) []byte {
	if a.com == nil || !a.com.uiFlag {
		return b
	}
	b = bytes.Replace(b, []byte("<!--COMMERCIAL_CSS-->"), []byte(`<link rel="stylesheet" href="/chat/static/quotes.css">`), 1)
	return bytes.Replace(b, []byte("<!--COMMERCIAL_JS-->"), []byte(`<script src="/chat/static/quotes.js"></script>`), 1)
}
