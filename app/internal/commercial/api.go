package commercial

import (
	"context"
	"devbrain/internal/entitle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"devbrain/internal/tenant"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var lima, _ = time.LoadLocation("America/Lima")

type API struct {
	DB   *pgxpool.Pool
	Key  []byte // actor HMAC key (>= 32 bytes); empty disables the module (503)
	Log  *slog.Logger
	Now  func() time.Time
	RUC  RUCLookup                                              // optional; disabled stub when nil
	Mail Mailer                                                 // optional; disabled stub when nil
	Ent  *entitle.Cache                                         // module entitlements; nil = legacy commercial_members check
	PDF  func(ctx context.Context, docx []byte) ([]byte, error) // DOCX→PDF converter (LibreOffice); nil = PDF unavailable
}

func (a *API) ruc() RUCLookup {
	if a.RUC == nil {
		return disabledRUC{}
	}
	return a.RUC
}
func (a *API) mailer() Mailer {
	if a.Mail == nil {
		return disabledMailer{}
	}
	return a.Mail
}
func jsonMarshal(v any) (json.RawMessage, error) { b, err := json.Marshal(v); return b, err }

type actorKey struct{}
type actor struct{ Email, Role string }

func jsonOut(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, code int, msg string) {
	jsonOut(w, code, map[string]string{"error": msg})
}

// Mount registers /v1/commercial/* behind the bearer-token middleware.
func (a *API) Mount(route func(string, http.Handler), auth func(http.Handler) http.Handler) {
	if a.Now == nil {
		a.Now = time.Now
	}
	h := func(perm string, fn http.HandlerFunc) http.Handler { return auth(a.guard(perm, fn)) }
	route("GET /v1/commercial/me", h(PermView, a.me))
	route("GET /v1/platform/me", auth(http.HandlerFunc(a.platformMe)))
	route("GET /v1/commercial/settings", h(PermView, a.getSettings))
	route("PATCH /v1/commercial/settings", h(PermSettings, a.patchSettings))
	route("GET /v1/commercial/company", h(PermView, a.getCompany))
	route("PATCH /v1/commercial/company", h(PermSettings, a.patchCompany))
	route("GET /v1/commercial/series", h(PermView, a.getSeries))
	route("PATCH /v1/commercial/series", h(PermSettings, a.patchSeries))
	route("GET /v1/commercial/customers", h(PermView, a.listCustomers))
	route("GET /v1/commercial/customers/search", h(PermView, a.listCustomers))
	route("POST /v1/commercial/customers", h(PermCustomer, a.createCustomer))
	route("GET /v1/commercial/customers/{id}", h(PermView, a.getCustomer))
	route("PATCH /v1/commercial/customers/{id}", h(PermCustomer, a.patchCustomer))
	route("POST /v1/commercial/calculate", h(PermView, a.calculate))
	route("GET /v1/commercial/quotes", h(PermView, a.listQuotes))
	route("POST /v1/commercial/quotes", h(PermEdit, a.createQuote))
	route("GET /v1/commercial/quotes/{id}", h(PermView, a.getQuote))
	route("PATCH /v1/commercial/quotes/{id}", h(PermEdit, a.patchQuote))
	route("POST /v1/commercial/quotes/{id}/duplicate", h(PermEdit, a.duplicateQuote))
	route("GET /v1/commercial/quotes/{id}/versions", h(PermView, a.listVersions))
	route("GET /v1/commercial/quotes/{id}/events", h(PermView, a.listEvents))
	route("GET /v1/commercial/catalog", h(PermView, a.listCatalog))
	route("POST /v1/commercial/catalog", h(PermSettings, a.createService))
	route("GET /v1/commercial/catalog/{id}", h(PermView, a.getService))
	route("PATCH /v1/commercial/catalog/{id}", h(PermSettings, a.patchService))
	route("POST /v1/commercial/catalog/{id}/prices", h(PermSettings, a.addPrice))
	route("POST /v1/commercial/intent", h(PermView, a.intent))
	route("GET /v1/commercial/integrations", h(PermView, a.integrations))
	route("POST /v1/commercial/quotes/{id}/export", h(PermEdit, a.exportQuote))
	route("GET /v1/commercial/quotes/{id}/exports", h(PermView, a.listExports))
	route("GET /v1/commercial/exports/{id}", h(PermView, a.downloadExport))
	route("POST /v1/commercial/quotes/{id}/{action}", h(PermView, a.transition)) // per-action permission inside
}

// guard: module enabled, token scope "commercial", signed actor, member role with permission.
func (a *API) guard(perm string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(a.Key) < 32 {
			fail(w, 503, "Módulo comercial no configurado.")
			return
		}
		info, _ := tenant.From(r.Context())
		if !hasScope(info.Scopes, "commercial") {
			fail(w, 403, "El token no tiene acceso al módulo comercial.")
			return
		}
		email, ok := VerifyActor(a.Key, r.Header.Get(ActorHeader), a.Now())
		if !ok {
			fail(w, 401, "Identidad de usuario no válida.")
			return
		}
		var role string
		if a.Ent != nil { // module entitlements (migration 013) are the source of truth
			e, err := a.Ent.Get(r.Context(), email)
			if err != nil {
				a.internal(w, "entitlements", err)
				return
			}
			if !e.Has(entitle.Cotizador) || e.TenantID != info.TenantID {
				a.Log.Warn("commercial module_disabled", "actor", email, "known", e.Known, "path", r.URL.Path)
				jsonOut(w, 403, map[string]string{"error": "El módulo Cotizador no está habilitado para tu usuario.", "code": "module_disabled"})
				return
			}
			if role = e.Role(entitle.Cotizador); !Can(role, perm) {
				a.Log.Warn("commercial forbidden", "actor", email, "role", role, "perm", perm, "path", r.URL.Path)
				fail(w, 403, "No tienes permiso para esta acción.")
				return
			}
		} else {
			err := tenant.WithTx(r.Context(), a.DB, func(tx pgx.Tx) error {
				return tx.QueryRow(r.Context(), "SELECT role FROM commercial_members WHERE email=$1", email).Scan(&role)
			})
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && !Can(role, perm)) {
				a.Log.Warn("commercial forbidden", "actor", email, "role", role, "perm", perm, "path", r.URL.Path)
				fail(w, 403, "No tienes permiso para esta acción.")
				return
			}
			if err != nil {
				a.internal(w, "role lookup", err)
				return
			}
		}
		next(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, actor{email, role})))
	}
}

