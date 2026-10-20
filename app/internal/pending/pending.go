// Package pending stores open pending items / TODOs extracted from conversations, with dedupe.
package pending

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"

	"devbrain/internal/store"
)

var fold = strings.NewReplacer("á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n", "à", "a", "è", "e", "ò", "o")

var nonWord = regexp.MustCompile(`[^\p{L}\p{N}]+`)

var stop = map[string]bool{"el": true, "la": true, "los": true, "las": true, "de": true, "del": true, "en": true, "y": true, "a": true, "que": true,
	"un": true, "una": true, "para": true, "por": true, "con": true, "al": true, "se": true, "lo": true, "the": true, "to": true, "of": true, "and": true}

// Words returns normalized (lowercase, accent-free) content words.
func Words(s string) map[string]bool {
	t := fold.Replace(strings.ToLower(s))
	w := map[string]bool{}
	for _, x := range nonWord.Split(t, -1) {
		if len(x) > 1 && !stop[x] {
			w[x] = true
		}
	}
	return w
}

func Jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	n := 0
	for k := range a {
		if b[k] {
			n++
		}
	}
	return float64(n) / float64(len(a)+len(b)-n)
}

// Fingerprint is stable under case, accents, punctuation and word order.
func Fingerprint(project, text string) string {
	ws := Words(text)
	l := make([]string, 0, len(ws))
	for k := range ws {
		l = append(l, k)
	}
	sort.Strings(l)
	h := sha256.Sum256([]byte(strings.ToLower(project) + "|" + strings.Join(l, " ")))
	return hex.EncodeToString(h[:16])
}

type Item struct {
	ID             string     `json:"id"`
	Project        string     `json:"project"`
	Text           string     `json:"text"`
	Status         string     `json:"status"`
	ConversationID string     `json:"conversation_id"`
	Title          string     `json:"source_title"`
	Date           *time.Time `json:"source_date,omitempty"`
	DocumentID     string     `json:"document_id,omitempty"`
	Occurrences    int        `json:"occurrences"`
}

type Source struct {
	Project, ConversationID, Title, DocumentURI, Source string
	Date                                                *time.Time
}

const dupThreshold = 0.6

// findSimilar returns the id/status of an item in the same project with the same fingerprint or ≥ threshold word overlap.
func findSimilar(ctx context.Context, q store.DBTX, tenantID, project, text string, statuses []string, th float64) (string, string, error) {
	var id, st string
	err := q.QueryRow(ctx, `SELECT id::text, status FROM pending_items WHERE tenant_id=$1::uuid AND fingerprint=$2`, tenantID, Fingerprint(project, text)).Scan(&id, &st)
	if err == nil {
		return id, st, nil
	}
	rows, err := q.Query(ctx, `SELECT id::text, status, text FROM pending_items WHERE tenant_id=$1::uuid AND project=$2 AND status = ANY($3::text[])`, tenantID, project, statuses)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	w := Words(text)
	best := 0.0
	for rows.Next() {
		var rid, rst, t string
		if rows.Scan(&rid, &rst, &t) == nil {
			if j := Jaccard(w, Words(t)); j >= th && j > best {
				best, id, st = j, rid, rst
			}
		}
	}
	return id, st, rows.Err()
}

