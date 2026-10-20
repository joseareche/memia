package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"devbrain/internal/pending"
	"devbrain/internal/provider"
	"devbrain/internal/store"
	"devbrain/internal/tenant"

	"github.com/jackc/pgx/v5"
)

const pendingPrompt = `Eres un asistente que lleva la lista de pendientes de un desarrollador. Lee la conversación (texto ya redactado) entre el usuario y un asistente de código y devuelve SOLO JSON válido:
{"open": ["..."], "done": ["..."]}
- "open": tareas o TODOs que quedaron ABIERTOS al final de la conversación (pedidos del usuario no terminados, siguientes pasos acordados, errores sin resolver, "falta…", "pendiente…", "luego hay que…"). Cada uno: una frase accionable en español, concreta (incluye host/servicio/archivo si aplica), máx. 200 caracteres.
- "done": tareas que la conversación muestra que se COMPLETARON (útil para cerrar pendientes previos), misma forma.
- Ignora tareas triviales del propio asistente, saludos y lo que ya se resolvió dentro de la conversación. No inventes. No incluyas secretos. Máximo 8 en cada lista. Si no hay, listas vacías.`

const pendingMaxChars = 60_000

type pendingSrcIn struct {
	Project        string `json:"project"`
	ConversationID string `json:"conversation_id"`
	Title          string `json:"title"`
	Date           string `json:"date"`
	DocumentURI    string `json:"document_uri"`
	Source         string `json:"source"`
	Text           string `json:"text"`
	// ReplaceConversation: this extraction is the latest view of the conversation; its earlier open items that were
	// not found again (and never seen elsewhere) are closed as superseded.
	ReplaceConversation bool `json:"replace_conversation"`
}

func parseTime(s string) *time.Time {
	for _, f := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(f, s); err == nil {
			return &t
		}
	}
	return nil
}

// POST /v1/pending/extract — scope "distill". LLM on redacted text; dedupes; closes items reported done.
func (a *api) pendingExtract(w http.ResponseWriter, r *http.Request) {
	if !hasScope(r, "distill") {
		writeJSON(w, 403, map[string]string{"error": "distill scope required"})
		return
	}
	var in pendingSrcIn
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Text) == "" || in.ConversationID == "" {
		writeJSON(w, 400, map[string]string{"error": "text and conversation_id required"})
		return
	}
	text := in.Text
	if len(text) > pendingMaxChars { // the end of a conversation says what is still open
		text = text[:8000] + "\n\n[…]\n\n" + text[len(text)-(pendingMaxChars-8000):]
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
		a.fail(w, "pending", err)
		return
	}
	mctx, m := provider.WithMeter(r.Context())
	t0 := time.Now()
	ans, st, err := provider.GuardedChat(mctx, pol, c, a.d.ChatModel, pendingPrompt,
		fmt.Sprintf("PROYECTO: %s\nCONVERSACIÓN: %s (%s)\n\n%s", orDash(in.Project), in.Title, in.Date, text))
	a.record(r.Context(), usageEvent{Op: "pending_extract", Provider: c.Name(), Model: a.d.ChatModel, Items: 1, In: m.Input, Out: m.Output,
		Latency: time.Since(t0), Status: statusOf(err), ErrKind: errKind(err), Redactions: st})
	cost := provider.Cost(a.d.Prices, c.Name(), a.d.ChatModel, m.Input, m.Output)
	switch {
	case errKind(err) == "provider_rate_limit":
		writeJSON(w, 429, map[string]string{"error": "rate_limited"})
		return
	case err != nil:
		if errKind(err) == "policy_local_only" {
			writeJSON(w, 403, map[string]string{"error": "policy_local_only"})
			return
		}
		a.fail(w, "pending", err)
		return
	}
	open, done := pending.ParseLLM(ans)
	src := pending.Source{Project: in.Project, ConversationID: in.ConversationID, Title: in.Title, DocumentURI: in.DocumentURI, Source: in.Source, Date: parseTime(in.Date)}
	res := map[string]any{"new": 0, "merged": 0, "closed": 0}
	var ids []string
	err = tenant.WithTx(r.Context(), a.d.DB, func(tx pgx.Tx) error {
		for _, t := range done {
			id, err := pending.MarkDone(r.Context(), tx, info.TenantID, t, src)
			if err != nil {
				return err
			}
			if id != "" {
				res["closed"] = res["closed"].(int) + 1
			}
		}
		for _, t := range open {
			id, dup, err := pending.AddOpen(r.Context(), tx, info.TenantID, t, src)
			if err != nil {
				return err
			}
			ids = append(ids, id)
			if dup {
				res["merged"] = res["merged"].(int) + 1
			} else {
				res["new"] = res["new"].(int) + 1
			}
		}
		if in.ReplaceConversation {
			if ids == nil {
				ids = []string{}
			}
			ct, err := tx.Exec(r.Context(), `UPDATE pending_items SET status='done', done_reason='superseded: re-extracted from updated conversation', done_at=now(), updated_at=now()
				WHERE source_conversation_id=$1 AND status='open' AND occurrences=1 AND NOT (id::text = ANY($2::text[]))`, in.ConversationID, ids)
			if err != nil {
				return err
			}
			res["superseded"] = int(ct.RowsAffected())
		}
		audit(r, tx, "pending_extract", map[string]any{"conversation_id": in.ConversationID, "open": len(open), "done": len(done)})
		return nil
	})
	if err != nil {
		a.fail(w, "pending", err)
		return
	}
	res["ids"], res["open_found"], res["done_found"], res["cost_usd"], res["input_tokens"], res["output_tokens"] = ids, len(open), len(done), cost, m.Input, m.Output
	writeJSON(w, 200, res)
}

