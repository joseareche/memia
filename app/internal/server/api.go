package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"devbrain/internal/embedsvc"
	"devbrain/internal/pending"
	"devbrain/internal/provider"
	"devbrain/internal/redact"
	"devbrain/internal/store"
	"devbrain/internal/tenant"

	"github.com/jackc/pgx/v5"
)

const maxText = 200_000

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid json"})
		return false
	}
	return true
}

func audit(r *http.Request, tx pgx.Tx, action string, detail map[string]any) {
	info, _ := tenant.From(r.Context())
	b, _ := json.Marshal(detail)
	_, _ = tx.Exec(r.Context(), "INSERT INTO audit_log (tenant_id, actor, action, detail) VALUES ($1::uuid,$2,$3,$4::jsonb)",
		info.TenantID, info.UserID, action, string(b))
}

// POST /v1/documents {title, source, project, text}
func (a *api) ingest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title, Source, Project, Text, URI string
		Metadata                          map[string]any
		ChunkSize                         int  `json:"chunk_size"`
		RedactStorage                     bool `json:"redact_storage"`
		ReplaceIfChanged                  bool `json:"replace_if_changed"` // same uri + different content: replace chunks (incremental sync)
	}
	if !decode(w, r, &in) {
		return
	}
	var storeRed redact.Stats
	if in.RedactStorage { // store only redacted text (title + body)
		var s2 redact.Stats
		in.Text, storeRed = redact.Redact(in.Text)
		in.Title, s2 = redact.Redact(in.Title)
		storeRed.Add(s2)
	}
	if in.ChunkSize < 400 || in.ChunkSize > 4000 {
		in.ChunkSize = 1200
	}
	if in.Metadata == nil {
		in.Metadata = map[string]any{}
	}
	sum := sha256.Sum256([]byte(in.Title + "\x00" + in.Text))
	in.Metadata["content_sha"] = hex.EncodeToString(sum[:16])
	meta, _ := json.Marshal(in.Metadata)
	if strings.TrimSpace(in.Text) == "" || len(in.Text) > maxText {
		writeJSON(w, 400, map[string]string{"error": fmt.Sprintf("text required, max %d chars", maxText)})
		return
	}
	if in.Source == "" {
		in.Source = "api"
	}
	createdAt := time.Now().UTC().Format(time.RFC3339)
	if in.Source == "chat" { // chat UI "Guardar nota" (older clients)
		in.Source = "note"
	}
	if in.Source == "note" {
		in.Metadata["kind"] = "note"
		in.Metadata["created_at"] = createdAt
		meta, _ = json.Marshal(in.Metadata)
	}
	info, _ := tenant.From(r.Context())
	chunks := store.Chunk(in.Text, in.ChunkSize)
	var docID string
	var chunkIDs []string
	var existing, replaced bool
	err := tenant.WithTx(r.Context(), a.d.DB, func(tx pgx.Tx) error {
		if in.URI != "" { // idempotent by uri
			var oldSha string
			err := tx.QueryRow(r.Context(), `SELECT id::text, COALESCE(metadata->>'content_sha','') FROM documents WHERE uri=$1`, in.URI).Scan(&docID, &oldSha)
			if err == nil {
				if !in.ReplaceIfChanged || oldSha == in.Metadata["content_sha"] {
					existing = true
					return nil
				}
				// changed: drop old chunks (embeddings cascade) and re-chunk into the same document id
				if _, err := tx.Exec(r.Context(), `DELETE FROM chunks WHERE document_id=$1::uuid`, docID); err != nil {
					return err
				}
				if _, err := tx.Exec(r.Context(), `DELETE FROM documents WHERE id=$1::uuid`, docID); err != nil {
					return err
				}
				replaced = true
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var projID *string
		if in.Project != "" {
			var p string
			if err := tx.QueryRow(r.Context(), `INSERT INTO projects (tenant_id, name) VALUES ($1::uuid,$2)
				ON CONFLICT (tenant_id, name) DO UPDATE SET name=EXCLUDED.name RETURNING id::text`, info.TenantID, in.Project).Scan(&p); err != nil {
				return err
			}
			projID = &p
		}
		var uri *string
		if in.URI != "" {
			uri = &in.URI
		}
		if err := tx.QueryRow(r.Context(), `INSERT INTO documents (tenant_id, project_id, source, title, uri, metadata) VALUES ($1::uuid,$2::uuid,$3,$4,$5,$6::jsonb) RETURNING id::text`,
			info.TenantID, projID, in.Source, in.Title, uri, string(meta)).Scan(&docID); err != nil {
			return err
		}
		for i, c := range chunks {
			var id string
			if err := tx.QueryRow(r.Context(), `INSERT INTO chunks (tenant_id, document_id, ord, content) VALUES ($1::uuid,$2::uuid,$3,$4) RETURNING id::text`,
				info.TenantID, docID, i, c).Scan(&id); err != nil {
				return err
			}
			chunkIDs = append(chunkIDs, id)
		}
		audit(r, tx, "ingest", map[string]any{"document_id": docID, "chunks": len(chunks)})
		return nil
	})
	if err != nil {
		a.fail(w, "ingest", err)
		return
	}
	if existing {
		writeJSON(w, 200, map[string]any{"document_id": docID, "skipped": true, "source": in.Source})
		return
	}
	out := map[string]any{"document_id": docID, "chunks": len(chunks), "embedded": false, "storage_redactions": storeRed, "replaced": replaced,
		"source": in.Source, "title": in.Title, "project": in.Project, "created_at": createdAt}
	err = tenant.WithTx(r.Context(), a.d.DB, func(tx pgx.Tx) error {
		idx, pol, err := a.indexAndPolicy(r, tx)
		if err != nil {
			return err
		}
		if idx == nil {
			out["note"] = "no active index: stored for full-text only"
			return nil
		}
		res, err := a.embed(r, tx, "embed_ingest", info.TenantID, pol, idx, chunks)
		if errors.Is(err, provider.ErrPolicy) {
			out["note"] = "tenant policy local_only: not sent to external provider; full-text only"
			return nil
		}
		if err != nil {
			return err
		}
		out["embedded"], out["index"], out["redactions"], out["cache_hits"] = true, idx.Name, res.Redactions, res.CacheHits
		return embedsvc.StoreChunkEmbeddings(r.Context(), tx, info.TenantID, idx.ID, chunkIDs, res.Vectors)
	})
	if err != nil {
		// Document stays stored; embeddings can be backfilled with `devbrain admin index build`.
		a.d.Log.Error("ingest embed", "err", err)
		out["note"] = "stored, embedding failed (backfill with admin index build)"
	}
	writeJSON(w, 201, out)
}

func (a *api) indexAndPolicy(r *http.Request, tx pgx.Tx) (*store.Index, provider.Policy, error) {
	info, _ := tenant.From(r.Context())
	idx, err := store.ActiveIndex(r.Context(), tx)
	if err != nil {
		return nil, "", err
	}
	pol, err := store.TenantPolicy(r.Context(), tx, info.TenantID)
	return idx, pol, err
}

type hit struct {
	ChunkID        string  `json:"chunk_id"`
	DocumentID     string  `json:"document_id"`
	Ord            int     `json:"chunk_ord"`
	Title          string  `json:"title"`
	Project        string  `json:"project"`
	Source         string  `json:"source"`
	URI            string  `json:"uri,omitempty"`
	ConversationID string  `json:"conversation_id,omitempty"`
	Date           string  `json:"date,omitempty"`
	Content        string  `json:"content"`
	Score          float64 `json:"score"`
}

// Diagnostics explains what was searched and why results may be missing.
type Diagnostics struct {
	Searched       []string `json:"searched"`
	Policy         string   `json:"policy"`
	TenantDocs     int64    `json:"tenant_documents"`
	TenantChunks   int64    `json:"tenant_chunks"`
	MinScore       float64  `json:"min_score"`
	BestScore      *float64 `json:"best_score,omitempty"`
	BelowThreshold int      `json:"below_threshold"`
	Filters        []string `json:"filters,omitempty"`
	Reason         string   `json:"reason,omitempty"`
}

type searchOut struct {
	Mode        string       `json:"mode"`
	Index       string       `json:"index,omitempty"`
	Note        string       `json:"note,omitempty"`
	Redactions  redact.Stats `json:"redactions,omitempty"`
	Hits        []hit        `json:"hits"`
	Diagnostics Diagnostics  `json:"diagnostics"`
}

var errPolicyExplicit = errors.New("policy")

const hitCols = `c.id::text, c.document_id::text, c.ord, COALESCE(d.title,''), COALESCE(p.name,''), d.source, COALESCE(d.uri,''),
	COALESCE(d.metadata->>'conversation_id',''), COALESCE(d.metadata->>'created_at', to_char(d.created_at,'YYYY-MM-DD"T"HH24:MI:SS"Z"')), c.content`

// filterSQL builds optional project/source filters (positional args start at n).
func filterSQL(q queryIn, n int) (string, []any) {
	var w []string
	var args []any
	if q.Project != "" { // personal notes stay searchable under any project filter
		args = append(args, q.Project)
		i := n + len(args) - 1
		w = append(w, fmt.Sprintf("(p.name = $%d OR d.source = 'note' OR ($%d = '(sin proyecto)' AND d.project_id IS NULL))", i, i))
	}
	if q.Source != "" {
		args = append(args, q.Source)
		w = append(w, fmt.Sprintf("d.source = $%d", n+len(args)-1))
	}
	if len(w) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(w, " AND "), args
}

// search: mode "semantic" | "fulltext" | "" (auto: semantic, then fulltext if nothing passes the threshold).
func (a *api) search(r *http.Request, tx pgx.Tx, q queryIn) (*searchOut, error) {
	info, _ := tenant.From(r.Context())
	out := &searchOut{Hits: []hit{}}
	dg := &out.Diagnostics
	dg.MinScore = q.MinScore
	if q.Project != "" {
		dg.Filters = append(dg.Filters, "project="+q.Project)
	}
	if q.Source != "" {
		dg.Filters = append(dg.Filters, "source="+q.Source)
	}
	_ = tx.QueryRow(r.Context(), "SELECT (SELECT count(*) FROM documents), (SELECT count(*) FROM chunks)").Scan(&dg.TenantDocs, &dg.TenantChunks)
	idx, pol, err := a.indexAndPolicy(r, tx)
	if err != nil {
		return nil, err
	}
	dg.Policy = string(pol)
	fsql, fargs := filterSQL(q, 3)
	if q.Mode != "fulltext" {
		switch {
		case idx == nil:
			out.Note = "no active index"
			dg.Searched = append(dg.Searched, "semantic: skipped (no active index)")
		default:
			res, err := a.embed(r, tx, "embed_search", info.TenantID, pol, idx, []string{q.Query})
			if errors.Is(err, provider.ErrPolicy) {
				if q.Mode == "semantic" {
					return nil, errPolicyExplicit
				}
				out.Note = "tenant policy local_only: semantic search needs an external provider; used full-text"
				dg.Searched = append(dg.Searched, "semantic: skipped (tenant policy local_only)")
				break
			}
			if err != nil {
				return nil, err
			}
			out.Mode, out.Index, out.Redactions = "semantic", idx.Name, res.Redactions
			dg.Searched = append(dg.Searched, "semantic: pgvector index "+idx.Name)
			v := store.VecLiteral(res.Vectors[0])
			// No filters: HNSW top-100 candidates, then re-rank with knowledge/recipe boost.
			// With filters: exact scan restricted by the filter (small subsets; avoids losing results post-filter).
			cand := fmt.Sprintf(`SELECT ce.chunk_id, ce.embedding::vector(%[1]d) <=> $1::vector(%[1]d) AS dist FROM chunk_embeddings ce
				WHERE ce.index_id = '%[2]s' ORDER BY ce.embedding::vector(%[1]d) <=> $1::vector(%[1]d) LIMIT 100) UNION (
				SELECT ce.chunk_id, ce.embedding::vector(%[1]d) <=> $1::vector(%[1]d) FROM chunk_embeddings ce JOIN chunks c ON c.id=ce.chunk_id
				JOIN documents d ON d.id=c.document_id WHERE ce.index_id = '%[2]s' AND d.source = 'note'`, idx.Dims, idx.ID)
			if fsql != "" {
				cand = fmt.Sprintf(`SELECT ce.chunk_id, ce.embedding::vector(%[1]d) <=> $1::vector(%[1]d) AS dist FROM chunk_embeddings ce
					JOIN chunks c ON c.id=ce.chunk_id JOIN documents d ON d.id=c.document_id LEFT JOIN projects p ON p.id=d.project_id
					WHERE ce.index_id = '%[2]s'%[3]s`, idx.Dims, idx.ID, fsql)
			}
			sql := `WITH cand AS ((` + cand + `)) SELECT ` + hitCols + `, 1 - cand.dist AS score
				FROM cand JOIN chunks c ON c.id=cand.chunk_id JOIN documents d ON d.id=c.document_id LEFT JOIN projects p ON p.id=d.project_id
				ORDER BY cand.dist - CASE WHEN d.source IN ('knowledge','recipe','note') THEN 0.05 ELSE 0 END LIMIT $2`
			if err := scanHits(r, tx, out, sql, append([]any{v, q.K * 2}, fargs...)...); err != nil {
				return nil, err
			}
			out.Hits = applyThreshold(out, q.MinScore, q.K)
			if len(out.Hits) > 0 || q.Mode == "semantic" {
				if len(out.Hits) == 0 {
					dg.Reason = reason(dg)
				}
				return out, nil
			}
		}
	}
	out.Mode = "fulltext"
	dg.Searched = append(dg.Searched, "fulltext: PostgreSQL tsvector (simple)")
	sql := `SELECT ` + hitCols + `, ts_rank(c.tsv, q)::float8 FROM chunks c JOIN documents d ON d.id=c.document_id LEFT JOIN projects p ON p.id=d.project_id,
		websearch_to_tsquery('simple', $1) q WHERE c.tsv @@ q` + fsql + ` ORDER BY ts_rank(c.tsv, q) + CASE WHEN d.source IN ('knowledge','recipe','note') THEN 0.05 ELSE 0 END DESC LIMIT $2`
	if err := scanHits(r, tx, out, sql, append([]any{q.Query, q.K}, fargs...)...); err != nil {
		return nil, err
	}
	if len(out.Hits) == 0 {
		dg.Reason = reason(dg)
	}
	return out, nil
}

func applyThreshold(out *searchOut, min float64, k int) []hit {
	keep := []hit{}
	for _, h := range out.Hits {
		if out.Diagnostics.BestScore == nil || h.Score > *out.Diagnostics.BestScore {
			s := h.Score
			out.Diagnostics.BestScore = &s
		}
		if h.Score < min {
			out.Diagnostics.BelowThreshold++
			continue
		}
		if len(keep) < k {
			keep = append(keep, h)
		}
	}
	return keep
}

func reason(d *Diagnostics) string {
	switch {
	case d.TenantDocs == 0:
		return "empty tenant: no documents ingested"
	case d.BelowThreshold > 0:
		return fmt.Sprintf("%d candidates below similarity threshold %.2f (best %.3f)", d.BelowThreshold, d.MinScore, *d.BestScore)
	case len(d.Filters) > 0:
		return "no documents match the filters " + strings.Join(d.Filters, ",")
	default:
		return "no matching chunks"
	}
}

func scanHits(r *http.Request, tx pgx.Tx, out *searchOut, sql string, args ...any) error {
	rows, err := tx.Query(r.Context(), sql, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.ChunkID, &h.DocumentID, &h.Ord, &h.Title, &h.Project, &h.Source, &h.URI, &h.ConversationID, &h.Date, &h.Content, &h.Score); err != nil {
			return err
		}
		out.Hits = append(out.Hits, h)
	}
	return rows.Err()
}

