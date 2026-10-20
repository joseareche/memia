// Package metrics exposes a minimal Prometheus text endpoint (no external deps).
package metrics

import (
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"sync"
	"time"
)

type Registry struct {
	mu    sync.Mutex
	reqs  map[string]uint64
	start time.Time
}

func New() *Registry { return &Registry{reqs: map[string]uint64{}, start: time.Now()} }

type rec struct {
	http.ResponseWriter
	code int
}

func (r *rec) WriteHeader(c int) { r.code = c; r.ResponseWriter.WriteHeader(c) }

// Wrap counts requests by route pattern and status code.
func (m *Registry) Wrap(route string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &rec{ResponseWriter: w, code: 200}
		h.ServeHTTP(rw, r)
		m.mu.Lock()
		m.reqs[fmt.Sprintf("route=%q,code=\"%d\"", route, rw.code)]++
		m.mu.Unlock()
	})
}

func (m *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		m.mu.Lock()
		keys := make([]string, 0, len(m.reqs))
		for k := range m.reqs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintln(w, "# TYPE devbrain_http_requests_total counter")
		for _, k := range keys {
			fmt.Fprintf(w, "devbrain_http_requests_total{%s} %d\n", k, m.reqs[k])
		}
		m.mu.Unlock()
		fmt.Fprintln(w, "# TYPE devbrain_uptime_seconds gauge")
		fmt.Fprintf(w, "devbrain_uptime_seconds %.0f\n", time.Since(m.start).Seconds())
		fmt.Fprintln(w, "# TYPE go_goroutines gauge")
		fmt.Fprintf(w, "go_goroutines %d\n", runtime.NumGoroutine())
	})
}
