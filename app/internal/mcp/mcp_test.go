package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"devbrain/internal/tenant"
)

func do(h http.Handler, method, sid, origin, body string, scopes ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/mcp", strings.NewReader(body))
	r = r.WithContext(tenant.With(r.Context(), tenant.Info{TenantID: "t1", UserID: "u1", Scopes: scopes}))
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if sid != "" {
		r.Header.Set("Mcp-Session-Id", sid)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestMCPSessionsAndScopes(t *testing.T) {
	h := New("t", []string{"https://ok"})
	h.Register(Tool{Name: "r", Scopes: []string{"mcp:read"}, InputSchema: map[string]any{"type": "object"},
		Call: func(_ context.Context, a json.RawMessage) (string, any, error) { return "hello " + string(a), nil, nil }},
		Tool{Name: "w", Scopes: []string{"mcp:write"}, InputSchema: map[string]any{"type": "object"},
			Call: func(context.Context, json.RawMessage) (string, any, error) { return "", nil, ToolError{"bad"} }})
	w := do(h, "POST", "", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`, "mcp:read")
	sid := w.Header().Get("Mcp-Session-Id")
	if w.Code != 200 || sid == "" || !strings.Contains(w.Body.String(), "2025-03-26") {
		t.Fatalf("initialize: %d %s", w.Code, w.Body)
	}
	if w := do(h, "POST", "", "", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, "mcp:read"); w.Code != 400 {
		t.Fatalf("missing session: %d", w.Code)
	}
	if w := do(h, "POST", "nope", "", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, "mcp:read"); w.Code != 404 {
		t.Fatalf("unknown session: %d", w.Code)
	}
	if w := do(h, "POST", sid, "", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "mcp:read"); w.Code != 202 {
		t.Fatalf("notification: %d", w.Code)
	}
	w = do(h, "POST", sid, "", `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`, "mcp:read")
	if !strings.Contains(w.Body.String(), `"name":"r"`) || strings.Contains(w.Body.String(), `"name":"w"`) {
		t.Fatalf("tools/list scope filter: %s", w.Body)
	}
	w = do(h, "POST", sid, "", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"r","arguments":{"x":1}}}`, "mcp:read")
	if !strings.Contains(w.Body.String(), `hello {\"x\":1}`) {
		t.Fatalf("call: %s", w.Body)
	}
	w = do(h, "POST", sid, "", `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"w","arguments":{}}}`, "mcp:read")
	if !strings.Contains(w.Body.String(), `"isError":true`) || !strings.Contains(w.Body.String(), "forbidden") {
		t.Fatalf("scope deny: %s", w.Body)
	}
	if w := do(h, "POST", sid, "https://evil", `{"jsonrpc":"2.0","id":6,"method":"ping"}`, "mcp:read"); w.Code != 403 {
		t.Fatalf("origin: %d", w.Code)
	}
	// session bound to tenant/user: other tenant can't reuse it
	r := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"ping"}`))
	r = r.WithContext(tenant.With(r.Context(), tenant.Info{TenantID: "t2", UserID: "u2", Scopes: []string{"mcp:read"}}))
	r.Header.Set("Mcp-Session-Id", sid)
	ww := httptest.NewRecorder()
	h.ServeHTTP(ww, r)
	if ww.Code != 404 {
		t.Fatalf("cross-tenant session: %d", ww.Code)
	}
	if w := do(h, "GET", sid, "", ""); w.Code != 405 {
		t.Fatalf("GET: %d", w.Code)
	}
	if w := do(h, "DELETE", sid, "", "", "mcp:read"); w.Code != 204 {
		t.Fatalf("DELETE: %d", w.Code)
	}
	if w := do(h, "POST", sid, "", `{"jsonrpc":"2.0","id":8,"method":"ping"}`, "mcp:read"); w.Code != 404 {
		t.Fatalf("after delete: %d", w.Code)
	}
}
