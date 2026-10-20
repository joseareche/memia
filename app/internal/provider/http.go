package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"devbrain/internal/redact"
)

var httpClient = &http.Client{Timeout: 90 * time.Second}

// postJSON sends body and decodes out. Error bodies are truncated and redacted
// (providers sometimes echo partial keys).
func postJSON(ctx context.Context, url string, hdr map[string]string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err = httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("provider request failed: %w", err)
		}
		retry := resp.StatusCode == 429 || resp.StatusCode >= 500
		if !retry || attempt >= 5 {
			break
		}
		wait := time.Duration(1<<attempt) * time.Second
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 && s < 60 {
			wait = time.Duration(s) * time.Second
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		red, _ := redact.Redact(string(msg))
		return fmt.Errorf("provider HTTP %d: %s", resp.StatusCode, red)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out)
}

func checkDims(v [][]float32, n, dims int) error {
	if len(v) != n {
		return fmt.Errorf("provider returned %d vectors, want %d", len(v), n)
	}
	for _, x := range v {
		if dims > 0 && len(x) != dims {
			return fmt.Errorf("provider returned %d dims, want %d", len(x), dims)
		}
	}
	return nil
}