// platformMe: entitlements of the signed actor (used by the chat for login + UI). Unknown users get enabled=false.
func (a *API) platformMe(w http.ResponseWriter, r *http.Request) {
	info, _ := tenant.From(r.Context())
	if len(a.Key) < 32 || a.Ent == nil || !hasScope(info.Scopes, "commercial") {
		fail(w, 503, "Entitlements no configurados.")
		return
	}
	email, ok := VerifyActor(a.Key, r.Header.Get(ActorHeader), a.Now())
	if !ok {
		fail(w, 401, "Identidad de usuario no válida.")
		return
	}
	e, err := a.Ent.Get(r.Context(), email)
	if err != nil {
		a.internal(w, "entitlements", err)
		return
	}
	mods := e.Modules
	if e.Superadmin {
		mods = map[string]string{entitle.Chat: "", entitle.Notes: "", entitle.Cotizador: "admin"}
	}
	jsonOut(w, 200, map[string]any{"email": e.Email, "enabled": e.Enabled, "superadmin": e.Superadmin, "modules": mods,
		"tenant_match": e.TenantID != "" && e.TenantID == info.TenantID})
}

func hasScope(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func actorOf(r *http.Request) actor { a, _ := r.Context().Value(actorKey{}).(actor); return a }

func (a *API) internal(w http.ResponseWriter, what string, err error) {
	a.Log.Error("commercial", "op", what, "err", err)
	fail(w, 500, "Error interno.")
}

func (a *API) tx(r *http.Request, fn func(pgx.Tx) error) error {
	return tenant.WithTx(r.Context(), a.DB, fn)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		fail(w, 400, "JSON no válido: "+strings.TrimPrefix(err.Error(), "json: "))
		return false
	}
	return true
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func pathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		fail(w, 404, "No encontrado.")
		return "", false
	}
	return id, true
}

