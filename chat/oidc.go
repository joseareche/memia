package main

// Google OIDC: authorization code + PKCE (S256) + state + nonce. ID token is verified
// locally: RS256 signature against Google JWKS, iss, aud (+azp), exp/iat, nonce,
// email_verified, and an explicit email allowlist. No external libraries.

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	googleAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL = "https://oauth2.googleapis.com/token"
	googleJWKSURL  = "https://www.googleapis.com/oauth2/v3/certs"
	oauthCookie    = "__Host-dbchat_oauth"
	pendingTTL     = 10 * time.Minute
)

type pending struct {
	nonce, verifier string
	created         time.Time
}

type oidcConfig struct {
	clientID, clientSecret, redirect string
	allowed                          map[string]bool                              // break-glass allowlist (CHAT_ALLOWED_EMAILS)
	allow                            func(ctx context.Context, email string) bool // admin-managed users (DevBrain)
	tokenURL                         string
	keys                             *jwks
	mu                               sync.Mutex
	pend                             map[string]pending
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func pkceChallenge(v string) string {
	h := sha256.Sum256([]byte(v))
	return b64(h[:])
}

// authURL builds the Google authorization redirect and remembers state->(nonce, verifier).
func (o *oidcConfig) authURL() (state, u string) {
	state, nonce, verifier := randB64(32), randB64(32), randB64(48)
	o.mu.Lock()
	now := time.Now()
	for k, p := range o.pend {
		if now.Sub(p.created) > pendingTTL {
			delete(o.pend, k)
		}
	}
	o.pend[state] = pending{nonce: nonce, verifier: verifier, created: now}
	o.mu.Unlock()
	q := url.Values{
		"client_id": {o.clientID}, "redirect_uri": {o.redirect}, "response_type": {"code"},
		"scope": {"openid email"}, "state": {state}, "nonce": {nonce},
		"code_challenge": {pkceChallenge(verifier)}, "code_challenge_method": {"S256"},
		"prompt": {"select_account"},
	}
	return state, googleAuthURL + "?" + q.Encode()
}

// takePending validates state (cookie == query, known, unexpired) and consumes it (one-time).
func (o *oidcConfig) takePending(queryState, cookieState string) (pending, error) {
	if queryState == "" || subtle.ConstantTimeCompare([]byte(queryState), []byte(cookieState)) != 1 {
		return pending{}, errors.New("state mismatch")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	p, ok := o.pend[queryState]
	delete(o.pend, queryState)
	if !ok || time.Since(p.created) > pendingTTL {
		return pending{}, errors.New("unknown or expired state")
	}
	return p, nil
}

func (o *oidcConfig) exchange(ctx context.Context, hc *http.Client, code, verifier string) (string, error) {
	form := url.Values{"code": {code}, "client_id": {o.clientID}, "client_secret": {o.clientSecret},
		"redirect_uri": {o.redirect}, "grant_type": {"authorization_code"}, "code_verifier": {verifier}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, o.tokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
	if resp.StatusCode != 200 || out.IDToken == "" {
		return "", fmt.Errorf("token endpoint HTTP %d %s", resp.StatusCode, out.Error)
	}
	return out.IDToken, nil
}
