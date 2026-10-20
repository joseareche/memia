package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fake struct {
	ext   bool
	calls int
	got   []string
}

func (f *fake) Name() string   { return "fake" }
func (f *fake) External() bool { return f.ext }
func (f *fake) Embed(_ context.Context, _ string, dims int, t []string) ([][]float32, error) {
	f.calls++
	f.got = append(f.got, t...)
	out := make([][]float32, len(t))
	for i := range out {
		out[i] = make([]float32, dims)
	}
	return out, nil
}
func (f *fake) Chat(_ context.Context, _, s, u string) (string, error) {
	f.calls++
	f.got = append(f.got, s, u)
	return "ok", nil
}

const secret = "use sk-proj-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789 and password=hunter2xyz"

func TestLocalOnlyRefusesExternal(t *testing.T) {
	f := &fake{ext: true}
	if _, _, err := GuardedEmbed(context.Background(), PolicyLocalOnly, f, "m", 3, []string{"x"}); !errors.Is(err, ErrPolicy) {
		t.Fatalf("want ErrPolicy, got %v", err)
	}
	if _, _, err := GuardedChat(context.Background(), PolicyLocalOnly, f, "m", "s", "u"); !errors.Is(err, ErrPolicy) {
		t.Fatalf("chat: want ErrPolicy, got %v", err)
	}
	if _, _, err := GuardedEmbed(context.Background(), "", f, "m", 3, []string{"x"}); !errors.Is(err, ErrPolicy) {
		t.Fatal("empty policy must fail closed")
	}
	if f.calls != 0 {
		t.Fatalf("external provider was called %d times", f.calls)
	}
}

func TestLocalOnlyAllowsLocal(t *testing.T) {
	f := &fake{ext: false}
	if _, _, err := GuardedEmbed(context.Background(), PolicyLocalOnly, f, "m", 3, []string{secret}); err != nil || f.calls != 1 {
		t.Fatalf("local provider should be allowed: %v", err)
	}
}

func TestRedactionBeforeExternal(t *testing.T) {
	f := &fake{ext: true}
	_, st, err := GuardedEmbed(context.Background(), PolicyExternalAllowed, f, "m", 3, []string{secret})
	if err != nil || st.Total() < 2 {
		t.Fatalf("err=%v stats=%v", err, st)
	}
	_, _, _ = GuardedChat(context.Background(), PolicyExternalAllowed, f, "m", secret, secret)
	for _, g := range f.got {
		if strings.Contains(g, "hunter2xyz") || strings.Contains(g, "sk-proj-AbCd") {
			t.Fatalf("secret reached provider: %q", g)
		}
	}
}

func fakeServer(t *testing.T, check func(path string, body map[string]any), resp string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		check(r.URL.Path, m)
		_, _ = io.WriteString(w, resp)
	}))
}

func TestAdapters(t *testing.T) {
	ctx := context.Background()
	s := fakeServer(t, func(p string, m map[string]any) {
		if p != "/embeddings" || m["dimensions"].(float64) != 2 {
			t.Errorf("openai req %s %v", p, m)
		}
	}, `{"data":[{"index":1,"embedding":[3,4]},{"index":0,"embedding":[1,2]}]}`)
	o := NewOpenAI("k")
	o.Base = s.URL
	v, err := o.Embed(ctx, "m", 2, []string{"a", "b"})
	if err != nil || v[0][0] != 1 || v[1][0] != 3 {
		t.Fatalf("openai: %v %v", v, err)
	}
	s.Close()

	s = fakeServer(t, func(p string, m map[string]any) {
		if !strings.HasSuffix(p, "/models/gm:batchEmbedContents") {
			t.Errorf("gemini path %s", p)
		}
	}, `{"embeddings":[{"values":[1,2]}]}`)
	g := NewGemini("k")
	g.Base = s.URL
	if v, err := g.Embed(ctx, "gm", 2, []string{"a"}); err != nil || len(v[0]) != 2 {
		t.Fatalf("gemini: %v %v", v, err)
	}
	s.Close()

	s = fakeServer(t, func(p string, m map[string]any) {
		if p != "/api/embed" {
			t.Errorf("ollama path %s", p)
		}
	}, `{"embeddings":[[1,2]]}`)
	if v, err := NewOllama(s.URL).Embed(ctx, "nomic", 2, []string{"a"}); err != nil || len(v) != 1 {
		t.Fatalf("ollama: %v %v", v, err)
	}
	s.Close()

	s = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"error":"Incorrect API key provided: sk-proj-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"}`)
	}))
	o.Base = s.URL
	if _, err := o.Embed(ctx, "m", 2, []string{"a"}); err == nil || strings.Contains(err.Error(), "sk-proj-AbCd") {
		t.Fatalf("error must be redacted: %v", err)
	}
	s.Close()
}

func TestRouter(t *testing.T) {
	r := NewRouter()
	if _, err := r.Get("openai"); !errors.Is(err, ErrNoProvider) {
		t.Fatal("expected ErrNoProvider")
	}
	r.Register(&fake{})
	if _, err := r.Get("fake"); err != nil {
		t.Fatal(err)
	}
}