func event(ctx context.Context, tx pgx.Tx, quoteID, actor, action, from, to string, detail any) error {
	b, _ := json.Marshal(detail)
	if detail == nil {
		b = []byte("{}")
	}
	_, err := tx.Exec(ctx, `INSERT INTO quote_events (tenant_id, quote_id, actor, action, from_status, to_status, detail)
		VALUES (current_setting('app.tenant_id')::uuid, $1, $2, $3, NULLIF($4,''), NULLIF($5,''), $6)`, quoteID, actor, action, from, to, b)
	return err
}

// ---------------- me / settings / company / series ----------------

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	ac := actorOf(r)
	perms := []string{}
	for _, p := range []string{PermView, PermEdit, PermApprove, PermCustomer, PermSettings} {
		if Can(ac.Role, p) {
			perms = append(perms, p)
		}
	}
	jsonOut(w, 200, map[string]any{"email": ac.Email, "role": ac.Role, "permissions": perms})
}

type settings struct {
	DefaultCurrency     string `json:"default_currency"`
	IGVRate             string `json:"igv_rate"`
	PricesIncludeIGV    bool   `json:"prices_include_igv"`
	DefaultValidityDays int    `json:"default_validity_days"`
	IsPlaceholder       bool   `json:"is_placeholder"`
}

func loadSettings(ctx context.Context, tx pgx.Tx) (settings, error) {
	var s settings
	err := tx.QueryRow(ctx, `SELECT default_currency, igv_rate::text, prices_include_igv, default_validity_days, is_placeholder FROM commercial_settings`).
		Scan(&s.DefaultCurrency, &s.IGVRate, &s.PricesIncludeIGV, &s.DefaultValidityDays, &s.IsPlaceholder)
	if errors.Is(err, pgx.ErrNoRows) { // lazily create defaults for tenants other than the seeded one
		if _, err = tx.Exec(ctx, `INSERT INTO commercial_settings (tenant_id) VALUES (current_setting('app.tenant_id')::uuid) ON CONFLICT DO NOTHING`); err != nil {
			return s, err
		}
		return loadSettings(ctx, tx)
	}
	return s, err
}

func (a *API) getSettings(w http.ResponseWriter, r *http.Request) {
	var s settings
	if err := a.tx(r, func(tx pgx.Tx) (e error) { s, e = loadSettings(r.Context(), tx); return }); err != nil {
		a.internal(w, "settings", err)
		return
	}
	jsonOut(w, 200, s)
}

func (a *API) patchSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DefaultCurrency     *string `json:"default_currency"`
		IGVRate             *string `json:"igv_rate"`
		PricesIncludeIGV    *bool   `json:"prices_include_igv"`
		DefaultValidityDays *int    `json:"default_validity_days"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.DefaultCurrency != nil && *in.DefaultCurrency != "PEN" && *in.DefaultCurrency != "USD" {
		fail(w, 400, "Moneda no válida (PEN o USD).")
		return
	}
	if in.IGVRate != nil {
		d, err := ParseDec(*in.IGVRate)
		if err != nil || d.Sign() < 0 || d.Cmp(one) >= 0 {
			fail(w, 400, "Tasa de IGV no válida (fracción, p. ej. 0.18).")
			return
		}
	}
	var s settings
	err := a.tx(r, func(tx pgx.Tx) error {
		if _, err := loadSettings(r.Context(), tx); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE commercial_settings SET default_currency=COALESCE($1,default_currency), igv_rate=COALESCE($2::numeric,igv_rate),
			prices_include_igv=COALESCE($3,prices_include_igv), default_validity_days=COALESCE($4,default_validity_days), is_placeholder=false, updated_at=now()`,
			in.DefaultCurrency, in.IGVRate, in.PricesIncludeIGV, in.DefaultValidityDays); err != nil {
			return err
		}
		var e error
		s, e = loadSettings(r.Context(), tx)
		return e
	})
	if err != nil {
		a.internal(w, "patch settings", err)
		return
	}
	jsonOut(w, 200, s)
}

