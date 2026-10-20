package commercial

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"golang.org/x/text/unicode/norm"
)

type Price struct {
	ID               string          `json:"id"`
	Currency         string          `json:"currency"`
	UnitPrice        string          `json:"unit_price"`
	PriceIncludesIGV bool            `json:"price_includes_igv"`
	ValidFrom        string          `json:"valid_from"`
	ValidTo          *string         `json:"valid_to"`
	Source           json.RawMessage `json:"source"`
	Verified         bool            `json:"verified"`
}

type Service struct {
	ID          string   `json:"id"`
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Aliases     []string `json:"aliases"`
	Unit        string   `json:"unit"`
	BillingMode string   `json:"billing_mode"`
	TaxRule     string   `json:"tax_rule"`
	Active      bool     `json:"active"`
	Current     *Price   `json:"current_price"` // valid today (Lima) in PEN, else any currency
	Prices      []Price  `json:"prices,omitempty"`
}

const serviceCols = `id::text, code, name, description, aliases, unit, billing_mode, tax_rule, active`
const priceCols = `id::text, currency, unit_price::text, price_includes_igv, to_char(valid_from,'YYYY-MM-DD'), to_char(valid_to,'YYYY-MM-DD'), source, verified`

func scanPrice(row pgx.Row) (Price, error) {
	var p Price
	err := row.Scan(&p.ID, &p.Currency, &p.UnitPrice, &p.PriceIncludesIGV, &p.ValidFrom, &p.ValidTo, &p.Source, &p.Verified)
	p.UnitPrice = trimDec(p.UnitPrice)
	return p, err
}

func trimDec(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

func (a *API) loadServices(ctx context.Context, tx pgx.Tx, all bool) ([]Service, error) {
	q := "SELECT " + serviceCols + " FROM service_catalog"
	if !all {
		q += " WHERE active"
	}
	rows, err := tx.Query(ctx, q+" ORDER BY name")
	if err != nil {
		return nil, err
	}
	out := []Service{}
	for rows.Next() {
		var s Service
		if err := rows.Scan(&s.ID, &s.Code, &s.Name, &s.Description, &s.Aliases, &s.Unit, &s.BillingMode, &s.TaxRule, &s.Active); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, s)
	}
	rows.Close()
	today := a.Now().In(lima).Format("2006-01-02")
	for i := range out {
		p, err := scanPrice(tx.QueryRow(ctx, "SELECT "+priceCols+` FROM price_versions WHERE service_id=$1 AND valid_from <= $2::date AND (valid_to IS NULL OR valid_to >= $2::date)
			ORDER BY (currency='PEN') DESC, valid_from DESC LIMIT 1`, out[i].ID, today))
		if err == nil {
			out[i].Current = &p
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	return out, nil
}

func (a *API) listCatalog(w http.ResponseWriter, r *http.Request) {
	var out []Service
	err := a.tx(r, func(tx pgx.Tx) (e error) { out, e = a.loadServices(r.Context(), tx, r.URL.Query().Get("all") == "1"); return })
	if err != nil {
		a.internal(w, "catalog", err)
		return
	}
	if q := r.URL.Query().Get("q"); q != "" {
		ms := MatchServices(q, out)
		out = out[:0]
		for _, m := range ms {
			out = append(out, m.Service)
		}
	}
	jsonOut(w, 200, map[string]any{"services": out})
}

func (a *API) getService(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var s Service
	err := a.tx(r, func(tx pgx.Tx) error {
		err := tx.QueryRow(r.Context(), "SELECT "+serviceCols+" FROM service_catalog WHERE id=$1", id).
			Scan(&s.ID, &s.Code, &s.Name, &s.Description, &s.Aliases, &s.Unit, &s.BillingMode, &s.TaxRule, &s.Active)
		if err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), "SELECT "+priceCols+" FROM price_versions WHERE service_id=$1 ORDER BY valid_from DESC", id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanPrice(rows)
			if err != nil {
				return err
			}
			s.Prices = append(s.Prices, p)
		}
		return rows.Err()
	})
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 404, "No encontrado.")
		return
	} else if err != nil {
		a.internal(w, "service", err)
		return
	}
	jsonOut(w, 200, s)
}