func (a *api) listPending(ctx context.Context, project, status string, limit int) ([]pending.Item, int, error) {
	var out []pending.Item
	var total int
	err := tenant.WithTx(ctx, a.d.DB, func(tx pgx.Tx) error {
		var e error
		if out, e = pending.List(ctx, tx, project, status, limit); e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM pending_items WHERE ($1='' OR project ILIKE '%'||$1||'%') AND ($2='all' OR status=$2)`,
			project, map[bool]string{true: "open", false: status}[status == ""]).Scan(&total)
	})
	return out, total, err
}

// GET /v1/pending?project=&status=open|done|all&limit=
func (a *api) pendingHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	n, _ := strconv.Atoi(q.Get("limit"))
	items, total, err := a.listPending(r.Context(), q.Get("project"), q.Get("status"), n)
	if err != nil {
		a.fail(w, "pending", err)
		return
	}
	if items == nil {
		items = []pending.Item{}
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total})
}

func short8(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// renderPending formats items grouped by project (with date and source) and returns answer + sources.
func renderPending(items []pending.Item, project string, total int) (string, []map[string]any) {
	var b strings.Builder
	srcs := []map[string]any{}
	if len(items) == 0 {
		scope := "en ningún proyecto"
		if project != "" {
			scope = "para el proyecto «" + project + "»"
		}
		return "No tengo pendientes abiertos registrados " + scope + ". Busqué en: tabla de pendientes (extraídos de conversaciones de Cursor).", srcs
	}
	fmt.Fprintf(&b, "Tienes **%d pendientes abiertos**", total)
	if project != "" {
		fmt.Fprintf(&b, " en «%s»", project)
	}
	if total > len(items) {
		fmt.Fprintf(&b, " (muestro los %d más recientes; filtra por proyecto para ver el resto)", len(items))
	}
	b.WriteString(":\n")
	cur := "\x00"
	for i, it := range items {
		p := orDash(it.Project)
		if p != cur {
			fmt.Fprintf(&b, "\n**%s**\n", p)
			cur = p
		}
		date := "—"
		if it.Date != nil {
			date = it.Date.Format("2006-01-02")
		}
		fmt.Fprintf(&b, "- %s — _%s · conversación «%s» [%d]_\n", it.Text, date, excerpt(it.Title, 60), i+1)
		srcs = append(srcs, map[string]any{"n": i + 1, "title": it.Title, "project": it.Project, "date": date, "source": "pending",
			"document_id": it.DocumentID, "chunk_id": it.ID, "pending_id": it.ID, "conversation_id": it.ConversationID, "excerpt": it.Text, "score": 1.0})
	}
	return b.String(), srcs
}

// sortByProject keeps newest-first inside project groups, projects ordered by their newest item.
func sortByProject(items []pending.Item) []pending.Item {
	order := []string{}
	g := map[string][]pending.Item{}
	for _, it := range items {
		if _, ok := g[it.Project]; !ok {
			order = append(order, it.Project)
		}
		g[it.Project] = append(g[it.Project], it)
	}
	out := make([]pending.Item, 0, len(items))
	for _, p := range order {
		out = append(out, g[p]...)
	}
	return out
}
