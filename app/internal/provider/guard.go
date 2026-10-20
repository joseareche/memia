package provider

import (
	"context"
	"errors"

	"devbrain/internal/redact"
)

type Policy string

const (
	PolicyExternalAllowed Policy = "external_allowed"
	PolicyLocalOnly       Policy = "local_only"
)

var ErrPolicy = errors.New("tenant policy local_only forbids external providers")

// Prepare enforces policy and redacts texts destined to an external provider.
// Unknown/empty policy fails closed for external providers.
func Prepare(p Policy, external bool, texts []string) ([]string, redact.Stats, error) {
	st := redact.Stats{}
	if !external {
		return texts, st, nil
	}
	if p != PolicyExternalAllowed {
		return nil, st, ErrPolicy
	}
	out := make([]string, len(texts))
	for i, t := range texts {
		r, s := redact.Redact(t)
		out[i] = r
		st.Add(s)
	}
	return out, st, nil
}

// GuardedEmbed = Prepare + Embed (no cache).
func GuardedEmbed(ctx context.Context, p Policy, e Embedder, model string, dims int, texts []string) ([][]float32, redact.Stats, error) {
	sent, st, err := Prepare(p, e.External(), texts)
	if err != nil {
		return nil, st, err
	}
	v, err := e.Embed(ctx, model, dims, sent)
	return v, st, err
}

// GuardedChat enforces policy and redacts both prompts before an external call.
func GuardedChat(ctx context.Context, p Policy, c Chatter, model, system, user string) (string, redact.Stats, error) {
	sent, st, err := Prepare(p, c.External(), []string{system, user})
	if err != nil {
		return "", st, err
	}
	out, err := c.Chat(ctx, model, sent[0], sent[1])
	return out, st, err
}