// AddOpen inserts an open item or merges it into an existing similar one. Returns (id, duplicate).
func AddOpen(ctx context.Context, q store.DBTX, tenantID, text string, s Source) (string, bool, error) {
	text = strings.TrimSpace(text)
	id, st, err := findSimilar(ctx, q, tenantID, s.Project, text, []string{"open", "done"}, dupThreshold)
	if err != nil {
		return "", false, err
	}
	if id != "" {
		// Seen again: bump; a later mention after it was closed re-opens it only if the source is newer than done_at.
		_, err = q.Exec(ctx, `UPDATE pending_items SET occurrences=occurrences+1, updated_at=now(),
			status = CASE WHEN status='done' AND $2::timestamptz IS NOT NULL AND done_at IS NOT NULL AND $2::timestamptz > done_at THEN 'open' ELSE status END,
			source_date = GREATEST(source_date, $2::timestamptz),
			source_conversation_id = CASE WHEN $2::timestamptz >= COALESCE(source_date,'-infinity') THEN $3 ELSE source_conversation_id END
			WHERE id=$1::uuid`, id, s.Date, s.ConversationID)
		_ = st
		return id, true, err
	}
	src := s.Source
	if src == "" {
		src = "cursor"
	}
	err = q.QueryRow(ctx, `INSERT INTO pending_items (tenant_id, project, text, fingerprint, source, source_conversation_id, source_title, source_date, source_document_id)
		VALUES ($1::uuid,$2,$3,$4,$5,$6,$7,$8,(SELECT id FROM documents WHERE tenant_id=$1::uuid AND uri=$9 LIMIT 1))
		ON CONFLICT (tenant_id, fingerprint) DO UPDATE SET occurrences=pending_items.occurrences+1, updated_at=now() RETURNING id::text`,
		tenantID, s.Project, text, Fingerprint(s.Project, text), src, s.ConversationID, s.Title, s.Date, s.DocumentURI).Scan(&id)
	return id, false, err
}

// MarkDone closes the most similar open item in the project (lower threshold: phrasing of "done" differs). Returns id or "".
func MarkDone(ctx context.Context, q store.DBTX, tenantID, text string, s Source) (string, error) {
	id, _, err := findSimilar(ctx, q, tenantID, s.Project, text, []string{"open"}, 0.5)
	if err != nil || id == "" {
		return "", err
	}
	b, _ := json.Marshal(map[string]string{"conversation_id": s.ConversationID, "text": text})
	_, err = q.Exec(ctx, `UPDATE pending_items SET status='done', done_at=COALESCE($2::timestamptz, now()), done_reason=$3, updated_at=now() WHERE id=$1::uuid AND status='open'`, id, s.Date, string(b))
	return id, err
}

// List returns items (newest source first). project filter is a case-insensitive substring.
func List(ctx context.Context, q store.DBTX, project, status string, limit int) ([]Item, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if status == "" {
		status = "open"
	}
	rows, err := q.Query(ctx, `SELECT id::text, project, text, status, source_conversation_id, source_title, source_date, COALESCE(source_document_id::text,''), occurrences
		FROM pending_items WHERE ($1='' OR project ILIKE '%'||$1||'%') AND ($2='all' OR status=$2)
		ORDER BY source_date DESC NULLS LAST, updated_at DESC LIMIT $3`, project, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.Project, &it.Text, &it.Status, &it.ConversationID, &it.Title, &it.Date, &it.DocumentID, &it.Occurrences); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

var intent = regexp.MustCompile(`(?i)\b(pendientes?|tareas (abiertas|por hacer)|qu[eé] (me )?falta|por hacer|to-?dos?\b\s*(abiertos|pendientes)?)`)

// IsPendingQuestion detects questions like "¿qué pendientes tengo?". Spanish "todo" (= all) alone does not match.
func IsPendingQuestion(q string) bool {
	if strings.Contains(q, "TODO") {
		return true
	}
	m := intent.FindString(q)
	if m == "" {
		return false
	}
	l := strings.ToLower(m)
	return !strings.HasPrefix(l, "todo") || strings.Contains(l, "abierto") || strings.Contains(l, "pendiente")
}

// ParseLLM extracts {"open":[...],"done":[...]} from a model reply (tolerates code fences / prose around the JSON).
func ParseLLM(s string) (open, done []string) {
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j <= i {
		return nil, nil
	}
	var v struct {
		Open []json.RawMessage `json:"open"`
		Done []json.RawMessage `json:"done"`
	}
	if json.Unmarshal([]byte(s[i:j+1]), &v) != nil {
		return nil, nil
	}
	get := func(rs []json.RawMessage) []string {
		var o []string
		for _, r := range rs {
			var t string
			if json.Unmarshal(r, &t) != nil {
				var x struct{ Text string }
				if json.Unmarshal(r, &x) != nil {
					continue
				}
				t = x.Text
			}
			t = strings.TrimSpace(t)
			if len([]rune(t)) >= 8 && len(t) <= 600 {
				o = append(o, t)
			}
		}
		return o
	}
	return get(v.Open), get(v.Done)
}
