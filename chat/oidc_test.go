package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

const cid = "test-client.apps.googleusercontent.com"

func testOIDC() (*oidcConfig, *rsa.PrivateKey) {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	o := &oidcConfig{clientID: cid, clientSecret: "s", redirect: "https://chat.example.com/chat/auth/google/callback",
		allowed: map[string]bool{"admin@example.com": true}, pend: map[string]pending{},
		keys: &jwks{url: "http://127.0.0.1:1/", hc: &http.Client{Timeout: time.Second},
			keys: map[string]*rsa.PublicKey{"k1": &k.PublicKey}, fetched: time.Now()}}
	return o, k
}

func sign(k *rsa.PrivateKey, kid string, claims map[string]any) string {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": kid, "typ": "JWT"})
	c, _ := json.Marshal(claims)
	in := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	d := sha256.Sum256([]byte(in))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, d[:])
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func good() map[string]any {
	now := time.Now().Unix()
	return map[string]any{"iss": "https://accounts.google.com", "aud": cid, "azp": cid, "exp": now + 3600, "iat": now,
		"nonce": "N", "email": "Admin@example.com", "email_verified": true}
}

func TestVerifyIDToken(t *testing.T) {
	o, k := testOIDC()
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	ctx := context.Background()
	if e, err := o.verifyIDToken(ctx, sign(k, "k1", good()), "N", time.Now()); err != nil || e != "admin@example.com" {
		t.Fatalf("valid token rejected: %v", err)
	}
	cases := map[string]func(m map[string]any) (string, string){
		"not allowlisted": func(m map[string]any) (string, string) {
			m["email"] = "attacker@example.com"
			return sign(k, "k1", m), "N"
		},
		"lookalike email": func(m map[string]any) (string, string) {
			m["email"] = "admin@example.com.evil.com"
			return sign(k, "k1", m), "N"
		},
		"email unverified": func(m map[string]any) (string, string) { m["email_verified"] = false; return sign(k, "k1", m), "N" },
		"wrong aud":        func(m map[string]any) (string, string) { m["aud"] = "other"; return sign(k, "k1", m), "N" },
		"multi aud bad azp": func(m map[string]any) (string, string) {
			m["aud"] = []string{cid, "x"}
			m["azp"] = "x"
			return sign(k, "k1", m), "N"
		},
		"wrong iss": func(m map[string]any) (string, string) { m["iss"] = "https://evil"; return sign(k, "k1", m), "N" },
		"expired": func(m map[string]any) (string, string) {
			m["exp"] = time.Now().Unix() - 3600
			return sign(k, "k1", m), "N"
		},
		"wrong nonce":   func(m map[string]any) (string, string) { return sign(k, "k1", m), "X" },
		"bad signature": func(m map[string]any) (string, string) { return sign(other, "k1", m), "N" },
		"unknown kid":   func(m map[string]any) (string, string) { return sign(k, "zz", m), "N" },
		"alg none": func(m map[string]any) (string, string) {
			p := strings.Split(sign(k, "k1", m), ".")
			return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","kid":"k1"}`)) + "." + p[1] + ".", "N"
		},
	}
	for name, mk := range cases {
		tok, nonce := mk(good())
		if _, err := o.verifyIDToken(ctx, tok, nonce, time.Now()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAuthURLParams(t *testing.T) {
	o, _ := testOIDC()
	state, u := o.authURL()
	pu, _ := url.Parse(u)
	q := pu.Query()
	if pu.Host != "accounts.google.com" || q.Get("client_id") != cid || q.Get("redirect_uri") != o.redirect ||
		q.Get("response_type") != "code" || q.Get("scope") != "openid email" || q.Get("state") != state ||
		q.Get("nonce") == "" || q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 {
		t.Fatalf("bad auth url: %s", u)
	}
	p := o.pend[state]
	if pkceChallenge(p.verifier) != q.Get("code_challenge") || p.nonce != q.Get("nonce") {
		t.Fatal("pkce/nonce not bound to state")
	}
}

func TestCallbackRejectsBadState(t *testing.T) {
	o, _ := testOIDC()
	a := &app{oidc: o, fails: map[string][]time.Time{}, sessions: map[string]*session{}, log: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	state, _ := o.authURL()
	for name, tc := range map[string][2]string{
		"no cookie":       {state, ""},
		"cookie mismatch": {state, "other"},
		"forged state":    {"forged", "forged"},
	} {
		r := httptest.NewRequest("GET", "/chat/auth/google/callback?code=c&state="+tc[0], nil)
		if tc[1] != "" {
			r.AddCookie(&http.Cookie{Name: oauthCookie, Value: tc[1]})
		}
		w := httptest.NewRecorder()
		a.googleCallback(w, r)
		if w.Code != 400 || len(a.sessions) != 0 {
			t.Errorf("%s: got %d", name, w.Code)
		}
	}
	s2, _ := o.authURL()
	if _, err := o.takePending(s2, s2); err != nil {
		t.Fatal(err)
	}
	if _, err := o.takePending(s2, s2); err == nil {
		t.Error("state reusable")
	}
}