type company struct {
	ID            string `json:"id"`
	LegalName     string `json:"legal_name"`
	TradeName     string `json:"trade_name"`
	TaxID         string `json:"tax_id"`
	Address       string `json:"address"`
	Email         string `json:"email"`
	Phone         string `json:"phone"`
	Website       string `json:"website"`
	Footer        string `json:"footer"`
	DefaultTerms  string `json:"default_terms"`
	IsPlaceholder bool   `json:"is_placeholder"`
}

const companyCols = `id::text, legal_name, trade_name, tax_id, address, email, phone, website, footer, default_terms, is_placeholder`

func scanCompany(row pgx.Row) (company, error) {
	var c company
	err := row.Scan(&c.ID, &c.LegalName, &c.TradeName, &c.TaxID, &c.Address, &c.Email, &c.Phone, &c.Website, &c.Footer, &c.DefaultTerms, &c.IsPlaceholder)
	return c, err
}

func (a *API) companyDefaults(r *http.Request, tx pgx.Tx) (company, error) {
	return scanCompany(tx.QueryRow(r.Context(), "SELECT "+companyCols+" FROM commercial_companies ORDER BY created_at LIMIT 1"))
}

func (a *API) getCompany(w http.ResponseWriter, r *http.Request) {
	var c company
	err := a.tx(r, func(tx pgx.Tx) (e error) {
		c, e = scanCompany(tx.QueryRow(r.Context(), "SELECT "+companyCols+" FROM commercial_companies ORDER BY created_at LIMIT 1"))
		return
	})
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 404, "No hay empresa emisora configurada.")
		return
	} else if err != nil {
		a.internal(w, "company", err)
		return
	}
	jsonOut(w, 200, c)
}

func (a *API) patchCompany(w http.ResponseWriter, r *http.Request) {
	var in map[string]string
	if !decode(w, r, &in) {
		return
	}
	allowed := map[string]bool{"legal_name": true, "trade_name": true, "tax_id": true, "address": true, "email": true, "phone": true, "website": true, "footer": true, "default_terms": true}
	sets, args := []string{}, []any{}
	for k, v := range in {
		if !allowed[k] {
			fail(w, 400, "Campo no permitido: "+k)
			return
		}
		if k == "tax_id" && v != "" {
			if _, ok := normTaxID("RUC", v); !ok {
				fail(w, 400, "RUC no válido.")
				return
			}
		}
		if k == "legal_name" && strings.TrimSpace(v) == "" {
			fail(w, 400, "La razón social es obligatoria.")
			return
		}
		args = append(args, strings.TrimSpace(v))
		sets = append(sets, fmt.Sprintf("%s=$%d", k, len(args)))
	}
	if len(sets) == 0 {
		fail(w, 400, "Nada que actualizar.")
		return
	}
	var c company
	err := a.tx(r, func(tx pgx.Tx) (e error) {
		c, e = scanCompany(tx.QueryRow(r.Context(), "UPDATE commercial_companies SET "+strings.Join(sets, ",")+
			", is_placeholder=false, updated_at=now() WHERE id=(SELECT id FROM commercial_companies ORDER BY created_at LIMIT 1) RETURNING "+companyCols, args...))
		return
	})
	if err != nil {
		a.internal(w, "patch company", err)
		return
	}
	jsonOut(w, 200, c)
}

