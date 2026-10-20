package provider

import (
	"context"
	"sync"
)

// Meter accumulates token usage reported by providers (API "usage" fields) for one request.
type Meter struct {
	mu            sync.Mutex
	Input, Output int
}

func (m *Meter) add(in, out int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.Input += in
	m.Output += out
	m.mu.Unlock()
}

type meterKey struct{}

func WithMeter(ctx context.Context) (context.Context, *Meter) {
	m := &Meter{}
	return context.WithValue(ctx, meterKey{}, m), m
}

func meterFrom(ctx context.Context) *Meter { m, _ := ctx.Value(meterKey{}).(*Meter); return m }

// Price per 1M tokens (USD). Estimates; configurable via DEVBRAIN_PRICES.
type Price struct{ In, Out float64 }

// DefaultPrices are configurable estimates (list prices at time of writing), not billing data.
var DefaultPrices = map[string]Price{
	"openai/text-embedding-3-small": {In: 0.02},
	"openai/gpt-4o-mini":            {In: 0.15, Out: 0.60},
}

func Cost(prices map[string]Price, prov, model string, in, out int) float64 {
	p := prices[prov+"/"+model]
	return (float64(in)*p.In + float64(out)*p.Out) / 1e6
}
