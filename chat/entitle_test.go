package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Fake DevBrain: /v1/platform/me by signed actor; /v1/* anything else = 200 (must never be reached when denied).
func entApp(t *testing.T) (*app, http.Handler, *int) {
	hits := 0
	key := []byte(strings.Repeat("k", 32))
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/platform/me" {
			hits++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"hits":[],"answer":"x"}`))
			return
		}
		h := r.Header.Get("X-DevBrain-Actor")
		email := strings.Split(strings.TrimPrefix(h, "v1."), ".")[0] + "." + strings.Split(strings.TrimPrefix(h, "v1."), ".")[1]
		if signActor(key, email, time.Now()) != h && signActor(key, email, time.Now().Add(-time.Second)) != h {
			w.WriteHeader(401)
			return
		}
		m := map[string]ent{
			"chat@x.com":  {Email: "chat@x.com", Enabled: true, Modules: map[string]string{"chat": ""}},
			"off@x.com":   {Email: "off@x.com", Enabled: false, Modules: map[string]string{}},
			"notes@x.com": {Email: "notes@x.com", Enabled: true, Modules: map[string]string{"chat": "", "notes": ""}, TenantMatch: false},
			"boss@x.com":  {Email: "boss@x.com", Enabled: true, Superadmin: true, TenantMatch: true, Modules: map[string]string{"chat": "", "notes": "", "cotizador": "admin"}},
		}
		e, ok := m[email]
		if !ok {
			e = ent{Email: email, Modules: map[string]string{}}
		}
		_ = json.NewEncoder(w).Encode(e)
	}))
	t.Cleanup(up.Close)
	now := time.Now()
	ss := map[string]*session{}
	for _, e := range []string{"chat@x.com", "off@x.com", "notes@x.com", "boss@x.com", "ghost@x.com"} {
		ss[e] = &session{created: now, seen: now, email: e}
	}
	h, _ := openHistory(t.TempDir())
	a := &app{user: "default", origin: "https://chat.example.com", hist: h, log: slog.New(slog.NewTextHandler(io.Discard, nil)), sessions: ss,
		upstream: up.URL, hc: up.Client(), token: "t", com: &commercialProxy{key: key, uiFlag: true}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /chat/api/me", a.me)
	mux.HandleFunc("GET /chat/api/conversations", a.convList)
	mux.HandleFunc("POST /chat/api/answer", a.needNotes(a.proxy("/v1/answer")))
	mux.HandleFunc("/chat/api/commercial/", a.commercial)
	mux.HandleFunc("GET /chat", a.page)
	return a, mux, &hits
}

func TestEntitlementsEnforced(t *testing.T) {
	_, h, hits := entApp(t)
	O := "https://chat.example.com"
	cases := []struct {
		who, method, path string
		want              int
	}{
		{"chat@x.com", "GET", "/chat/api/conversations", 200},
		{"chat@x.com", "GET", "/chat/api/commercial/me", 403},
		{"chat@x.com", "POST", "/chat/api/answer", 403},
		{"notes@x.com", "POST", "/chat/api/answer", 403}, // notes but no tenant -> still denied
		{"off@x.com", "GET", "/chat/api/conversations", 403},
		{"off@x.com", "GET", "/chat", 403},
		{"ghost@x.com", "GET", "/chat/api/conversations", 403},
		{"ghost@x.com", "GET", "/chat/api/commercial/quotes", 403},
		{"boss@x.com", "GET", "/chat/api/conversations", 200},
		{"boss@x.com", "POST", "/chat/api/answer", 200},
		{"boss@x.com", "GET", "/chat/api/commercial/me", 200},
	}
	for _, c := range cases {
		body := ""
		if c.method == "POST" {
			body = `{"q":"x"}`
		}
		code, m := do(h, c.method, c.path, body, c.who, O)
		if code != c.want {
			t.Errorf("%s %s %s = %d want %d (%v)", c.who, c.method, c.path, code, c.want, m)
		}
		if c.want == 403 && strings.HasPrefix(c.path, "/chat/api/") && m["code"] != "module_disabled" {
			t.Errorf("%s %s: missing module_disabled code: %v", c.who, c.path, m)
		}
	}
	if *hits != 2 { // only boss's answer + commercial/me reached the upstream
		t.Errorf("upstream hits = %d, want 2", *hits)
	}
	_, m := do(h, "GET", "/chat/api/me", "", "chat@x.com", "")
	if b, _ := json.Marshal(m["modules"]); string(b) != `["chat"]` {
		t.Errorf("me modules = %s", b)
	}
	_, m = do(h, "GET", "/chat/api/me", "", "boss@x.com", "")
	if b, _ := json.Marshal(m["modules"]); string(b) != `["chat","notes","cotizador"]` {
		t.Errorf("boss modules = %s", b)
	}
}

func TestPageHidesCotizadorWithoutModule(t *testing.T) {
	_, h, _ := entApp(t)
	for who, want := range map[string]bool{"chat@x.com": false, "boss@x.com": true} {
		r := httptest.NewRequest("GET", "/chat", nil)
		r.Header.Set("Cookie", cookieName+"="+who)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if got := strings.Contains(w.Body.String(), "quotes.js"); got != want || w.Code != 200 {
			t.Errorf("%s: code %d quotes.js=%v want %v", who, w.Code, got, want)
		}
	}
}

func TestHistoryIsPerUser(t *testing.T) {
	a, _, _ := entApp(t)
	if a.histOwner("admin@example.com") != "default" || a.histOwner("chat@x.com") != "chat@x.com" {
		t.Fatal("history owner mapping")
	}
}

func TestPersonalize(t *testing.T) {
	b := []byte(`<div class="app" id="app"><span class="profile-name">Memia</span><span class="rail-avatar" title="Memia">JA</span>`)
	out := string(personalize(b, ent{Email: "ana<x>@x.com", Enabled: true, Modules: map[string]string{"chat": ""}}))
	if !strings.Contains(out, "no-notes") || strings.Contains(out, "Memia") || strings.Contains(out, "<x>") {
		t.Fatal(out)
	}
	out = string(personalize(b, ent{Email: "admin@example.com", Enabled: true, Superadmin: true, TenantMatch: true, Modules: map[string]string{"chat": "", "notes": ""}}))
	if strings.Contains(out, "no-notes") || !strings.Contains(out, "Memia") {
		t.Fatal(out)
	}
}
