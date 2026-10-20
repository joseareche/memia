// Package provider defines embedding/chat providers and the guard that enforces
// tenant policy and redaction before any external call.
package provider

import (
	"context"
	"errors"
	"sort"
	"sync"
)

var ErrNoProvider = errors.New("provider not configured")

// Embedder turns texts into vectors. External() reports whether data leaves the infrastructure.
type Embedder interface {
	Name() string
	External() bool
	Embed(ctx context.Context, model string, dims int, texts []string) ([][]float32, error)
}

// Chatter produces an answer from a system + user prompt.
type Chatter interface {
	Name() string
	External() bool
	Chat(ctx context.Context, model, system, user string) (string, error)
}

type Router struct {
	mu    sync.RWMutex
	emb   map[string]Embedder
	chats map[string]Chatter
}

func NewRouter() *Router { return &Router{emb: map[string]Embedder{}, chats: map[string]Chatter{}} }

func (r *Router) Register(e Embedder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emb[e.Name()] = e
}

func (r *Router) RegisterChat(c Chatter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.chats[c.Name()] = c
}

func (r *Router) Get(name string) (Embedder, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if e, ok := r.emb[name]; ok {
		return e, nil
	}
	return nil, ErrNoProvider
}

func (r *Router) Chat(name string) (Chatter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if c, ok := r.chats[name]; ok {
		return c, nil
	}
	return nil, ErrNoProvider
}

func (r *Router) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.emb))
	for n := range r.emb {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
