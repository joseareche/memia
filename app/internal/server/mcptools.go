package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"devbrain/internal/learn"
	"devbrain/internal/mcp"
	"devbrain/internal/redact"
	"devbrain/internal/tenant"

	"github.com/jackc/pgx/v5"
)

var (
	readScopes  = []string{"mcp", "mcp:read"}
	writeScopes = []string{"mcp:write"}
)

func obj(props map[string]any, req ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(req) > 0 {
		m["required"] = req
	}
	return m
}

var (
	pStr = func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	pInt = func(d string) map[string]any {
		return map[string]any{"type": "integer", "description": d, "minimum": 1, "maximum": 20}
	}
)

func bad(msg string) error { return mcp.ToolError{Msg: msg} }

func ctxReq(ctx context.Context) *http.Request {
	return (&http.Request{Header: http.Header{}}).WithContext(ctx)
}

func fmtHit(b *strings.Builder, i int, h hit, max int) {
	date := h.Date
	if len(date) > 10 {
		date = date[:10]
	}
	fmt.Fprintf(b, "### [%d] %s\nproyecto: %s · fecha: %s · fuente: %s · score %.3f\nref: doc=%s chunk=%s#%d", i, h.Title, orDash(h.Project), orDash(date), h.Source, h.Score, h.DocumentID, h.ChunkID, h.Ord)
	if h.ConversationID != "" {
		fmt.Fprintf(b, " conversation=%s", h.ConversationID)
	}
	fmt.Fprintf(b, "\n\n%s\n\n", excerpt(h.Content, max))
}

func diagText(d Diagnostics) string {
	return fmt.Sprintf("Sin resultados. Buscado en: %s. Política: %s. Documentos del tenant: %d. Motivo: %s.",
		strings.Join(d.Searched, "; "), d.Policy, d.TenantDocs, d.Reason)
}

