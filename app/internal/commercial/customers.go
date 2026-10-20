package commercial

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// CustomerData is the normalized customer shape (local record or free/one-off snapshot).
// Absent fields stay empty; nothing is invented.
type CustomerData struct {
	DisplayName  string `json:"display_name"`
	LegalName    string `json:"legal_name,omitempty"`
	DocumentType string `json:"document_type,omitempty"`
	TaxID        string `json:"tax_id,omitempty"`
	Email        string `json:"email,omitempty"`
	Phone        string `json:"phone,omitempty"`
	ContactName  string `json:"contact_name,omitempty"`
	Address      string `json:"address,omitempty"`
	Notes        string `json:"notes,omitempty"`
}

type Customer struct {
	ID string `json:"id"`
	CustomerData
	Source    string `json:"source"` // "local" (future: "facturador", "crm", "importado")
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

var nonAlnum = regexp.MustCompile(`[^0-9A-Za-z]`)
var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// rucValid applies SUNAT's modulo-11 check digit.
func rucValid(r string) bool {
	if len(r) != 11 || !regexp.MustCompile(`^(10|15|16|17|20)\d{9}$`).MatchString(r) {
		return false
	}
	w := []int{5, 4, 3, 2, 7, 6, 5, 4, 3, 2}
	s := 0
	for i := 0; i < 10; i++ {
		s += int(r[i]-'0') * w[i]
	}
	d := 11 - s%11
	if d == 10 {
		d = 0
	} else if d == 11 {
		d = 1
	}
	return int(r[10]-'0') == d
}

// normTaxID validates per document type and returns the normalized identifier.
func normTaxID(docType, v string) (string, bool) {
	n := strings.ToUpper(nonAlnum.ReplaceAllString(v, ""))
	switch docType {
	case "RUC":
		return n, rucValid(n)
	case "DNI":
		return n, regexp.MustCompile(`^\d{8}$`).MatchString(n)
	case "CE", "PAS", "OTRO":
		return n, len(n) >= 4 && len(n) <= 20
	case "":
		return "", strings.TrimSpace(v) == ""
	}
	return "", false
}

// Validate trims and validates customer data. Returns the normalized tax id (nil if none).
func (c *CustomerData) Validate() (*string, string) {
	for _, p := range []*string{&c.DisplayName, &c.LegalName, &c.DocumentType, &c.TaxID, &c.Email, &c.Phone, &c.ContactName, &c.Address, &c.Notes} {
		*p = strings.TrimSpace(*p)
	}
	c.DocumentType = strings.ToUpper(c.DocumentType)
	if c.DisplayName == "" {
		c.DisplayName = c.LegalName
	}
	if c.DisplayName == "" {
		return nil, "Falta el nombre o razón social del cliente."
	}
	if len(c.DisplayName) > 200 || len(c.Notes) > 4000 {
		return nil, "Texto demasiado largo."
	}
	if c.Email != "" && !emailRe.MatchString(c.Email) {
		return nil, "Correo no válido."
	}
	if c.TaxID != "" && c.DocumentType == "" {
		return nil, "Indica el tipo de documento (RUC, DNI, CE, PAS u OTRO)."
	}
	n, ok := normTaxID(c.DocumentType, c.TaxID)
	if !ok {
		return nil, "Número de documento no válido para " + c.DocumentType + "."
	}
	if n == "" {
		return nil, ""
	}
	c.TaxID = n
	return &n, ""
}

const customerCols = `id::text, display_name, legal_name, document_type, tax_id, email, phone, contact_name, address, notes, status, to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"')`

func scanCustomer(row pgx.Row) (Customer, error) {
	var c Customer
	err := row.Scan(&c.ID, &c.DisplayName, &c.LegalName, &c.DocumentType, &c.TaxID, &c.Email, &c.Phone, &c.ContactName, &c.Address, &c.Notes, &c.Status, &c.CreatedAt)
	c.Source = "local"
	return c, err
}

func (a *API) listCustomers(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	out := []Customer{}
	err := a.tx(r, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), "SELECT "+customerCols+` FROM commercial_customers WHERE status='active' AND
			($1 = '' OR display_name ILIKE '%'||$1||'%' OR legal_name ILIKE '%'||$1||'%' OR tax_id_norm LIKE upper($2)||'%')
			ORDER BY lower(display_name) LIMIT 50`, q, nonAlnum.ReplaceAllString(q, ""))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCustomer(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		a.internal(w, "list customers", err)
		return
	}
	// Phase B: merge results from enabled providers (labelled by source; failures become warnings).
	jsonOut(w, 200, map[string]any{"customers": out, "sources": []string{"local"}, "warnings": []string{}})
}

func (a *API) createCustomer(w http.ResponseWriter, r *http.Request) {
	var in CustomerData
	if !decode(w, r, &in) {
		return
	}
	norm, msg := in.Validate()
	if msg != "" {
		fail(w, 400, msg)
		return
	}
	var c Customer
	err := a.tx(r, func(tx pgx.Tx) (e error) {
		c, e = scanCustomer(tx.QueryRow(r.Context(), `INSERT INTO commercial_customers (tenant_id, display_name, legal_name, document_type, tax_id, tax_id_norm, email, phone, contact_name, address, notes, created_by)
			VALUES (current_setting('app.tenant_id')::uuid,$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING `+customerCols,
			in.DisplayName, in.LegalName, in.DocumentType, in.TaxID, norm, in.Email, in.Phone, in.ContactName, in.Address, in.Notes, actorOf(r).Email))
		return
	})
	if isUnique(err) {
		fail(w, 409, "Ya existe un cliente con ese documento.")
		return
	} else if err != nil {
		a.internal(w, "create customer", err)
		return
	}
	jsonOut(w, 201, c)
}

func (a *API) getCustomer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var c Customer
	err := a.tx(r, func(tx pgx.Tx) (e error) {
		c, e = scanCustomer(tx.QueryRow(r.Context(), "SELECT "+customerCols+" FROM commercial_customers WHERE id=$1", id))
		return
	})
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 404, "No encontrado.")
		return
	} else if err != nil {
		a.internal(w, "get customer", err)
		return
	}
	jsonOut(w, 200, c)
}

func (a *API) patchCustomer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		CustomerData
		Status string `json:"status"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Status != "" && in.Status != "active" && in.Status != "archived" {
		fail(w, 400, "Estado no válido.")
		return
	}
	norm, msg := in.CustomerData.Validate()
	if msg != "" {
		fail(w, 400, msg)
		return
	}
	var c Customer
	err := a.tx(r, func(tx pgx.Tx) (e error) { // full replacement of the editable fields
		c, e = scanCustomer(tx.QueryRow(r.Context(), `UPDATE commercial_customers SET display_name=$2, legal_name=$3, document_type=$4, tax_id=$5, tax_id_norm=$6,
			email=$7, phone=$8, contact_name=$9, address=$10, notes=$11, status=COALESCE(NULLIF($12,''),status), updated_at=now() WHERE id=$1 RETURNING `+customerCols,
			id, in.DisplayName, in.LegalName, in.DocumentType, in.TaxID, norm, in.Email, in.Phone, in.ContactName, in.Address, in.Notes, in.Status))
		return
	})
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 404, "No encontrado.")
		return
	} else if isUnique(err) {
		fail(w, 409, "Ya existe un cliente con ese documento.")
		return
	} else if err != nil {
		a.internal(w, "patch customer", err)
		return
	}
	jsonOut(w, 200, c)
}
