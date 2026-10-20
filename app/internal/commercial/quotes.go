package commercial

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type ItemIn struct {
	LineIn
	PriceSource json.RawMessage `json:"price_source,omitempty"` // {kind: manual|catalog|history, ref, valid}; Phase A: manual
}

type CustomerRef struct {
	Kind string        `json:"kind"`           // "local" | "free"
	ID   string        `json:"id,omitempty"`   // local
	Data *CustomerData `json:"data,omitempty"` // free (one-off snapshot, not persisted as a customer)
}

type QuoteIn struct {
	Title             *string      `json:"title"`
	Customer          *CustomerRef `json:"customer"`
	Currency          *string      `json:"currency"`
	PricesIncludeIGV  *bool        `json:"prices_include_igv"`
	IGVRate           *string      `json:"igv_rate"`
	GlobalDiscountPct *string      `json:"global_discount_pct"`
	ValidUntil        *string      `json:"valid_until"` // YYYY-MM-DD (Lima)
	Terms             *string      `json:"terms"`
	Items             *[]ItemIn    `json:"items"`
}

type Quote struct {
	ID                string          `json:"id"`
	Number            *string         `json:"number"`
	Status            string          `json:"status"`
	Title             string          `json:"title"`
	CustomerKind      string          `json:"customer_kind"`
	CustomerID        *string         `json:"customer_id"`
	Customer          json.RawMessage `json:"customer"`
	Currency          string          `json:"currency"`
	PricesIncludeIGV  bool            `json:"prices_include_igv"`
	IGVRate           string          `json:"igv_rate"`
	GlobalDiscountPct string          `json:"global_discount_pct"`
	ValidUntil        *string         `json:"valid_until"`
	Terms             string          `json:"terms"`
	Totals            json.RawMessage `json:"totals"`
	Version           int             `json:"version"`
	CreatedBy         string          `json:"created_by"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
	Items             []ItemOut       `json:"items,omitempty"`
	// highest version whose immutable snapshot was approved (still downloadable after a revision)
	ApprovedVersion *int `json:"approved_version"`
}

type ItemOut struct {
	LineIn
	PriceSource json.RawMessage `json:"price_source"`
}

const ts = `'YYYY-MM-DD"T"HH24:MI:SS"Z"'`
const quoteCols = `id::text, number, status, title, customer_kind, customer_id::text, customer_snapshot, currency, prices_include_igv, igv_rate::text,
	global_discount_pct::text, to_char(valid_until,'YYYY-MM-DD'), terms, totals, current_version, created_by,
	to_char(created_at AT TIME ZONE 'UTC',` + ts + `), to_char(updated_at AT TIME ZONE 'UTC',` + ts + `),
	(SELECT max(v.version) FROM quote_versions v WHERE v.quote_id=quotes.id AND v.status IN ('approved','sent','accepted'))`

func scanQuote(row pgx.Row) (Quote, error) {
	var q Quote
	err := row.Scan(&q.ID, &q.Number, &q.Status, &q.Title, &q.CustomerKind, &q.CustomerID, &q.Customer, &q.Currency, &q.PricesIncludeIGV, &q.IGVRate,
		&q.GlobalDiscountPct, &q.ValidUntil, &q.Terms, &q.Totals, &q.Version, &q.CreatedBy, &q.CreatedAt, &q.UpdatedAt, &q.ApprovedVersion)
	return q, err
}

func loadItems(ctx context.Context, tx pgx.Tx, id string) ([]ItemOut, error) {
	rows, err := tx.Query(ctx, `SELECT description, unit, qty::text, unit_price::text, COALESCE(unit_cost::text,''), discount_pct::text, discount_amount::text,
		billing_mode, tax_rule, price_source FROM quote_items WHERE quote_id=$1 ORDER BY ord`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ItemOut{}
	for rows.Next() {
		var it ItemOut
		if err := rows.Scan(&it.Description, &it.Unit, &it.Qty, &it.UnitPrice, &it.UnitCost, &it.DiscountPct, &it.DiscountAmount, &it.BillingMode, &it.TaxRule, &it.PriceSource); err != nil {
			return nil, err
		}
		trimZeros(&it.LineIn)
		out = append(out, it)
	}
	return out, rows.Err()
}

func trimZeros(l *LineIn) {
	for _, p := range []*string{&l.Qty, &l.UnitPrice, &l.UnitCost, &l.DiscountPct, &l.DiscountAmount} {
		if strings.Contains(*p, ".") {
			*p = strings.TrimRight(strings.TrimRight(*p, "0"), ".")
		}
	}
}

func (a *API) calculate(w http.ResponseWriter, r *http.Request) {
	var in CalcIn
	if !decode(w, r, &in) {
		return
	}
	out, err := Calculate(in)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	jsonOut(w, 200, out)
}

func (a *API) listQuotes(w http.ResponseWriter, r *http.Request) {
	st, q := r.URL.Query().Get("status"), strings.TrimSpace(r.URL.Query().Get("q"))
	out := []Quote{}
	err := a.tx(r, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), "SELECT "+quoteCols+` FROM quotes WHERE archived_at IS NULL AND ($1='' OR status=$1)
			AND ($2='' OR title ILIKE '%'||$2||'%' OR number ILIKE '%'||$2||'%' OR customer_snapshot->>'display_name' ILIKE '%'||$2||'%')
			ORDER BY updated_at DESC LIMIT 100`, st, q)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			x, err := scanQuote(rows)
			if err != nil {
				return err
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	if err != nil {
		a.internal(w, "list quotes", err)
		return
	}
	jsonOut(w, 200, map[string]any{"quotes": out})
}

func (a *API) fetchQuote(ctx context.Context, tx pgx.Tx, id string, lock bool) (Quote, error) {
	sql := "SELECT " + quoteCols + " FROM quotes WHERE id=$1"
	if lock {
		sql += " FOR UPDATE"
	}
	q, err := scanQuote(tx.QueryRow(ctx, sql, id))
	if err != nil {
		return q, err
	}
	q.Items, err = loadItems(ctx, tx, id)
	return q, err
}

func (a *API) getQuote(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var q Quote
	err := a.tx(r, func(tx pgx.Tx) (e error) { q, e = a.fetchQuote(r.Context(), tx, id, false); return })
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 404, "No encontrado.")
		return
	} else if err != nil {
		a.internal(w, "get quote", err)
		return
	}
	jsonOut(w, 200, q)
}

type badReq struct{ msg string }

func (e badReq) Error() string { return e.msg }

// resolveCustomer returns kind, id and an immutable snapshot of the customer data used.
func resolveCustomer(ctx context.Context, tx pgx.Tx, c *CustomerRef) (string, *string, []byte, error) {
	if c == nil {
		return "", nil, nil, badReq{"Falta el cliente (local o libre)."}
	}
	switch c.Kind {
	case "local":
		if !uuidRe.MatchString(c.ID) {
			return "", nil, nil, badReq{"Cliente local no válido."}
		}
		cu, err := scanCustomer(tx.QueryRow(ctx, "SELECT "+customerCols+" FROM commercial_customers WHERE id=$1 AND status='active'", c.ID))
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, nil, badReq{"Cliente local no encontrado."}
		} else if err != nil {
			return "", nil, nil, err
		}
		snap, _ := json.Marshal(map[string]any{"source": "local", "customer_id": cu.ID, "captured_at": time.Now().UTC().Format(time.RFC3339), "data": cu.CustomerData, "display_name": cu.DisplayName})
		return "local", &cu.ID, snap, nil
	case "free":
		if c.Data == nil {
			return "", nil, nil, badReq{"Faltan los datos del cliente libre."}
		}
		d := *c.Data
		if _, msg := d.Validate(); msg != "" {
			return "", nil, nil, badReq{msg}
		}
		snap, _ := json.Marshal(map[string]any{"source": "free", "captured_at": time.Now().UTC().Format(time.RFC3339), "data": d, "display_name": d.DisplayName})
		return "free", nil, snap, nil
	}
	return "", nil, nil, badReq{"Tipo de cliente no válido (local o libre)."}
}

// saveDraft applies the input to a draft quote (insert when id == ""), recomputing totals server-side.
func (a *API) saveDraft(ctx context.Context, tx pgx.Tx, id string, in QuoteIn, actorEmail string) (string, error) {
	s, err := loadSettings(ctx, tx)
	if err != nil {
		return "", err
	}
	var cur Quote
	if id != "" {
		if cur, err = a.fetchQuote(ctx, tx, id, true); err != nil {
			return "", err
		}
		if cur.Status != "draft" {
			return "", badReq{"Solo se puede editar un borrador. Crea una revisión (revise) para cambiar una cotización " + cur.Status + "."}
		}
	} else {
		cur = Quote{Currency: s.DefaultCurrency, PricesIncludeIGV: s.PricesIncludeIGV, IGVRate: s.IGVRate, GlobalDiscountPct: "0"}
		if c, err := scanCompany(tx.QueryRow(ctx, "SELECT "+companyCols+" FROM commercial_companies ORDER BY created_at LIMIT 1")); err == nil {
			cur.Terms = c.DefaultTerms
		}
		vu := a.Now().In(lima).AddDate(0, 0, s.DefaultValidityDays).Format("2006-01-02")
		cur.ValidUntil = &vu
	}
	if in.Title != nil {
		cur.Title = strings.TrimSpace(*in.Title)
	}
	if in.Currency != nil {
		cur.Currency = *in.Currency
	}
	if in.PricesIncludeIGV != nil {
		cur.PricesIncludeIGV = *in.PricesIncludeIGV
	}
	if in.IGVRate != nil {
		cur.IGVRate = *in.IGVRate
	}
	if in.GlobalDiscountPct != nil {
		cur.GlobalDiscountPct = *in.GlobalDiscountPct
	}
	if in.Terms != nil {
		cur.Terms = *in.Terms
	}
	if in.ValidUntil != nil {
		if *in.ValidUntil == "" {
			cur.ValidUntil = nil
		} else if _, err := time.Parse("2006-01-02", *in.ValidUntil); err != nil {
			return "", badReq{"Fecha de vigencia no válida (AAAA-MM-DD)."}
		} else {
			cur.ValidUntil = in.ValidUntil
		}
	}
	items := cur.Items
	if in.Items != nil {
		items = items[:0:0]
		for _, it := range *in.Items {
			ps := it.PriceSource
			if len(ps) == 0 {
				ps = json.RawMessage(`{"kind":"manual"}`)
			}
			if it.Unit == "" {
				it.Unit = "und"
			}
			items = append(items, ItemOut{LineIn: it.LineIn, PriceSource: ps})
		}
	}
	calcIn := CalcIn{Currency: cur.Currency, PricesIncludeIGV: cur.PricesIncludeIGV, IGVRate: cur.IGVRate, GlobalDiscountPct: cur.GlobalDiscountPct}
	for _, it := range items {
		calcIn.Lines = append(calcIn.Lines, it.LineIn)
	}
	totals, err := Calculate(calcIn)
	if err != nil {
		return "", badReq{err.Error()}
	}
	tb, _ := json.Marshal(totals)
	kind, custID, snap := cur.CustomerKind, cur.CustomerID, []byte(cur.Customer)
	if in.Customer != nil || id == "" || kind == "local" { // local snapshots refresh while in draft
		ref := in.Customer
		if ref == nil && kind == "local" {
			ref = &CustomerRef{Kind: "local", ID: *custID}
		}
		if kind, custID, snap, err = resolveCustomer(ctx, tx, ref); err != nil {
			return "", err
		}
	}
	if id == "" {
		err = tx.QueryRow(ctx, `INSERT INTO quotes (tenant_id, company_id, series_id, title, customer_kind, customer_id, customer_snapshot, currency, prices_include_igv,
			igv_rate, global_discount_pct, valid_until, terms, totals, created_by)
			SELECT current_setting('app.tenant_id')::uuid, s.company_id, s.id, $1, $2, $3::uuid, $4, $5, $6, $7::numeric, $8::numeric, $9::date, $10, $11, $12
			FROM commercial_series s WHERE s.is_default ORDER BY s.code LIMIT 1 RETURNING id::text`,
			cur.Title, kind, custID, snap, cur.Currency, cur.PricesIncludeIGV, cur.IGVRate, cur.GlobalDiscountPct, cur.ValidUntil, cur.Terms, tb, actorEmail).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", badReq{"Configura primero una empresa emisora y una serie."}
		}
	} else {
		_, err = tx.Exec(ctx, `UPDATE quotes SET title=$2, customer_kind=$3, customer_id=$4::uuid, customer_snapshot=$5, currency=$6, prices_include_igv=$7, igv_rate=$8::numeric,
			global_discount_pct=$9::numeric, valid_until=$10::date, terms=$11, totals=$12, updated_at=now() WHERE id=$1`,
			id, cur.Title, kind, custID, snap, cur.Currency, cur.PricesIncludeIGV, cur.IGVRate, cur.GlobalDiscountPct, cur.ValidUntil, cur.Terms, tb)
	}
	if err != nil {
		return "", err
	}
	if in.Items != nil {
		if _, err := tx.Exec(ctx, "DELETE FROM quote_items WHERE quote_id=$1", id); err != nil {
			return "", err
		}
		for i, it := range items {
			l := it.LineIn
			if _, err := tx.Exec(ctx, `INSERT INTO quote_items (tenant_id, quote_id, ord, description, unit, qty, unit_price, unit_cost, discount_pct, discount_amount, billing_mode, tax_rule, price_source)
				VALUES (current_setting('app.tenant_id')::uuid, $1, $2, $3, $4, $5::numeric, $6::numeric, NULLIF($7,'')::numeric, COALESCE(NULLIF($8,''),'0')::numeric, COALESCE(NULLIF($9,''),'0')::numeric, $10, $11, $12)`,
				id, i+1, strings.TrimSpace(l.Description), l.Unit, l.Qty, l.UnitPrice, l.UnitCost, l.DiscountPct, l.DiscountAmount, l.BillingMode, l.TaxRule, []byte(it.PriceSource)); err != nil {
				return "", err
			}
		}
	}
	return id, nil
}

func (a *API) writeQuote(w http.ResponseWriter, r *http.Request, id string, in QuoteIn, code int) {
	ac := actorOf(r)
	var q Quote
	err := a.tx(r, func(tx pgx.Tx) error {
		newID, err := a.saveDraft(r.Context(), tx, id, in, ac.Email)
		if err != nil {
			return err
		}
		action := "update"
		if id == "" {
			action = "create"
		}
		if err := event(r.Context(), tx, newID, ac.Email, action, "", "draft", map[string]any{"items_changed": in.Items != nil}); err != nil {
			return err
		}
		q, err = a.fetchQuote(r.Context(), tx, newID, false)
		return err
	})
	a.respondQuote(w, err, q, code)
}

func (a *API) respondQuote(w http.ResponseWriter, err error, q Quote, code int) {
	var br badReq
	switch {
	case err == nil:
		jsonOut(w, code, q)
	case errors.As(err, &br):
		fail(w, 400, br.msg)
	case errors.Is(err, pgx.ErrNoRows):
		fail(w, 404, "No encontrado.")
	case isUnique(err):
		fail(w, 409, "Conflicto de numeración; reintenta.")
	default:
		a.internal(w, "quote", err)
	}
}

func (a *API) createQuote(w http.ResponseWriter, r *http.Request) {
	var in QuoteIn
	if !decode(w, r, &in) {
		return
	}
	a.writeQuote(w, r, "", in, 201)
}

func (a *API) patchQuote(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in QuoteIn
	if !decode(w, r, &in) {
		return
	}
	a.writeQuote(w, r, id, in, 200)
}

func (a *API) duplicateQuote(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ac := actorOf(r)
	var q Quote
	err := a.tx(r, func(tx pgx.Tx) error {
		src, err := a.fetchQuote(r.Context(), tx, id, false)
		if err != nil {
			return err
		}
		var data CustomerData
		var snap struct {
			Data CustomerData `json:"data"`
		}
		_ = json.Unmarshal(src.Customer, &snap)
		data = snap.Data
		ref := &CustomerRef{Kind: src.CustomerKind, Data: &data}
		if src.CustomerID != nil {
			ref.ID = *src.CustomerID
		}
		items := make([]ItemIn, 0, len(src.Items))
		for _, it := range src.Items {
			items = append(items, ItemIn{LineIn: it.LineIn, PriceSource: it.PriceSource})
		}
		title := strings.TrimSpace(src.Title + " (copia)")
		in := QuoteIn{Title: &title, Customer: ref, Currency: &src.Currency, PricesIncludeIGV: &src.PricesIncludeIGV, IGVRate: &src.IGVRate,
			GlobalDiscountPct: &src.GlobalDiscountPct, Terms: &src.Terms, Items: &items}
		newID, err := a.saveDraft(r.Context(), tx, "", in, ac.Email)
		if err != nil {
			return err
		}
		if err := event(r.Context(), tx, newID, ac.Email, "duplicate", "", "draft", map[string]string{"from_quote": id}); err != nil {
			return err
		}
		q, err = a.fetchQuote(r.Context(), tx, newID, false)
		return err
	})
	a.respondQuote(w, err, q, 201)
}

// transition applies a state-machine action with its own permission.
func (a *API) transition(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	action := r.PathValue("action")
	t, known := Transitions[action]
	if !known {
		fail(w, 404, "Acción no válida.")
		return
	}
	ac := actorOf(r)
	if !Can(ac.Role, t.perm) {
		a.Log.Warn("commercial forbidden", "actor", ac.Email, "role", ac.Role, "action", action)
		fail(w, 403, "No tienes permiso para esta acción.")
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength > 0 && !decode(w, r, &in) {
		return
	}
	var q Quote
	err := a.tx(r, func(tx pgx.Tx) error {
		cur, err := a.fetchQuote(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		to, msg, ok := Next(action, cur.Status)
		if !ok {
			return badReq{"No se puede «" + action + "»: " + msg + "."}
		}
		if action == "request-approval" && len(cur.Items) == 0 {
			return badReq{"La cotización no tiene conceptos."}
		}
		if (action == "return" || action == "reject" || action == "cancel") && strings.TrimSpace(in.Reason) == "" {
			return badReq{"Indica el motivo."}
		}
		detail := map[string]any{}
		if in.Reason != "" {
			detail["reason"] = strings.TrimSpace(in.Reason)
		}
		switch action {
		case "approve":
			if cur.Number == nil { // assign number: the series row lock serializes concurrent approvals
				var code, pattern string
				var n int64
				y := a.Now().In(lima).Year()
				err := tx.QueryRow(r.Context(), `UPDATE commercial_series s SET
					next_number = CASE WHEN s.reset_yearly AND s.current_year <> $2 THEN 2 ELSE s.next_number + 1 END, current_year = $2
					FROM quotes q WHERE q.id = $1 AND s.id = q.series_id RETURNING s.code, s.pattern, s.next_number - 1`, id, y).Scan(&code, &pattern, &n)
				if err != nil {
					return err
				}
				num := FormatNumber(pattern, code, y, n)
				if _, err := tx.Exec(r.Context(), "UPDATE quotes SET number=$2 WHERE id=$1", id, num); err != nil {
					return err
				}
				cur.Number = &num
				detail["number"] = num
			}
			cur.Status = to
			snap, _ := json.Marshal(cur) // immutable approved version (quote + items + totals + customer snapshot)
			if _, err := tx.Exec(r.Context(), `INSERT INTO quote_versions (tenant_id, quote_id, version, status, snapshot, created_by)
				VALUES (current_setting('app.tenant_id')::uuid, $1, $2, 'approved', $3, $4)`, id, cur.Version, snap, ac.Email); err != nil {
				return err
			}
			detail["version"] = cur.Version
		case "revise":
			if _, err := tx.Exec(r.Context(), "UPDATE quotes SET current_version = current_version + 1 WHERE id=$1", id); err != nil {
				return err
			}
			detail["version"] = cur.Version + 1
		}
		if _, err := tx.Exec(r.Context(), "UPDATE quotes SET status=$2, updated_at=now() WHERE id=$1", id, to); err != nil {
			return err
		}
		if err := event(r.Context(), tx, id, ac.Email, action, cur.Status, to, detail); err != nil {
			return err
		}
		q, err = a.fetchQuote(r.Context(), tx, id, false)
		return err
	})
	a.respondQuote(w, err, q, 200)
}

func (a *API) listVersions(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	out := []map[string]any{}
	err := a.tx(r, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT version, status, snapshot, created_by, to_char(created_at AT TIME ZONE 'UTC',`+ts+`) FROM quote_versions WHERE quote_id=$1 ORDER BY version`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v int
			var st, by, at string
			var snap json.RawMessage
			if err := rows.Scan(&v, &st, &snap, &by, &at); err != nil {
				return err
			}
			out = append(out, map[string]any{"version": v, "status": st, "snapshot": snap, "created_by": by, "created_at": at})
		}
		return rows.Err()
	})
	if err != nil {
		a.internal(w, "versions", err)
		return
	}
	jsonOut(w, 200, map[string]any{"versions": out})
}

func (a *API) listEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	out := []map[string]any{}
	err := a.tx(r, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT actor, action, COALESCE(from_status,''), COALESCE(to_status,''), detail, to_char(at AT TIME ZONE 'UTC',`+ts+`) FROM quote_events WHERE quote_id=$1 ORDER BY id`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ac, act, f, t, at string
			var d json.RawMessage
			if err := rows.Scan(&ac, &act, &f, &t, &d, &at); err != nil {
				return err
			}
			out = append(out, map[string]any{"actor": ac, "action": act, "from": f, "to": t, "detail": d, "at": at})
		}
		return rows.Err()
	})
	if err != nil {
		a.internal(w, "events", err)
		return
	}
	jsonOut(w, 200, map[string]any{"events": out})
}