type queryIn struct {
	Query      string   `json:"query"`
	Mode       string   `json:"mode"`
	K          int      `json:"k"`
	Project    string   `json:"project"`
	Source     string   `json:"source"`
	MinScoreIn *float64 `json:"min_score"`
	MinScore   float64  `json:"-"`
}

// POST /v1/search {query, mode, k}
func (a *api) searchHandler(w http.ResponseWriter, r *http.Request) {
	var in queryIn
	if !decode(w, r, &in) {
		return
	}
	in.norm()
	if strings.TrimSpace(in.Query) == "" {
		writeJSON(w, 400, map[string]string{"error": "query required"})
		return
	}
	var out *searchOut
	err := tenant.WithTx(r.Context(), a.d.DB, func(tx pgx.Tx) error {
		var err error
		out, err = a.search(r, tx, in)
		if err == nil {
			audit(r, tx, "search", map[string]any{"mode": out.Mode, "hits": len(out.Hits)})
		}
		return err
	})
	if errors.Is(err, errPolicyExplicit) {
		writeJSON(w, 403, map[string]string{"error": "policy_local_only", "detail": "semantic search requires an external provider"})
		return
	}
	if err != nil {
		a.fail(w, "search", err)
		return
	}
	writeJSON(w, 200, out)
}

