package server

// VPN-only admin API for platform users and module entitlements (migration 013).
// Writes require: admin auth (IP allowlist + Basic), JSON body, X-DevBrain-Admin: 1 header (not settable
// cross-site without CORS preflight) and, when present, a same-host Origin. Every change is audited.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"devbrain/internal/entitle"

	"github.com/jackc/pgx/v5"
)

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

type adminUsers struct {
	a   *api
	ent *entitle.Cache
}

func adminWriteOK(r *http.Request) bool {
	if r.Header.Get("X-DevBrain-Admin") != "1" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Host != r.Host {
			return false
		}
	}
	return true
}

type puser struct {
	Email       string            `json:"email"`
	DisplayName string            `json:"display_name"`
	TenantID    *string           `json:"tenant_id"`
	Tenant      *string           `json:"tenant"`
	Enabled     bool              `json:"enabled"`
	Superadmin  bool              `json:"superadmin"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
	Modules     map[string]modEnt `json:"modules"`
}
type modEnt struct {
	Enabled bool   `json:"enabled"`
	Role    string `json:"role"`
}

const ats = `'YYYY-MM-DD"T"HH24:MI:SS"Z"'`

func (u *adminUsers) loadUser(ctx context.Context, q pgx.Tx, email string) (puser, error) {
	var p puser
	err := q.QueryRow(ctx, `SELECT u.email, u.display_name, u.tenant_id::text, t.name, u.enabled, u.superadmin,
		to_char(u.created_at AT TIME ZONE 'UTC',`+ats+`), to_char(u.updated_at AT TIME ZONE 'UTC',`+ats+`)
		FROM platform_users u LEFT JOIN platform_tenants() t ON t.id=u.tenant_id WHERE u.email=$1`, email).
		Scan(&p.Email, &p.DisplayName, &p.TenantID, &p.Tenant, &p.Enabled, &p.Superadmin, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return p, err
	}
	p.Modules = map[string]modEnt{}
	rows, err := q.Query(ctx, `SELECT module, enabled, COALESCE(role,'') FROM user_modules WHERE email=$1`, email)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var m modEnt
		if err := rows.Scan(&k, &m.Enabled, &m.Role); err != nil {
			return p, err
		}
		p.Modules[k] = m
	}
	return p, rows.Err()
}

func (u *adminUsers) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, u.a.d.DB, fn)
}

func entAudit(ctx context.Context, tx pgx.Tx, r *http.Request, action, email, module string, before, after any) error {
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	ip := ""
	if x := clientIP(r); x != nil {
		ip = x.String()
	}
	_, err := tx.Exec(ctx, `INSERT INTO entitlement_audit (actor, source_ip, action, email, module, before, after) VALUES ('admin-panel', $1, $2, $3, NULLIF($4,''), $5, $6)`,
		ip, action, email, module, b, a)
	return err
}

func (u *adminUsers) catalog(w http.ResponseWriter, r *http.Request) {
	type mod struct {
		Key         string   `json:"key"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Roles       []string `json:"roles"`
		DefaultRole *string  `json:"default_role"`
	}
	type ten struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	mods, tens := []mod{}, []ten{}
	err := u.tx(r.Context(), func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT key, name, description, roles, default_role FROM platform_modules ORDER BY sort, key`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var m mod
			if err := rows.Scan(&m.Key, &m.Name, &m.Description, &m.Roles, &m.DefaultRole); err != nil {
				return err
			}
			mods = append(mods, m)
		}
		rows.Close()
		rows, err = tx.Query(r.Context(), `SELECT id::text, name FROM platform_tenants()`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t ten
			if err := rows.Scan(&t.ID, &t.Name); err != nil {
				return err
			}
			tens = append(tens, t)
		}
		return rows.Err()
	})
	if err != nil {
		u.a.d.Log.Error("admin modules", "err", err)
		writeJSON(w, 500, map[string]string{"error": "internal"})
		return
	}
	writeJSON(w, 200, map[string]any{"modules": mods, "tenants": tens})
}

func (u *adminUsers) list(w http.ResponseWriter, r *http.Request) {
	out := []puser{}
	err := u.tx(r.Context(), func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT email FROM platform_users ORDER BY superadmin DESC, email`)
		if err != nil {
			return err
		}
		var emails []string
		for rows.Next() {
			var e string
			if err := rows.Scan(&e); err != nil {
				return err
			}
			emails = append(emails, e)
		}
		rows.Close()
		for _, e := range emails {
			p, err := u.loadUser(r.Context(), tx, e)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		u.a.d.Log.Error("admin users", "err", err)
		writeJSON(w, 500, map[string]string{"error": "internal"})
		return
	}
	writeJSON(w, 200, map[string]any{"users": out})
}