type serviceIn struct {
	Code        *string   `json:"code"`
	Name        *string   `json:"name"`
	Description *string   `json:"description"`
	Aliases     *[]string `json:"aliases"`
	Unit        *string   `json:"unit"`
	BillingMode *string   `json:"billing_mode"`
	TaxRule     *string   `json:"tax_rule"`
	Active      *bool     `json:"active"`
}

var codeRe = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,31}$`)

func (in serviceIn) validate(create bool) string {
	if create && (in.Code == nil || in.Name == nil || in.BillingMode == nil) {
		return "Faltan código, nombre o ciclo."
	}
	if in.Code != nil && !codeRe.MatchString(*in.Code) {
		return "Código no válido (A-Z, 0-9, - _; máx. 32)."
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
		return "El nombre es obligatorio."
	}
	if in.BillingMode != nil && !validMode[*in.BillingMode] {
		return "Ciclo no válido."
	}
	if in.TaxRule != nil && !validTax[*in.TaxRule] {
		return "Afectación no válida."
	}
	return ""
}

func (a *API) createService(w http.ResponseWriter, r *http.Request) {
	var in serviceIn
	if !decode(w, r, &in) {
		return
	}
	if m := in.validate(true); m != "" {
		fail(w, 400, m)
		return
	}
	def := func(p *string, d string) string {
		if p == nil || *p == "" {
			return d
		}
		return strings.TrimSpace(*p)
	}
	aliases := []string{}
	if in.Aliases != nil {
		aliases = *in.Aliases
	}
	var id string
	err := a.tx(r, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `INSERT INTO service_catalog (tenant_id, code, name, description, aliases, unit, billing_mode, tax_rule, created_by)
			VALUES (current_setting('app.tenant_id')::uuid, $1,$2,$3,$4,$5,$6,$7,$8) RETURNING id::text`,
			*in.Code, strings.TrimSpace(*in.Name), def(in.Description, ""), aliases, def(in.Unit, "und"), *in.BillingMode, def(in.TaxRule, "gravado"), actorOf(r).Email).Scan(&id)
	})
	if isUnique(err) {
		fail(w, 409, "Ya existe un servicio con ese código.")
		return
	} else if err != nil {
		a.internal(w, "create service", err)
		return
	}
	jsonOut(w, 201, map[string]string{"id": id})
}

func (a *API) patchService(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in serviceIn
	if !decode(w, r, &in) {
		return
	}
	if m := in.validate(false); m != "" {
		fail(w, 400, m)
		return
	}
	var n int64
	err := a.tx(r, func(tx pgx.Tx) error {
		ct, err := tx.Exec(r.Context(), `UPDATE service_catalog SET code=COALESCE($2,code), name=COALESCE($3,name), description=COALESCE($4,description),
			aliases=COALESCE($5,aliases), unit=COALESCE($6,unit), billing_mode=COALESCE($7,billing_mode), tax_rule=COALESCE($8,tax_rule), active=COALESCE($9,active), updated_at=now() WHERE id=$1`,
			id, in.Code, in.Name, in.Description, in.Aliases, in.Unit, in.BillingMode, in.TaxRule, in.Active)
		n = ct.RowsAffected()
		return err
	})
	if isUnique(err) {
		fail(w, 409, "Ya existe un servicio con ese código.")
		return
	} else if err != nil {
		a.internal(w, "patch service", err)
		return
	} else if n == 0 {
		fail(w, 404, "No encontrado.")
		return
	}
	jsonOut(w, 200, map[string]string{"id": id})
}

// addPrice creates a new price version; an open-ended current version is closed the day before.
func (a *API) addPrice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Currency         string          `json:"currency"`
		UnitPrice        string          `json:"unit_price"`
		PriceIncludesIGV bool            `json:"price_includes_igv"`
		ValidFrom        string          `json:"valid_from"`
		ValidTo          string          `json:"valid_to"`
		Source           json.RawMessage `json:"source"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Currency != "PEN" && in.Currency != "USD" {
		fail(w, 400, "Moneda no válida (PEN o USD).")
		return
	}
	if d, err := ParseDec(in.UnitPrice); err != nil || d.Sign() < 0 {
		fail(w, 400, "Precio no válido.")
		return
	}
	if in.ValidFrom == "" {
		in.ValidFrom = a.Now().In(lima).Format("2006-01-02")
	}
	src := map[string]any{"kind": "manual", "entered_by": actorOf(r).Email}
	if len(in.Source) > 0 {
		var extra map[string]any
		if json.Unmarshal(in.Source, &extra) == nil {
			for k, v := range extra {
				if k == "note" || k == "origin" {
					src[k] = v
				}
			}
		}
	}
	sb, _ := json.Marshal(src)
	var p Price
	err := a.tx(r, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `UPDATE price_versions SET valid_to = $3::date - 1 WHERE service_id=$1 AND currency=$2 AND valid_to IS NULL AND valid_from < $3::date`,
			id, in.Currency, in.ValidFrom); err != nil {
			return err
		}
		var e error
		p, e = scanPrice(tx.QueryRow(r.Context(), `INSERT INTO price_versions (tenant_id, service_id, currency, unit_price, price_includes_igv, valid_from, valid_to, source, verified, created_by)
			VALUES (current_setting('app.tenant_id')::uuid, $1, $2, $3::numeric, $4, $5::date, NULLIF($6,'')::date, $7, true, $8) RETURNING `+priceCols,
			id, in.Currency, in.UnitPrice, in.PriceIncludesIGV, in.ValidFrom, in.ValidTo, sb, actorOf(r).Email))
		return e
	})
	if err != nil {
		if strings.Contains(err.Error(), "23P01") || strings.Contains(err.Error(), "overlaps") {
			fail(w, 409, "La vigencia se solapa con otra versión de precio.")
			return
		}
		if strings.Contains(err.Error(), "23503") {
			fail(w, 404, "Servicio no encontrado.")
			return
		}
		a.internal(w, "add price", err)
		return
	}
	jsonOut(w, 201, p)
}

