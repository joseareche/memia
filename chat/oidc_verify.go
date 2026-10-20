package main

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

type idClaims struct {
	Iss           string          `json:"iss"`
	Aud           json.RawMessage `json:"aud"`
	Azp           string          `json:"azp"`
	Exp           int64           `json:"exp"`
	Iat           int64           `json:"iat"`
	Nonce         string          `json:"nonce"`
	Email         string          `json:"email"`
	EmailVerified any             `json:"email_verified"`
}

// verifyIDToken returns the verified, allowlisted email.
func (o *oidcConfig) verifyIDToken(ctx context.Context, tok, nonce string, now time.Time) (string, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return "", errors.New("malformed id_token")
	}
	var hdr struct{ Alg, Kid string }
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(hb, &hdr) != nil {
		return "", errors.New("bad header")
	}
	if hdr.Alg != "RS256" {
		return "", errors.New("unexpected alg")
	}
	key, err := o.keys.get(ctx, hdr.Kid)
	if err != nil {
		return "", err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", errors.New("bad signature encoding")
	}
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, h[:], sig) != nil {
		return "", errors.New("bad signature")
	}
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	var c idClaims
	if err != nil || json.Unmarshal(pb, &c) != nil {
		return "", errors.New("bad claims")
	}
	if c.Iss != "https://accounts.google.com" && c.Iss != "accounts.google.com" {
		return "", errors.New("bad iss")
	}
	if !audOK(c.Aud, c.Azp, o.clientID) {
		return "", errors.New("bad aud")
	}
	if now.Unix() > c.Exp+60 || c.Iat > now.Unix()+300 {
		return "", errors.New("expired or not yet valid")
	}
	if c.Nonce == "" || subtle.ConstantTimeCompare([]byte(c.Nonce), []byte(nonce)) != 1 {
		return "", errors.New("bad nonce")
	}
	if v, ok := c.EmailVerified.(bool); !(ok && v) && c.EmailVerified != "true" {
		return "", errors.New("email not verified")
	}
	email := strings.ToLower(strings.TrimSpace(c.Email))
	if !o.allowed[email] && (o.allow == nil || !o.allow(ctx, email)) {
		return "", errors.New("email not allowlisted")
	}
	return email, nil
}

func audOK(raw json.RawMessage, azp, clientID string) bool {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s == clientID
	}
	var arr []string
	if json.Unmarshal(raw, &arr) != nil {
		return false
	}
	for _, a := range arr {
		if a == clientID {
			return azp == clientID
		}
	}
	return false
}

type jwks struct {
	url     string
	hc      *http.Client
	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

func (j *jwks) get(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if k, ok := j.keys[kid]; ok && time.Since(j.fetched) < time.Hour {
		return k, nil
	}
	if j.keys == nil || time.Since(j.fetched) > 30*time.Second {
		if err := j.fetch(ctx); err != nil {
			return nil, err
		}
	}
	if k, ok := j.keys[kid]; ok {
		return k, nil
	}
	return nil, errors.New("unknown kid")
}

func (j *jwks) fetch(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, j.url, nil)
	resp, err := j.hc.Do(req)
	if err != nil {
		return fmt.Errorf("jwks: %w", err)
	}
	defer resp.Body.Close()
	var doc struct {
		Keys []struct{ Kid, Kty, N, E string } `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return fmt.Errorf("jwks decode: %w", err)
	}
	m := map[string]*rsa.PublicKey{}
	for _, k := range doc.Keys {
		nb, err1 := base64.RawURLEncoding.DecodeString(k.N)
		eb, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if k.Kty != "RSA" || err1 != nil || err2 != nil {
			continue
		}
		m[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(new(big.Int).SetBytes(eb).Int64())}
	}
	j.keys, j.fetched = m, time.Now()
	return nil
}