type series struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	Pattern     string `json:"pattern"`
	ResetYearly bool   `json:"reset_yearly"`
	NextNumber  int64  `json:"next_number"`
	CurrentYear int    `json:"current_year"`
	Preview     string `json:"preview"`
}

func FormatNumber(pattern, code string, year int, n int64) string {
	s := strings.ReplaceAll(pattern, "{CODE}", code)
	s = strings.ReplaceAll(s, "{YYYY}", strconv.Itoa(year))
	for _, w := range []int{8, 7, 6, 5, 4, 3} {
		s = strings.ReplaceAll(s, fmt.Sprintf("{N%d}", w), fmt.Sprintf("%0*d", w, n))
	}
	return strings.ReplaceAll(s, "{N}", strconv.FormatInt(n, 10))
}

var patternRe = regexp.MustCompile(`^[A-Za-z0-9\-_/ ]*(\{(CODE|YYYY|N|N[3-8])\}[A-Za-z0-9\-_/ ]*)+$`)

func (a *API) loadSeries(ctx context.Context, tx pgx.Tx) (series, error) {
	var s series
	err := tx.QueryRow(ctx, `SELECT id::text, code, pattern, reset_yearly, next_number, current_year FROM commercial_series WHERE is_default ORDER BY code LIMIT 1`).
		Scan(&s.ID, &s.Code, &s.Pattern, &s.ResetYearly, &s.NextNumber, &s.CurrentYear)
	y := a.Now().In(lima).Year()
	n := s.NextNumber
	if s.ResetYearly && s.CurrentYear != y {
		n = 1
	}
	s.Preview = FormatNumber(s.Pattern, s.Code, y, n)
	return s, err
}

func (a *API) getSeries(w http.ResponseWriter, r *http.Request) {
	var s series
	err := a.tx(r, func(tx pgx.Tx) (e error) { s, e = a.loadSeries(r.Context(), tx); return })
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 404, "No hay serie configurada.")
		return
	} else if err != nil {
		a.internal(w, "series", err)
		return
	}
	jsonOut(w, 200, s)
}

func (a *API) patchSeries(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code        *string `json:"code"`
		Pattern     *string `json:"pattern"`
		ResetYearly *bool   `json:"reset_yearly"`
		NextNumber  *int64  `json:"next_number"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Code != nil && !regexp.MustCompile(`^[A-Z0-9]{1,8}$`).MatchString(*in.Code) {
		fail(w, 400, "Código de serie no válido (A-Z, 0-9, máx. 8).")
		return
	}
	if in.Pattern != nil && (!patternRe.MatchString(*in.Pattern) || !strings.Contains(*in.Pattern, "{N")) {
		fail(w, 400, "Formato no válido. Use {CODE}, {YYYY} y {N5} (p. ej. {CODE}-{YYYY}-{N5}).")
		return
	}
	if in.NextNumber != nil && *in.NextNumber < 1 {
		fail(w, 400, "El siguiente número debe ser ≥ 1.")
		return
	}
	var s series
	err := a.tx(r, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `UPDATE commercial_series SET code=COALESCE($1,code), pattern=COALESCE($2,pattern), reset_yearly=COALESCE($3,reset_yearly),
			next_number=COALESCE($4,next_number), current_year=CASE WHEN $4::bigint IS NULL THEN current_year ELSE $5 END WHERE is_default`,
			in.Code, in.Pattern, in.ResetYearly, in.NextNumber, a.Now().In(lima).Year()); err != nil {
			return err
		}
		var e error
		s, e = a.loadSeries(r.Context(), tx)
		return e
	})
	if err != nil {
		if isUnique(err) {
			fail(w, 409, "Ya existe una serie con ese código.")
			return
		}
		a.internal(w, "patch series", err)
		return
	}
	jsonOut(w, 200, s)
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}
