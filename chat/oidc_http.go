package main

import (
	"context"
	"net/http"
	"time"
)

func randB64(n int) string {
	b := make([]byte, n)
	if _, err := randRead(b); err != nil {
		panic(err)
	}
	return b64(b)
}

func (a *app) googleStart(w http.ResponseWriter, r *http.Request) {
	if a.oidc == nil {
		http.Error(w, "google login not configured", http.StatusServiceUnavailable)
		return
	}
	state, u := a.oidc.authURL()
	// SameSite=Lax: must accompany the top-level GET back from accounts.google.com.
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Value: state, Path: "/", Secure: true, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: int(pendingTTL.Seconds())})
	http.Redirect(w, r, u, http.StatusFound)
}

func (a *app) googleCallback(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if a.oidc == nil {
		http.Error(w, "google login not configured", http.StatusServiceUnavailable)
		return
	}
	if a.limited(ip) {
		http.Error(w, "demasiados intentos, espera 15 minutos", http.StatusTooManyRequests)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	deny := func(reason string, code int) {
		a.recordFail(ip)
		a.log.Warn("google login denied", "ip", ip, "reason", reason)
		http.Error(w, "acceso denegado", code)
	}
	cookieState := ""
	if c, err := r.Cookie(oauthCookie); err == nil {
		cookieState = c.Value
	}
	q := r.URL.Query()
	p, err := a.oidc.takePending(q.Get("state"), cookieState)
	if err != nil {
		deny(err.Error(), http.StatusBadRequest)
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		deny("provider error", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	idt, err := a.oidc.exchange(ctx, a.pub, q.Get("code"), p.verifier)
	if err != nil {
		deny(err.Error(), http.StatusBadGateway)
		return
	}
	email, err := a.oidc.verifyIDToken(ctx, idt, p.nonce, time.Now())
	if err != nil {
		deny(err.Error(), http.StatusForbidden)
		return
	}
	a.startSession(w, email)
	a.log.Info("google login ok", "ip", ip, "email", email)
	// Same-origin hop so the SameSite=Strict session cookie is sent (a redirect chain that
	// started cross-site at accounts.google.com would not carry it).
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><meta http-equiv="refresh" content="0;url=/chat"><a href="/chat">Continuar</a>`))
}