const systemPrompt = `Eres el asistente de Memia: amable, cercano y profesional. Responde en el idioma de la pregunta (por defecto, español).
Identidad: si te preguntan quién eres, qué modelo, proveedor, empresa o tecnología usas, responde solo que eres el asistente de Memia y ofrece ayuda. Nunca menciones nombres de modelos, proveedores de IA, empresas de IA ni nombres internos del sistema.
Para preguntas técnicas o de datos, responde SOLO con la información del CONTEXTO; si no basta, dilo con amabilidad. Cita las fuentes como [n].
Algunos datos aparecen como [REDACTED]/[EMAIL]/[PHONE]: no intentes reconstruirlos.`

func excerpt(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

func sourcesOf(hs []hit) ([]map[string]any, string) {
	srcs := make([]map[string]any, len(hs))
	var md strings.Builder
	for i, h := range hs {
		date := h.Date
		if len(date) > 10 {
			date = date[:10]
		}
		srcs[i] = map[string]any{"n": i + 1, "title": h.Title, "project": h.Project, "date": date, "source": h.Source,
			"document_id": h.DocumentID, "chunk_id": h.ChunkID, "chunk_ord": h.Ord, "conversation_id": h.ConversationID,
			"uri": h.URI, "score": h.Score, "excerpt": excerpt(h.Content, 280)}
		ref := "doc " + h.DocumentID[:8] + " · chunk " + h.ChunkID[:8]
		if h.ConversationID != "" {
			ref += " · conv " + h.ConversationID[:8]
		}
		fmt.Fprintf(&md, "[%d] %s — %s — %s — %s (%s)\n", i+1, h.Title, orDash(h.Project), orDash(date), h.Source, ref)
	}
	return srcs, md.String()
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// POST /v1/answer {query, k, project, source, min_score, mode: ""|"memory"}:
// retrieval, then LLM only when there is context. Always returns sources (with ids) or diagnostics.
func (a *api) answer(w http.ResponseWriter, r *http.Request) {
	var in queryIn
	if !decode(w, r, &in) {
		return
	}
	in.norm()
	memoryOnly := in.Mode == "memory"
	in.Mode = ""
	if strings.TrimSpace(in.Query) == "" {
		writeJSON(w, 400, map[string]string{"error": "query required"})
		return
	}
	if pending.IsPendingQuestion(in.Query) { // "¿qué pendientes tengo?": answered from the pending_items table, no LLM
		items, total, err := a.listPending(r.Context(), in.Project, "open", 60)
		if err != nil {
			a.fail(w, "answer", err)
			return
		}
		ans, srcs := renderPending(sortByProject(items), in.Project, total)
		writeJSON(w, 200, map[string]any{"answer": ans, "answered_by": "memory", "sources": srcs, "retrieval": "pending",
			"diagnostics": Diagnostics{Searched: []string{"pending_items (status=open)"}, Reason: map[bool]string{true: "no open pending items", false: ""}[len(items) == 0]},
			"usage":       map[string]any{"input_tokens": 0, "output_tokens": 0, "cost_usd": 0.0}})
		return
	}
	if n := recencyQuestion(in.Query); n > 0 { // "lo último que guardé": newest notes by created_at, not similarity
		a.answerRecent(w, r, in, n, memoryOnly)
		return
	}
	info, _ := tenant.From(r.Context())
	c, cerr := a.d.Providers.Chat(a.d.ChatProvider)
	if cerr != nil && !memoryOnly {
		memoryOnly = true // no LLM configured: degrade to memory-only
	}
	var out map[string]any
	err := tenant.WithTx(r.Context(), a.d.DB, func(tx pgx.Tx) error {
		pol, err := store.TenantPolicy(r.Context(), tx, info.TenantID)
		if err != nil {
			return err
		}
		sr, err := a.search(r, tx, in)
		if err != nil {
			return err
		}
		srcs, cites := sourcesOf(sr.Hits)
		out = map[string]any{"sources": srcs, "citations": cites, "retrieval": sr.Mode, "diagnostics": sr.Diagnostics,
			"usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "cost_usd": 0.0}}
		if len(sr.Hits) == 0 {
			out["answered_by"] = "memory"
			out["answer"] = "No encontré nada en la memoria para esta pregunta. Busqué en: " + strings.Join(sr.Diagnostics.Searched, "; ") +
				". Motivo: " + sr.Diagnostics.Reason + "."
			audit(r, tx, "answer", map[string]any{"retrieval": sr.Mode, "sources": 0})
			return nil
		}
		if memoryOnly || (c.External() && pol != provider.PolicyExternalAllowed) {
			var b strings.Builder
			b.WriteString("Fragmentos más relevantes de la memoria (sin IA):\n\n")
			for i, h := range sr.Hits {
				fmt.Fprintf(&b, "**[%d] %s**\n%s\n\n", i+1, h.Title, excerpt(h.Content, 600))
			}
			out["answered_by"], out["answer"] = "memory", b.String()
			if !memoryOnly {
				out["note"] = "tenant policy local_only: external AI not used"
			}
			audit(r, tx, "answer", map[string]any{"retrieval": sr.Mode, "sources": len(sr.Hits), "llm": false})
			return nil
		}
		var ctxb strings.Builder
		for i, h := range sr.Hits {
			fmt.Fprintf(&ctxb, "[%d] %s | proyecto: %s | fecha: %s\n%s\n\n", i+1, h.Title, orDash(h.Project), orDash(h.Date), h.Content)
		}
		mctx, m := provider.WithMeter(r.Context())
		t0 := time.Now()
		ans, st, err := provider.GuardedChat(mctx, pol, c, a.d.ChatModel, systemPrompt,
			"CONTEXTO:\n"+ctxb.String()+"\nPREGUNTA: "+in.Query)
		a.record(r.Context(), usageEvent{Op: "answer", Provider: c.Name(), Model: a.d.ChatModel, Items: 1, In: m.Input, Out: m.Output,
			Latency: time.Since(t0), Status: statusOf(err), ErrKind: errKind(err), Redactions: st})
		if err != nil {
			return err
		}
		st.Add(sr.Redactions)
		by := "local_model"
		if c.External() {
			by = "external_ai"
		}
		out["answer"], out["answered_by"], out["model"], out["redactions"] = ans, by, c.Name()+"/"+a.d.ChatModel, st
		out["usage"] = map[string]any{"input_tokens": m.Input, "output_tokens": m.Output,
			"cost_usd": provider.Cost(a.d.Prices, c.Name(), a.d.ChatModel, m.Input, m.Output)}
		audit(r, tx, "answer", map[string]any{"retrieval": sr.Mode, "sources": len(sr.Hits)})
		return nil
	})
	if errors.Is(err, errPolicyExplicit) {
		writeJSON(w, 403, map[string]string{"error": "policy_local_only"})
		return
	}
	if err != nil {
		a.fail(w, "answer", err)
		return
	}
	writeJSON(w, 200, out)
}

// GET /v1/projects: projects with document counts (for selectors).
func (a *api) projects(w http.ResponseWriter, r *http.Request) {
	var list []map[string]any
	err := tenant.WithTx(r.Context(), a.d.DB, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT p.name, count(d.id), max(d.created_at) FROM projects p LEFT JOIN documents d ON d.project_id=p.id
			GROUP BY p.name ORDER BY count(d.id) DESC, p.name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n string
			var c int64
			var t *time.Time
			if err := rows.Scan(&n, &c, &t); err != nil {
				return err
			}
			list = append(list, map[string]any{"name": n, "documents": c})
		}
		return rows.Err()
	})
	if err != nil {
		a.fail(w, "projects", err)
		return
	}
	if list == nil {
		list = []map[string]any{}
	}
	writeJSON(w, 200, map[string]any{"projects": list})
}

const distillMap = `Eres un analista técnico. Del siguiente extracto de conversaciones de desarrollo (texto ya redactado) del proyecto indicado, extrae SOLO conocimiento reutilizable y verificable, en español, en Markdown conciso con estas secciones (omite las vacías):
## Decisiones
## Arquitectura y configuración
## Hosts, servicios, rutas y puertos (sin secretos)
## Errores resueltos (síntoma → causa → solución)
## Comandos útiles
## Pendientes / TODO
No inventes. No incluyas secretos, contraseñas, tokens ni marcadores [REDACTED]. Sé denso: máximo ~600 palabras.`

const distillReduce = `Eres un analista técnico. Combina estas notas parciales del mismo proyecto en UNA base de conocimiento final en español, Markdown, deduplicada y ordenada, con las secciones: Resumen del proyecto (2-4 líneas), Decisiones, Arquitectura y configuración, Hosts/servicios/rutas/puertos (sin secretos), Errores resueltos (síntoma → causa → solución), Comandos útiles, Pendientes/TODO. Prefiere lo más reciente si hay contradicciones. No inventes ni incluyas secretos. Máximo ~1200 palabras.`

// POST /v1/distill {mode: map|reduce, project, text} — requires scope "distill". Records usage (op distill_*).
func (a *api) distill(w http.ResponseWriter, r *http.Request) {
	if !hasScope(r, "distill") {
		writeJSON(w, 403, map[string]string{"error": "distill scope required"})
		return
	}
	var in struct{ Mode, Project, Text string }
	if !decode(w, r, &in) {
		return
	}
	sys := distillMap
	if in.Mode == "reduce" {
		sys = distillReduce
	} else {
		in.Mode = "map"
	}
	if strings.TrimSpace(in.Text) == "" || len(in.Text) > 400_000 {
		writeJSON(w, 400, map[string]string{"error": "text required, max 400000 chars"})
		return
	}
	info, _ := tenant.From(r.Context())
	c, err := a.d.Providers.Chat(a.d.ChatProvider)
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "no chat provider configured"})
		return
	}
	var pol provider.Policy
	if err := tenant.WithTx(r.Context(), a.d.DB, func(tx pgx.Tx) error {
		var e error
		pol, e = store.TenantPolicy(r.Context(), tx, info.TenantID)
		return e
	}); err != nil {
		a.fail(w, "distill", err)
		return
	}
	mctx, m := provider.WithMeter(r.Context())
	t0 := time.Now()
	ans, st, err := provider.GuardedChat(mctx, pol, c, a.d.ChatModel, sys, "PROYECTO: "+in.Project+"\n\n"+in.Text)
	a.record(r.Context(), usageEvent{Op: "distill_" + in.Mode, Provider: c.Name(), Model: a.d.ChatModel, Items: 1, In: m.Input, Out: m.Output,
		Latency: time.Since(t0), Status: statusOf(err), ErrKind: errKind(err), Redactions: st})
	switch {
	case errors.Is(err, provider.ErrPolicy):
		writeJSON(w, 403, map[string]string{"error": "policy_local_only"})
		return
	case errKind(err) == "provider_rate_limit":
		writeJSON(w, 429, map[string]string{"error": "rate_limited"})
		return
	case err != nil:
		a.fail(w, "distill", err)
		return
	}
	writeJSON(w, 200, map[string]any{"summary": ans, "input_tokens": m.Input, "output_tokens": m.Output,
		"cost_usd": provider.Cost(a.d.Prices, c.Name(), a.d.ChatModel, m.Input, m.Output)})
}

func (a *api) fail(w http.ResponseWriter, op string, err error) {
	a.d.Log.Error(op, "err", err)
	writeJSON(w, 500, map[string]string{"error": "internal"})
}

const defaultMinScore = 0.30

func (q *queryIn) norm() {
	if q.K <= 0 || q.K > 20 {
		q.K = 5
	}
	q.MinScore = defaultMinScore
	if q.MinScoreIn != nil && *q.MinScoreIn >= 0 && *q.MinScoreIn <= 1 {
		q.MinScore = *q.MinScoreIn
	}
}
