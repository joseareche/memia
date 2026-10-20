package main

// Module entitlements, resolved by DevBrain (/v1/platform/me, HMAC-signed actor) and cached briefly.
// The chat enforces them server-side: login (chat), conversations (chat), notes/answer (notes + tenant),
// cotizador proxy (cotizador). The UI only mirrors /chat/api/me.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	modChat      = "chat"
	modNotes     = "notes"
	modCotizador = "cotizador"
)

type ent struct {
	Email       string            `json:"email"`
	Enabled     bool              `json:"enabled"`
	Superadmin  bool              `json:"superadmin"`
	Modules     map[string]string `json:"modules"`
	TenantMatch bool              `json:"tenant_match"`
}

func (e ent) has(m string) bool {
	if !e.Enabled {
		return false
	}
	_, ok := e.Modules[m]
	return ok
}

type entCache struct {
	mu sync.Mutex
	m  map[string]entItem
}
type entItem struct {
	e  ent
	at time.Time
}

const entTTL = 15 * time.Second

var errNoEnt = errors.New("entitlements unavailable")

func (a *app) fetchEnt(ctx context.Context, email string) (ent, error) {
	if a.com == nil || len(a.com.key) < 32 {
		return ent{}, errNoEnt
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.upstream+"/v1/platform/me", nil)
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("X-DevBrain-Actor", signActor(a.com.key, email, time.Now()))
	resp, err := a.hc.Do(req)
	if err != nil {
		return ent{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ent{}, errNoEnt
	}
	var e ent
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e); err != nil {
		return ent{}, err
	}
	if e.Modules == nil {
		e.Modules = map[string]string{}
	}
	return e, nil
}

// entFor: cached entitlements. If DevBrain is unreachable, the last known value is reused; a
// break-glass (CHAT_ALLOWED_EMAILS) account with no cached value gets chat only. Others get nothing.
func (a *app) entFor(ctx context.Context, email string) ent {
	if email == "" {
		return ent{}
	}
	a.ents.mu.Lock()
	if a.ents.m == nil {
		a.ents.m = map[string]entItem{}
	}
	it, ok := a.ents.m[email]
	a.ents.mu.Unlock()
	if ok && time.Since(it.at) < entTTL {
		return it.e
	}
	e, err := a.fetchEnt(ctx, email)
	if err != nil {
		a.log.Warn("entitlements fetch failed", "err", err)
		if ok {
			return it.e
		}
		if a.oidc != nil && a.oidc.allowed[email] || email == a.actorEmail() {
			return ent{Email: email, Enabled: true, Modules: map[string]string{modChat: ""}}
		}
		return ent{Email: email}
	}
	a.ents.mu.Lock()
	a.ents.m[email] = entItem{e, time.Now()}
	a.ents.mu.Unlock()
	return e
}

func (a *app) reqEnt(r *http.Request) ent { return a.entFor(r.Context(), a.sessionEmail(r)) }

func (a *app) actorEmail() string { return env("CHAT_ACTOR_EMAIL", "admin@example.com") }

// histOwner keeps the owner's pre-existing history (stored under the legacy account name).
func (a *app) histOwner(email string) string {
	if email == "" || email == a.actorEmail() {
		return a.user
	}
	return email
}

func moduleDenied(w http.ResponseWriter, name string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "El módulo " + name + " no está habilitado para tu usuario.", "code": "module_disabled"})
}

func (a *app) me(w http.ResponseWriter, r *http.Request) {
	if !a.authed(r) {
		jsonOut(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	e := a.reqEnt(r)
	mods := []string{}
	for _, m := range []string{modChat, modNotes, modCotizador} {
		if e.has(m) && (m != modNotes || e.TenantMatch) && (m != modCotizador || (a.com != nil && a.com.uiFlag)) {
			mods = append(mods, m)
		}
	}
	jsonOut(w, 200, map[string]any{"email": e.Email, "superadmin": e.Superadmin, "modules": mods})
}