type userIn struct {
	Email       *string `json:"email"`
	DisplayName *string `json:"display_name"`
	TenantID    *string `json:"tenant_id"` // "" = none
	Enabled     *bool   `json:"enabled"`
}

func abad(w http.ResponseWriter, code int, msg string) { writeJSON(w, code, map[string]string{"error": msg}) }

func decodeStrict(r *http.Request, v any) error {
	d := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10))
	d.DisallowUnknownFields()
	return d.Decode(v)
}

func (u *adminUsers) create(w http.ResponseWriter, r *http.Request) {
	if !adminWriteOK(r) {
		abad(w, 403, "forbidden")
		return
	}
	var in userIn
	if err := decodeStrict(r, &in); err != nil || in.Email == nil {
		abad(w, 400, "JSON no válido (email requerido).")
		return
	}
	email := strings.ToLower(strings.TrimSpace(*in.Email))
	if !emailRe.MatchString(email) || len(email) > 254 {
		abad(w, 400, "Correo no válido.")
		return
	}
	name := ""
	if in.DisplayName != nil {
		name = strings.TrimSpace(*in.DisplayName)
	}
	var tid *string
	if in.TenantID != nil && *in.TenantID != "" {
		tid = in.TenantID
	}
	var p puser
	err := u.tx(r.Context(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `INSERT INTO platform_users (email, display_name, tenant_id, created_by) VALUES ($1,$2,$3::uuid,'admin-panel') ON CONFLICT DO NOTHING`, email, name, tid)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errExists
		}
		// default: chat only
		if _, err := tx.Exec(r.Context(), `INSERT INTO user_modules (email, module, enabled, updated_by) VALUES ($1,'chat',true,'admin-panel')`, email); err != nil {
			return err
		}
		if p, err = u.loadUser(r.Context(), tx, email); err != nil {
			return err
		}
		return entAudit(r.Context(), tx, r, "user.create", email, "", nil, p)
	})
	if errors.Is(err, errExists) {
		abad(w, 409, "Ese usuario ya existe.")
		return
	}
	if err != nil {
		u.a.d.Log.Error("admin create user", "err", err)
		abad(w, 400, "No se pudo crear (¿tenant válido?).")
		return
	}
	u.ent.Invalidate(email)
	writeJSON(w, 201, p)
}

var errExists = errors.New("exists")
var errSuper = errors.New("superadmin")

func (u *adminUsers) patch(w http.ResponseWriter, r *http.Request) {
	if !adminWriteOK(r) {
		abad(w, 403, "forbidden")
		return
	}
	email := strings.ToLower(r.PathValue("email"))
	var in userIn
	if err := decodeStrict(r, &in); err != nil || in.Email != nil {
		abad(w, 400, "JSON no válido.")
		return
	}
	var p puser
	err := u.tx(r.Context(), func(tx pgx.Tx) error {
		before, err := u.loadUser(r.Context(), tx, email)
		if err != nil {
			return err
		}
		if before.Superadmin && ((in.Enabled != nil && !*in.Enabled) || in.TenantID != nil) {
			return errSuper
		}
		var tid any = nil
		setT := in.TenantID != nil
		if setT && *in.TenantID != "" {
			tid = *in.TenantID
		}
		if _, err := tx.Exec(r.Context(), `UPDATE platform_users SET display_name=COALESCE($2,display_name), enabled=COALESCE($3,enabled),
			tenant_id=CASE WHEN $4 THEN $5::uuid ELSE tenant_id END, updated_at=now() WHERE email=$1`, email, in.DisplayName, in.Enabled, setT, tid); err != nil {
			return err
		}
		if p, err = u.loadUser(r.Context(), tx, email); err != nil {
			return err
		}
		return entAudit(r.Context(), tx, r, "user.update", email, "", before, p)
	})
	u.respond(w, err, email, p)
}

func (u *adminUsers) respond(w http.ResponseWriter, err error, email string, p puser) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		abad(w, 404, "Usuario no encontrado.")
	case errors.Is(err, errSuper):
		abad(w, 409, "El superadmin no se puede deshabilitar, cambiar de tenant ni quitarle módulos (evita bloquearte).")
	case errors.Is(err, errBadRole):
		abad(w, 400, "Rol no válido para ese módulo.")
	case err != nil:
		u.a.d.Log.Error("admin entitlement", "err", err)
		abad(w, 400, "No se pudo guardar.")
	default:
		u.ent.Invalidate(email)
		writeJSON(w, 200, p)
	}
}

