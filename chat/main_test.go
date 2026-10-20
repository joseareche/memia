package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRateLimit(t *testing.T) {
	a := &app{fails: map[string][]time.Time{}}
	for i := 0; i < failPerIP; i++ {
		if a.limited("1.2.3.4") {
			t.Fatal("limited too early")
		}
		a.recordFail("1.2.3.4")
	}
	if !a.limited("1.2.3.4") || a.limited("5.6.7.8") {
		t.Fatal("per-IP limit wrong")
	}
}

func TestSessionExpiry(t *testing.T) {
	a := &app{sessions: map[string]*session{"ok": {created: time.Now(), seen: time.Now()}, "old": {created: time.Now().Add(-13 * time.Hour), seen: time.Now()}}}
	r := httptest.NewRequest("GET", "/chat", nil)
	r.Header.Set("Cookie", cookieName+"=ok")
	if !a.authed(r) {
		t.Fatal("valid session rejected")
	}
	r.Header.Set("Cookie", cookieName+"=old")
	if a.authed(r) {
		t.Fatal("expired session accepted")
	}
}

func TestSanitizeHidesProvider(t *testing.T) {
	up := []byte(`{"answer":"hola [1]","model":"openai/gpt-4o-mini","retrieval":"semantic","redactions":{"x":1},"sources":[{"n":1,"title":"t","document_id":"d"}]}`)
	_, out := sanitize("/v1/answer", 200, up)
	b, _ := json.Marshal(out)
	for _, bad := range []string{"openai", "gpt", "model", "semantic", "document_id", "redactions"} {
		if strings.Contains(strings.ToLower(string(b)), bad) {
			t.Fatalf("leaked %q: %s", bad, b)
		}
	}
	_, out = sanitize("/v1/search", 200, []byte(`{"mode":"semantic","index":"oa-te3s-1536-v1","note":"external provider","hits":[]}`))
	b, _ = json.Marshal(out)
	if strings.Contains(string(b), "oa-te3s") || strings.Contains(string(b), "provider") {
		t.Fatalf("leaked: %s", b)
	}
	code, out := sanitize("/v1/answer", 403, []byte(`{"error":"policy_local_only","detail":"answering requires an external LLM"}`))
	b, _ = json.Marshal(out)
	if code != 403 || strings.Contains(string(b), "LLM") || strings.Contains(string(b), "policy") {
		t.Fatalf("leaked: %s", b)
	}
	if c, _ := sanitize("/v1/answer", 500, nil); c != 502 {
		t.Fatal("5xx not normalized")
	}
}

func TestSanitizeAnswerLabelsAndSources(t *testing.T) {
	raw := `{"answer":"ok","answered_by":"external_ai","model":"openai/gpt-4o-mini","usage":{"input_tokens":10,"output_tokens":5,"cost_usd":0.0001},
	"sources":[{"n":1,"title":"T","project":"p","date":"2026-01-02","source":"cursor","document_id":"0123456789ab","chunk_id":"abcdef012345","conversation_id":"c0ffee001122","excerpt":"x","score":0.8}],
	"diagnostics":{"searched":["semantic: pgvector index oa-te3s-1536-v1"],"reason":""}}`
	_, out := sanitize("/v1/answer", 200, []byte(raw))
	b, _ := json.Marshal(out)
	s := string(b)
	for _, bad := range []string{"openai", "gpt", "oa-te3s", "pgvector"} {
		if strings.Contains(strings.ToLower(s), bad) {
			t.Fatalf("leaked %q: %s", bad, s)
		}
	}
	for _, want := range []string{`"answered_by":"IA externa"`, `"project":"p"`, `"date":"2026-01-02"`, `doc 01234567 · frag abcdef01 · conv c0ffee00`, `"tokens":15`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s in %s", want, s)
		}
	}
}

func TestSanitizeIngestConfirmation(t *testing.T) {
	raw := []byte(`{"document_id":"ea58c966-1111-2222-3333-444455556666","chunks":1,"embedded":true,"source":"note","title":"Idea","created_at":"2026-10-19T15:15:08Z","index":"oa-te3s-1536-v1","storage_redactions":{}}`)
	code, out := sanitize("/v1/documents", 201, raw)
	m := out.(map[string]any)
	if code != 201 || m["ref"] != "ea58c966" || m["created_at"] != "2026-10-19T15:15:08Z" || m["title"] != "Idea" || m["indexed"] != true {
		t.Fatalf("bad confirmation: %d %v", code, m)
	}
	if _, leak := m["index"]; leak {
		t.Fatal("index name must not reach the browser")
	}
}