// MCPTools wires DevBrain tools into the MCP handler.
func MCPTools(d Deps) []mcp.Tool {
	a := &api{d: d, svc: newSvc(d)}
	runSearch := func(ctx context.Context, q queryIn) (*searchOut, error) {
		q.norm()
		var out *searchOut
		err := tenant.WithTx(ctx, d.DB, func(tx pgx.Tx) error {
			var e error
			out, e = a.search(ctx2req(ctx), tx, q)
			return e
		})
		if errors.Is(err, errPolicyExplicit) {
			return nil, bad("tenant policy local_only: semantic search unavailable")
		}
		return out, err
	}
	return []mcp.Tool{
		{Name: "search_history", Title: "Buscar en el historial", ReadOnly: true, Scopes: readScopes,
			Description: "Búsqueda semántica (pgvector) con fallback full-text en conversaciones de Cursor, conocimiento destilado y recetas. Devuelve fragmentos con proyecto, fecha y referencias (doc/chunk/conversación).",
			InputSchema: obj(map[string]any{"query": pStr("Qué buscar"), "project": pStr("Filtrar por proyecto exacto (opcional)"),
				"source": map[string]any{"type": "string", "enum": []string{"cursor", "knowledge", "recipe"}}, "k": pInt("Resultados (1-20, def. 5)"),
				"min_score": map[string]any{"type": "number", "minimum": 0, "maximum": 1, "description": "Umbral de similitud (def. 0.30)"}}, "query"),
			Call: func(ctx context.Context, raw json.RawMessage) (string, any, error) {
				var q queryIn
				if json.Unmarshal(raw, &q) != nil || strings.TrimSpace(q.Query) == "" {
					return "", nil, bad("query required")
				}
				out, err := runSearch(ctx, q)
				if err != nil {
					return "", nil, err
				}
				if len(out.Hits) == 0 {
					return diagText(out.Diagnostics), out, nil
				}
				var b strings.Builder
				fmt.Fprintf(&b, "%d resultados (%s):\n\n", len(out.Hits), out.Mode)
				for i, h := range out.Hits {
					fmtHit(&b, i+1, h, 700)
				}
				return b.String(), out, nil
			}},
		{Name: "get_conversation", Title: "Obtener conversación", ReadOnly: true, Scopes: readScopes,
			Description: "Devuelve el texto (redactado) de una conversación de Cursor por conversation_id, paginado por offset/max_chars.",
			InputSchema: obj(map[string]any{"conversation_id": pStr("UUID de la conversación"), "offset": map[string]any{"type": "integer", "minimum": 0},
				"max_chars": map[string]any{"type": "integer", "minimum": 1000, "maximum": 200000}}, "conversation_id"),
			Call: func(ctx context.Context, raw json.RawMessage) (string, any, error) {
				var in struct {
					ConversationID string `json:"conversation_id"`
					Offset         int    `json:"offset"`
					MaxChars       int    `json:"max_chars"`
				}
				if json.Unmarshal(raw, &in) != nil || in.ConversationID == "" {
					return "", nil, bad("conversation_id required")
				}
				if in.MaxChars == 0 {
					in.MaxChars = 60000
				}
				var title, proj, created, updated string
				var docs []string
				var full strings.Builder
				err := tenant.WithTx(ctx, d.DB, func(tx pgx.Tx) error {
					rows, err := tx.Query(ctx, `SELECT d.id::text, COALESCE(d.metadata->>'title',''), COALESCE(d.metadata->>'project',''),
						COALESCE(d.metadata->>'created_at',''), COALESCE(d.metadata->>'updated_at',''), string_agg(c.content, E'\n\n' ORDER BY c.ord)
						FROM documents d JOIN chunks c ON c.document_id=d.id WHERE d.metadata->>'conversation_id'=$1
						GROUP BY d.id ORDER BY COALESCE((d.metadata->>'part')::int,1)`, in.ConversationID)
					if err != nil {
						return err
					}
					defer rows.Close()
					for rows.Next() {
						var id, t string
						if err := rows.Scan(&id, &title, &proj, &created, &updated, &t); err != nil {
							return err
						}
						docs = append(docs, id)
						full.WriteString(t + "\n\n")
					}
					return rows.Err()
				})
				if err != nil {
					return "", nil, err
				}
				if len(docs) == 0 {
					return "", nil, bad("conversation not found in this tenant")
				}
				t := []rune(full.String())
				end := min(len(t), in.Offset+in.MaxChars)
				if in.Offset > len(t) {
					in.Offset = len(t)
				}
				meta := map[string]any{"conversation_id": in.ConversationID, "title": title, "project": proj, "created_at": created, "updated_at": updated,
					"document_ids": docs, "total_chars": len(t), "offset": in.Offset, "returned_chars": end - in.Offset, "has_more": end < len(t)}
				hdr := fmt.Sprintf("# %s\nproyecto: %s · %s → %s · docs: %s · chars %d-%d de %d\n\n", title, orDash(proj), created, updated, strings.Join(docs, ","), in.Offset, end, len(t))
				return hdr + string(t[in.Offset:end]), meta, nil
			}},
		{Name: "find_solution", Title: "Encontrar solución previa", ReadOnly: true, Scopes: readScopes,
			Description: "Para un error/problema, busca recetas (verificadas primero) y fragmentos de conversaciones pasadas con la solución y su código (prioriza fragmentos con bloques de código).",
			InputSchema: obj(map[string]any{"problem": pStr("Error, síntoma o tarea"), "project": pStr("Proyecto (opcional)"), "k": pInt("Fragmentos (def. 4)")}, "problem"),
			Call: func(ctx context.Context, raw json.RawMessage) (string, any, error) {
				var in struct {
					Problem, Project string
					K                int
				}
				if json.Unmarshal(raw, &in) != nil || strings.TrimSpace(in.Problem) == "" {
					return "", nil, bad("problem required")
				}
				if in.K <= 0 || in.K > 10 {
					in.K = 4
				}
				type rec struct {
					ID, Title, Project, Status, Problem, Solution, Code string
					Occurrences                                         int
					Rank                                                float64
				}
				var recs []rec
				err := tenant.WithTx(ctx, d.DB, func(tx pgx.Tx) error {
					q := `SELECT id::text, title, project, status, problem, solution, code, occurrences, ts_rank(tsv, q)::float8
						FROM recipes, websearch_to_tsquery('simple', $1) q WHERE tsv @@ q AND status <> 'rejected'`
					args := []any{in.Problem}
					if in.Project != "" {
						q += " AND project=$2"
						args = append(args, in.Project)
					}
					rows, err := tx.Query(ctx, q+` ORDER BY (status='verified') DESC, 9 DESC LIMIT 3`, args...)
					if err != nil {
						return err
					}
					defer rows.Close()
					for rows.Next() {
						var x rec
						if err := rows.Scan(&x.ID, &x.Title, &x.Project, &x.Status, &x.Problem, &x.Solution, &x.Code, &x.Occurrences, &x.Rank); err != nil {
							return err
						}
						recs = append(recs, x)
					}
					return rows.Err()
				})
				if err != nil {
					return "", nil, err
				}
				out, err := runSearch(ctx, queryIn{Query: in.Problem, Project: in.Project, K: 12})
				if err != nil {
					return "", nil, err
				}
				hits := out.Hits
				sort.SliceStable(hits, func(i, j int) bool { // prefer fragments that contain code
					bi, bj := hits[i].Score, hits[j].Score
					if strings.Contains(hits[i].Content, "```") {
						bi += 0.04
					}
					if strings.Contains(hits[j].Content, "```") {
						bj += 0.04
					}
					return bi > bj
				})
				if len(hits) > in.K {
					hits = hits[:in.K]
				}
				var b strings.Builder
				if len(recs) > 0 {
					b.WriteString("## Recetas\n\n")
					for _, x := range recs {
						fmt.Fprintf(&b, "### %s [%s, visto %d×] (recipe %s, proyecto %s)\n**Problema:** %s\n\n**Solución:** %s\n\n", x.Title, x.Status, x.Occurrences, x.ID, orDash(x.Project), excerpt(x.Problem, 600), excerpt(x.Solution, 1500))
						if x.Code != "" {
							b.WriteString(excerpt(x.Code, 3000) + "\n\n")
						}
					}
				}
				if len(hits) > 0 {
					b.WriteString("## Fragmentos de conversaciones / conocimiento\n\n")
					for i, h := range hits {
						fmtHit(&b, i+1, h, 2500)
					}
				}
				if len(recs) == 0 && len(hits) == 0 {
					return diagText(out.Diagnostics) + " Recetas: 0 coincidencias full-text.", out.Diagnostics, nil
				}
				return b.String(), map[string]any{"recipes": recs, "hits": hits, "diagnostics": out.Diagnostics}, nil
			}},
		{Name: "get_project_rules", Title: "Reglas y conocimiento del proyecto", ReadOnly: true, Scopes: readScopes,
			Description: "Devuelve el conocimiento destilado del proyecto (decisiones, configuración, hosts, errores resueltos, comandos, TODO) y sus recetas verificadas.",
			InputSchema: obj(map[string]any{"project": pStr("Nombre o parte del nombre del proyecto (p. ej. 'my-project')")}, "project"),
			Call: func(ctx context.Context, raw json.RawMessage) (string, any, error) {
				var in struct{ Project string }
				if json.Unmarshal(raw, &in) != nil || strings.TrimSpace(in.Project) == "" {
					return "", nil, bad("project required")
				}
				var b strings.Builder
				var docs []map[string]any
				var recipes []map[string]any
				err := tenant.WithTx(ctx, d.DB, func(tx pgx.Tx) error {
					rows, err := tx.Query(ctx, `SELECT d.id::text, d.title, COALESCE(p.name,''), string_agg(c.content, E'\n\n' ORDER BY c.ord)
						FROM documents d JOIN chunks c ON c.document_id=d.id LEFT JOIN projects p ON p.id=d.project_id
						WHERE d.source='knowledge' AND (p.name ILIKE '%'||$1||'%' OR d.title ILIKE '%'||$1||'%')
						GROUP BY d.id, p.name ORDER BY length(string_agg(c.content,'')) DESC LIMIT 3`, in.Project)
					if err != nil {
						return err
					}
					for rows.Next() {
						var id, t, p, txt string
						if err := rows.Scan(&id, &t, &p, &txt); err != nil {
							rows.Close()
							return err
						}
						docs = append(docs, map[string]any{"document_id": id, "title": t, "project": p})
						fmt.Fprintf(&b, "%s\n\n(ref: doc=%s)\n\n---\n\n", excerpt(txt, 12000), id)
					}
					rows.Close()
					rows, err = tx.Query(ctx, `SELECT id::text, title, status, occurrences FROM recipes WHERE project ILIKE '%'||$1||'%' AND status<>'rejected'
						ORDER BY (status='verified') DESC, occurrences DESC, updated_at DESC LIMIT 15`, in.Project)
					if err != nil {
						return err
					}
					defer rows.Close()
					for rows.Next() {
						var id, t, st string
						var n int
						if err := rows.Scan(&id, &t, &st, &n); err != nil {
							return err
						}
						recipes = append(recipes, map[string]any{"id": id, "title": t, "status": st, "occurrences": n})
					}
					return rows.Err()
				})
				if err != nil {
					return "", nil, err
				}
				if len(recipes) > 0 {
					b.WriteString("## Recetas del proyecto\n")
					for _, r := range recipes {
						fmt.Fprintf(&b, "- [%s] %s (×%d, recipe %s)\n", r["status"], r["title"], r["occurrences"], r["id"])
					}
				}
				if len(docs) == 0 && len(recipes) == 0 {
					return "", nil, bad("no hay conocimiento destilado ni recetas para '" + in.Project + "' (prueba search_history o el nombre exacto del proyecto)")
				}
				return b.String(), map[string]any{"knowledge_documents": docs, "recipes": recipes}, nil
			}},
		{Name: "get_pending", Title: "Pendientes abiertos", ReadOnly: true, Scopes: readScopes,
			Description: "Lista los pendientes/TODOs abiertos extraídos de las conversaciones (con proyecto, fecha y conversación origen). Responde a '¿qué pendientes tengo?'.",
			InputSchema: obj(map[string]any{"project": pStr("Filtra por proyecto (subcadena)"), "status": map[string]any{"type": "string", "enum": []string{"open", "done", "all"}},
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}),
			Call: func(ctx context.Context, raw json.RawMessage) (string, any, error) {
				var in struct {
					Project, Status string
					Limit           int
				}
				if len(raw) > 0 && json.Unmarshal(raw, &in) != nil {
					return "", nil, bad("invalid arguments")
				}
				items, total, err := a.listPending(ctx, in.Project, in.Status, in.Limit)
				if err != nil {
					return "", nil, err
				}
				txt, srcs := renderPending(sortByProject(items), in.Project, total)
				for _, s := range srcs {
					txt += fmt.Sprintf("\n[%v] ref: pending=%v conversation=%v doc=%v", s["n"], s["pending_id"], s["conversation_id"], s["document_id"])
				}
				return txt, map[string]any{"items": items, "total": total}, nil
			}},
		{Name: "save_verified_recipe", Title: "Guardar receta", ReadOnly: false, Scopes: writeScopes,
			Description: "Guarda una receta reutilizable. Queda 'unverified' salvo que se aporten AMBOS: evidence (prueba: salida de test, commit, URL) y approved_by (quién la aprobó). Se redacta antes de guardar; deduplica.",
			InputSchema: obj(map[string]any{"title": pStr("Título corto"), "problem": pStr("Problema/síntoma"), "solution": pStr("Solución explicada"),
				"code": pStr("Código/comandos"), "project": pStr("Proyecto"), "versions": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
				"files": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "commits": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"evidence": pStr("Evidencia de que funciona"), "approved_by": pStr("Persona que aprueba"), "conversation_id": pStr("Conversación origen")}, "title", "problem", "solution"),
			Call: func(ctx context.Context, raw json.RawMessage) (string, any, error) {
				var in struct {
					Title, Problem, Solution, Code, Project, Evidence, ConversationID string
					ApprovedBy                                                        string `json:"approved_by"`
					Versions                                                          map[string]string
					Files, Commits                                                    []string
				}
				if json.Unmarshal(raw, &in) != nil || in.Title == "" || in.Problem == "" || in.Solution == "" {
					return "", nil, bad("title, problem and solution required")
				}
				if len(in.Problem)+len(in.Solution)+len(in.Code) > 60000 {
					return "", nil, bad("recipe too large (max 60000 chars)")
				}
				st := redact.Stats{}
				rd := func(s string) string { r, x := redact.Redact(s); st.Add(x); return r }
				info, _ := tenant.From(ctx)
				var res learn.SaveResult
				err := tenant.WithTx(ctx, d.DB, func(tx pgx.Tx) error {
					var e error
					res, e = learn.Save(ctx, tx, info.TenantID, learn.SaveInput{Recipe: learn.Recipe{Title: rd(in.Title), Problem: rd(in.Problem), Solution: rd(in.Solution),
						Code: rd(in.Code), Versions: in.Versions, Files: in.Files, Commits: in.Commits}, Project: in.Project, ConversationID: in.ConversationID,
						Evidence: rd(in.Evidence), ApprovedBy: in.ApprovedBy, CreatedBy: "mcp"})
					if e == nil {
						audit(ctxReq(ctx), tx, "recipe_save", map[string]any{"id": res.ID, "status": res.Status, "duplicate": res.Duplicate})
					}
					return e
				})
				if err != nil {
					return "", nil, err
				}
				msg := fmt.Sprintf("Receta %s guardada con estado '%s'", res.ID, res.Status)
				if res.Duplicate {
					msg = fmt.Sprintf("Receta duplicada: fusionada con %s (estado '%s')", res.ID, res.Status)
				}
				if res.Status != "verified" {
					msg += ". Para marcarla verificada aporta evidence y approved_by."
				}
				if st.Total() > 0 {
					msg += fmt.Sprintf(" Redactados %d elementos sensibles.", st.Total())
				}
				return msg, map[string]any{"id": res.ID, "status": res.Status, "duplicate": res.Duplicate, "redactions": st}, nil
			}},
	}
}

func ctx2req(ctx context.Context) *http.Request { return ctxReq(ctx) }
