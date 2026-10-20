package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"devbrain/internal/provider"
)

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil || !checkPassword(h, "correct horse battery") || checkPassword(h, "wrong") || checkPassword("x$1$a$b", "") {
		t.Fatal("pbkdf2 check failed")
	}
}

func TestAdminIPAndAuth(t *testing.T) {
	h, _ := HashPassword("correct horse battery")
	a, err := NewAdminAuth("10.0.0.2", h)
	if err != nil {
		t.Fatal(err)
	}
	ok := a.wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	do := func(xff, pw string) int {
		r := httptest.NewRequest("GET", "/admin", nil)
		r.RemoteAddr = "127.0.0.1:5000"
		r.Header.Set("X-Forwarded-For", xff)
		if pw != "" {
			r.SetBasicAuth("admin", pw)
		}
		w := httptest.NewRecorder()
		ok.ServeHTTP(w, r)
		if w.Header().Get("Content-Security-Policy") == "" {
			t.Fatal("missing CSP")
		}
		return w.Code
	}
	if c := do("192.0.2.30", "correct horse battery"); c != 403 {
		t.Fatalf("other IP: %d", c)
	}
	if c := do("10.0.0.2", ""); c != 401 {
		t.Fatalf("no auth: %d", c)
	}
	if c := do("10.0.0.2", "bad"); c != 401 {
		t.Fatalf("bad pw: %d", c)
	}
	if c := do("10.0.0.2", "correct horse battery"); c != 200 {
		t.Fatalf("good: %d", c)
	}
	for i := 0; i < 10; i++ {
		do("10.0.0.2", "bad")
	}
	if c := do("10.0.0.2", "correct horse battery"); c != 429 {
		t.Fatalf("rate limit: %d", c)
	}
}

func TestCost(t *testing.T) {
	p := provider.DefaultPrices
	if c := provider.Cost(p, "openai", "gpt-4o-mini", 1_000_000, 1_000_000); c < 0.7499 || c > 0.7501 {
		t.Fatal(c)
	}
	if c := provider.Cost(p, "openai", "text-embedding-3-small", 1_000_000, 0); c != 0.02 {
		t.Fatal(c)
	}
}
