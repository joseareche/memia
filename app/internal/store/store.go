// Package store holds DB helpers shared by the API and the admin CLI.
package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"devbrain/internal/provider"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Index struct {
	ID, Name, Provider, Model, ModelVersion, Status string
	Dims                                            int
}

const indexCols = "id::text, name, provider, model, model_version, status, dims"

func scanIndex(r pgx.Row) (*Index, error) {
	var i Index
	err := r.Scan(&i.ID, &i.Name, &i.Provider, &i.Model, &i.ModelVersion, &i.Status, &i.Dims)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &i, nil
}

// ActiveIndex returns the active index, or nil if none.
func ActiveIndex(ctx context.Context, q DBTX) (*Index, error) {
	return scanIndex(q.QueryRow(ctx, "SELECT "+indexCols+" FROM embedding_indexes WHERE status='active'"))
}

func IndexByName(ctx context.Context, q DBTX, name string) (*Index, error) {
	return scanIndex(q.QueryRow(ctx, "SELECT "+indexCols+" FROM embedding_indexes WHERE name=$1", name))
}

func ListIndexes(ctx context.Context, q DBTX) ([]Index, error) {
	rows, err := q.Query(ctx, "SELECT "+indexCols+" FROM embedding_indexes ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Index
	for rows.Next() {
		var i Index
		if err := rows.Scan(&i.ID, &i.Name, &i.Provider, &i.Model, &i.ModelVersion, &i.Status, &i.Dims); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// TenantPolicy defaults to external_allowed when no settings row exists.
func TenantPolicy(ctx context.Context, q DBTX, tenantID string) (provider.Policy, error) {
	var p string
	err := q.QueryRow(ctx, "SELECT COALESCE((SELECT provider_policy FROM tenant_settings WHERE tenant_id=$1::uuid), 'external_allowed')", tenantID).Scan(&p)
	return provider.Policy(p), err
}

// VecLiteral formats a pgvector text literal.
func VecLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

func ParseVec(s string) ([]float32, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' {
		return nil, fmt.Errorf("bad vector literal")
	}
	parts := strings.Split(s[1:len(s)-1], ",")
	out := make([]float32, len(parts))
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return nil, err
		}
		out[i] = float32(f)
	}
	return out, nil
}

// HNSWIndexName is the per-registry-index partial HNSW index name.
func HNSWIndexName(id string) string {
	return "ce_hnsw_" + strings.ReplaceAll(id, "-", "")[:16]
}

// Chunk splits text into ~maxLen chunks on paragraph/line boundaries.
func Chunk(text string, maxLen int) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		for len(para) > maxLen {
			cut := strings.LastIndexAny(para[:maxLen], "\n. ")
			if cut < maxLen/2 {
				cut = maxLen
			}
			for cut > 0 && !utf8.RuneStart(para[cut]) { // never split a multi-byte rune
				cut--
			}
			flush()
			cur.WriteString(para[:cut])
			flush()
			para = para[cut:]
		}
		if cur.Len()+len(para)+2 > maxLen {
			flush()
		}
		if cur.Len() > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(para)
	}
	flush()
	return out
}