// ---------- fuzzy matching (deterministic) ----------

type ServiceMatch struct {
	Service    Service `json:"service"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

var stop = map[string]bool{"de": true, "del": true, "la": true, "el": true, "en": true, "los": true, "las": true, "un": true, "una": true, "para": true, "con": true, "y": true, "a": true, "al": true, "por": true, "plan": false}

func Fold(s string) string {
	t := norm.NFD.String(strings.ToLower(s))
	var b strings.Builder
	for _, r := range t {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return " " + strings.Join(strings.Fields(b.String()), " ") + " "
}

func tokens(s string) []string {
	out := []string{}
	for _, t := range strings.Fields(Fold(s)) {
		if !stop[t] && len(t) > 1 {
			out = append(out, t)
		}
	}
	return out
}

// MatchServices ranks catalog services against free text: alias/name phrase containment first, then token coverage.
func MatchServices(text string, svcs []Service) []ServiceMatch {
	ft := Fold(text)
	tt := map[string]bool{}
	for _, t := range tokens(text) {
		tt[t] = true
	}
	var out []ServiceMatch
	for _, s := range svcs {
		best, reason := 0.0, ""
		for _, al := range append([]string{s.Name, s.Code}, s.Aliases...) {
			if fa := Fold(al); len(strings.TrimSpace(fa)) > 2 && strings.Contains(ft, fa) {
				if best < 0.95 {
					best, reason = 0.95, "coincide con «"+al+"»"
				}
			}
			at := tokens(al)
			if len(at) == 0 {
				continue
			}
			hit := 0
			for _, t := range at {
				if tt[t] {
					hit++
				}
			}
			if c := 0.9 * float64(hit) / float64(len(at)); c > best {
				best, reason = c, "palabras en común con «"+al+"»"
			}
		}
		if best >= 0.5 {
			out = append(out, ServiceMatch{s, float64(int(best*100)) / 100, reason})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Confidence > out[j].Confidence })
	return out
}
