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

func histApp(t *testing.T, dir string) (*app, http.Handler) {
	h, err := openHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{user: "default", origin: "https://chat.example.com", hist: h, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		sessions: map[string]*session{"ok": {created: time.Now(), seen: time.Now(), email: "admin@example.com"}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /chat/api/conversations", a.convList)
	mux.HandleFunc("POST /chat/api/conversations", a.convCreate)
	mux.HandleFunc("GET /chat/api/conversations/{id}", a.convGet)
	mux.HandleFunc("PATCH /chat/api/conversations/{id}", a.convRename)
	mux.HandleFunc("DELETE /chat/api/conversations/{id}", a.convDelete)
	mux.HandleFunc("POST /chat/api/conversations/{id}/messages", a.convAppend)
	return a, mux
}

func do(h http.Handler, method, path, body, cookie, origin string) (int, map[string]any) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if cookie != "" {
		r.Header.Set("Cookie", cookieName+"="+cookie)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func TestHistoryFlow(t *testing.T) {
	dir := t.TempDir()
	_, h := histApp(t, dir)
	O := "https://chat.example.com"
	if c, _ := do(h, "GET", "/chat/api/conversations", "", "", ""); c != 401 {
		t.Fatal("anon list", c)
	}
	if c, _ := do(h, "POST", "/chat/api/conversations", "{}", "ok", "https://evil.example"); c != 403 {
		t.Fatal("evil origin", c)
	}
	c, m := do(h, "POST", "/chat/api/conversations", "{}", "ok", O)
	if c != 201 {
		t.Fatal("create", c)
	}
	id := m["id"].(string)
	msgs := `{"messages":[{"role":"user","text":"¿Qué notas tengo sobre la PBX?"},{"role":"bot","text":"Tienes 2 [1]","by":"memoria","sources":[{"n":1,"title":"PBX"}],"meta":"1 fuente(s) · sin coste"}]}`
	if c, m = do(h, "POST", "/chat/api/conversations/"+id+"/messages", msgs, "ok", O); c != 200 || m["title"] != "¿Qué notas tengo sobre la PBX?" {
		t.Fatal("append", c, m)
	}
	if c, _ = do(h, "POST", "/chat/api/conversations/"+id+"/messages", `{"messages":[{"role":"admin","text":"x"}]}`, "ok", O); c != 400 {
		t.Fatal("bad role", c)
	}
	// reload from disk: new process sees the same data
	_, h2 := histApp(t, dir)
	c, m = do(h2, "GET", "/chat/api/conversations", "", "ok", "")
	list := m["conversations"].([]any)
	if c != 200 || len(list) != 1 || list[0].(map[string]any)["messages"].(float64) != 2 {
		t.Fatal("list after reload", c, m)
	}
	c, m = do(h2, "GET", "/chat/api/conversations/"+id, "", "ok", "")
	got := m["messages"].([]any)
	if c != 200 || len(got) != 2 || got[1].(map[string]any)["by"] != "memoria" || len(got[1].(map[string]any)["sources"].([]any)) != 1 {
		t.Fatal("get", c, m)
	}
	if c, m = do(h2, "PATCH", "/chat/api/conversations/"+id, `{"title":"PBX notas"}`, "ok", O); c != 200 || m["title"] != "PBX notas" {
		t.Fatal("rename", c, m)
	}
	if c, _ = do(h2, "DELETE", "/chat/api/conversations/"+id, "", "ok", ""); c != 403 {
		t.Fatal("delete without origin", c)
	}
	if c, _ = do(h2, "DELETE", "/chat/api/conversations/"+id, "", "ok", O); c != 200 {
		t.Fatal("delete", c)
	}
	if c, _ = do(h2, "GET", "/chat/api/conversations/"+id, "", "ok", ""); c != 404 {
		t.Fatal("get deleted", c)
	}
}
