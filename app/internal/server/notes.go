package server

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"devbrain/internal/provider"
	"devbrain/internal/store"
	"devbrain/internal/tenant"

	"github.com/jackc/pgx/v5"
)

var (
	recentRe = regexp.MustCompile(`(?i)(lo|la|el)\s+[uú]ltim[oa]s?\s+(que|nota|idea|cosa)|[uú]ltim[oa]s?\s+(\d+\s+)?(notas?|ideas?|apuntes?|cosas?\s+que\s+guard)|m[aá]s\s+recientes?\s+(que\s+guard|notas?)|qu[eé]\s+guard[eé]\s+(hoy|ayer|reci[eé]n|[uú]ltimamente|esta\s+semana)|notas?\s+(de\s+)?(hoy|ayer)|lo\s+que\s+(acabo\s+de\s+guardar|guard[eé]\s+(hoy|ayer|reci[eé]n))`)
	pluralRe = regexp.MustCompile(`(?i)[uú]ltim[oa]s\s+(\d+)?|notas\s+de|ideas`)
	numRe    = regexp.MustCompile(`\b(\d{1,2})\b`)
)

// recencyQuestion returns how many recent notes the question asks for (0 = not a recency question).
func recencyQuestion(q string) int {
	if !recentRe.MatchString(q) {
		return 0
	}
	if pluralRe.MatchString(q) {
		if m := numRe.FindStringSubmatch(q); m != nil {
			var n int
			fmt.Sscan(m[1], &n)
			if n >= 1 && n <= 10 {
				return n
			}
		}
		return 5
	}
	return 1
}

type noteDoc struct {
	ID, Title, Project, Text string
	Created                  time.Time
}

func latestNotes(r *http.Request, tx pgx.Tx, project string, n int) ([]noteDoc, error) {
	rows, err := tx.Query(r.Context(), `SELECT d.id::text, COALESCE(d.title,''), COALESCE(p.name,''), d.created_at,
			COALESCE((SELECT string_agg(c.content, E'\n' ORDER BY c.ord) FROM chunks c WHERE c.document_id=d.id),'')
		FROM documents d LEFT JOIN projects p ON p.id=d.project_id
		WHERE d.source='note' AND ($1='' OR $1='(sin proyecto)' OR p.name=$1 OR d.project_id IS NULL)
		ORDER BY d.created_at DESC LIMIT $2`, project, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []noteDoc
	for rows.Next() {
		var d noteDoc
		if err := rows.Scan(&d.ID, &d.Title, &d.Project, &d.Created, &d.Text); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

const recentPrompt = systemPrompt + `
El usuario pregunta por lo ÚLTIMO que guardó. El CONTEXTO contiene sus notas más recientes ordenadas de la más nueva a la más antigua, con su fecha de guardado. Responde sobre esas notas (empieza por la más reciente), cita [n] y menciona la fecha. No uses otra información.`

// answerRecent answers "lo último que guardé" from notes ordered by created_at (never by similarity).
func (a *api) answerRecent(w http.ResponseWriter, r *http.Request, in queryIn, n int, memoryOnly bool) {
	info, _ := tenant.From(r.Context())
	var notes []noteDoc
	var pol provider.Policy
	err := tenant.WithTx(r.Context(), a.d.DB, func(tx pgx.Tx) error {
		var e error
		if pol, e = store.TenantPolicy(r.Context(), tx, info.TenantID); e != nil {
			return e
		}
		notes, e = latestNotes(r, tx, in.Project, n)
		return e
	})
	if err != nil {
		a.fail(w, "answer", err)
		return
	}
	lima, _ := time.LoadLocation("America/Lima")
	srcs := []map[string]any{}
	var ctxb, list strings.Builder
	for i, d := range notes {
		when := d.Created.In(lima).Format("2006-01-02 15:04")
		srcs = append(srcs, map[string]any{"n": i + 1, "title": d.Title, "project": d.Project, "date": d.Created.In(lima).Format("2006-01-02"), "source": "note",
			"document_id": d.ID, "chunk_id": "", "conversation_id": "", "created_at": d.Created.UTC().Format(time.RFC3339), "excerpt": excerpt(d.Text, 280), "score": 1.0})
		fmt.Fprintf(&ctxb, "[%d] %s | guardada: %s (hora de Lima)\n%s\n\n", i+1, d.Title, when, excerpt(d.Text, 12000))
		fmt.Fprintf(&list, "**[%d] %s** — guardada el %s (doc %s)\n%s\n\n", i+1, d.Title, when, d.ID[:8], excerpt(d.Text, 600))
	}
	diag := Diagnostics{Searched: []string{"notas guardadas (source=note) ordenadas por fecha de creación"}}
	out := map[string]any{"sources": srcs, "retrieval": "recent_notes", "diagnostics": &diag,
		"usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "cost_usd": 0.0}, "answered_by": "memory"}
	if len(notes) == 0 {
		diag.Reason = "no saved notes yet"
		out["answer"] = "Todavía no tienes notas guardadas. Busqué en: notas guardadas ordenadas por fecha."
		writeJSON(w, 200, out)
		return
	}
	c, cerr := a.d.Providers.Chat(a.d.ChatProvider)
	if memoryOnly || cerr != nil || (c.External() && pol != provider.PolicyExternalAllowed) {
		out["answer"] = "Lo último que guardaste:\n\n" + list.String()
		writeJSON(w, 200, out)
		return
	}
	mctx, m := provider.WithMeter(r.Context())
	t0 := time.Now()
	ans, st, err := provider.GuardedChat(mctx, pol, c, a.d.ChatModel, recentPrompt, "CONTEXTO:\n"+ctxb.String()+"\nPREGUNTA: "+in.Query)
	a.record(r.Context(), usageEvent{Op: "answer", Provider: c.Name(), Model: a.d.ChatModel, Items: 1, In: m.Input, Out: m.Output,
		Latency: time.Since(t0), Status: statusOf(err), ErrKind: errKind(err), Redactions: st})
	if err != nil { // degrade to the raw list instead of failing
		out["answer"] = "Lo último que guardaste:\n\n" + list.String()
		writeJSON(w, 200, out)
		return
	}
	by := "local_model"
	if c.External() {
		by = "external_ai"
	}
	out["answer"], out["answered_by"], out["model"] = ans, by, c.Name()+"/"+a.d.ChatModel
	out["usage"] = map[string]any{"input_tokens": m.Input, "output_tokens": m.Output, "cost_usd": provider.Cost(a.d.Prices, c.Name(), a.d.ChatModel, m.Input, m.Output)}
	writeJSON(w, 200, out)
}