var errBadRole = errors.New("bad role")

func (u *adminUsers) setModule(w http.ResponseWriter, r *http.Request) {
	if !adminWriteOK(r) {
		abad(w, 403, "forbidden")
		return
	}
	email, mod := strings.ToLower(r.PathValue("email")), r.PathValue("module")
	var in struct {
		Enabled bool    `json:"enabled"`
		Role    *string `json:"role"`
	}
	if err := decodeStrict(r, &in); err != nil {
		abad(w, 400, "JSON no válido.")
		return
	}
	var p puser
	err := u.tx(r.Context(), func(tx pgx.Tx) error {
		before, err := u.loadUser(r.Context(), tx, email)
		if err != nil {
			return err
		}
		if before.Superadmin {
			return errSuper
		}
		var roles []string
		var def *string
		if err := tx.QueryRow(r.Context(), `SELECT roles, default_role FROM platform_modules WHERE key=$1`, mod).Scan(&roles, &def); err != nil {
			return err
		}
		role := def
		if old, ok := before.Modules[mod]; ok && old.Role != "" {
			role = &old.Role
		}
		if in.Role != nil {
			role = in.Role
		}
		if len(roles) == 0 {
			role = nil
		} else if role == nil || !hasStr(roles, *role) {
			return errBadRole
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO user_modules (email, module, enabled, role, updated_by, updated_at) VALUES ($1,$2,$3,$4,'admin-panel',now())
			ON CONFLICT (email, module) DO UPDATE SET enabled=EXCLUDED.enabled, role=EXCLUDED.role, updated_by=EXCLUDED.updated_by, updated_at=now()`, email, mod, in.Enabled, role); err != nil {
			return err
		}
		if p, err = u.loadUser(r.Context(), tx, email); err != nil {
			return err
		}
		return entAudit(r.Context(), tx, r, "module.set", email, mod, before.Modules[mod], p.Modules[mod])
	})
	u.respond(w, err, email, p)
}

func hasStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func (u *adminUsers) auditLog(w http.ResponseWriter, r *http.Request) {
	type row struct {
		ID     int64           `json:"id"`
		At     string          `json:"at"`
		Actor  string          `json:"actor"`
		IP     *string         `json:"source_ip"`
		Action string          `json:"action"`
		Email  string          `json:"email"`
		Module *string         `json:"module"`
		Before json.RawMessage `json:"before"`
		After  json.RawMessage `json:"after"`
	}
	out := []row{}
	email := strings.ToLower(r.URL.Query().Get("email"))
	err := u.tx(r.Context(), func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT id, to_char(at AT TIME ZONE 'UTC',`+ats+`), actor, source_ip, action, email, module, COALESCE(before,'null'), COALESCE(after,'null')
			FROM entitlement_audit WHERE ($1='' OR email=$1) ORDER BY id DESC LIMIT 200`, email)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x row
			if err := rows.Scan(&x.ID, &x.At, &x.Actor, &x.IP, &x.Action, &x.Email, &x.Module, &x.Before, &x.After); err != nil {
				return err
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	if err != nil {
		u.a.d.Log.Error("admin audit", "err", err)
		abad(w, 500, "internal")
		return
	}
	writeJSON(w, 200, map[string]any{"audit": out})
}

func (a *AdminAuth) mountUsers(route func(string, http.Handler), ap *api) {
	if ap.d.Commercial == nil || ap.d.Commercial.Ent == nil {
		return
	}
	u := &adminUsers{a: ap, ent: ap.d.Commercial.Ent}
	route("GET /admin/api/modules", a.wrap(http.HandlerFunc(u.catalog)))
	route("GET /admin/api/users", a.wrap(http.HandlerFunc(u.list)))
	route("POST /admin/api/users", a.wrap(http.HandlerFunc(u.create)))
	route("PATCH /admin/api/users/{email}", a.wrap(http.HandlerFunc(u.patch)))
	route("PUT /admin/api/users/{email}/modules/{module}", a.wrap(http.HandlerFunc(u.setModule)))
	route("GET /admin/api/audit", a.wrap(http.HandlerFunc(u.auditLog)))
}
