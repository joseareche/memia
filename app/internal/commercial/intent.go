package commercial

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Deterministic Spanish intent parser for "genérame una cotización del RUC X de <servicio> ..." → editable draft.
// Never invents prices or razón social: prices come only from the catalog (with source/validity), else they are asked.

var (
	intentRe = regexp.MustCompile(`(?i)\b(cotiza\w*|cotizaci[oó]n\w*|presupuesto|proforma)\b`)
	rucRe    = regexp.MustCompile(`\b((?:10|15|16|17|20)\d{9})\b`)
	dniRe    = regexp.MustCompile(`(?i)\bdni\s*:?\s*(\d{8})\b`)
	mailRe   = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	sendRe   = regexp.MustCompile(`(?i)\b(env[ií]a\w*|m[aá]nda\w*|remite\w*)\b`)
	qtyRe    = regexp.MustCompile(`(?i)\b(\d{1,5})\s*(?:x\s+|unidades?\s+|licencias?\s+|anexos?\s+|usuarios?\s+|planes\s+|meses\s+)`)
	usdRe    = regexp.MustCompile(`(?i)\b(usd|d[oó]lares|us\$)\b`)
)

type IntentResult struct {
	IsQuote  bool           `json:"is_quote"`
	Draft    QuoteIn        `json:"draft"`
	Customer map[string]any `json:"customer"`
	Matches  []map[string]any `json:"matches"`
	Missing  []string       `json:"missing"`
	Warnings []string       `json:"warnings"`
	Send     map[string]any `json:"send"`
}

func DetectQuoteIntent(text string) bool { return intentRe.MatchString(text) }

func (a *API) intent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &in) {
		return
	}
	text := strings.TrimSpace(in.Text)
	if len(text) > 2000 {
		fail(w, 400, "Texto demasiado largo.")
		return
	}
	res := IntentResult{IsQuote: DetectQuoteIntent(text), Missing: []string{}, Warnings: []string{}, Matches: []map[string]any{}}
	if !res.IsQuote {
		jsonOut(w, 200, res)
		return
	}
	err := a.tx(r, func(tx pgx.Tx) error {
		s, err := loadSettings(r.Context(), tx)
		if err != nil {
			return err
		}
		cur := s.DefaultCurrency
		if usdRe.MatchString(text) {
			cur = "USD"
		}
		inc := s.PricesIncludeIGV
		res.Draft = QuoteIn{Currency: &cur, PricesIncludeIGV: &inc, IGVRate: &s.IGVRate}

		// --- customer ---
		docType, num := "", ""
		if m := rucRe.FindStringSubmatch(text); m != nil {
			docType, num = "RUC", m[1]
		} else if m := dniRe.FindStringSubmatch(text); m != nil {
			docType, num = "DNI", m[1]
		}
		switch {
		case num == "":
			res.Missing = append(res.Missing, "customer")
			res.Customer = map[string]any{"status": "missing"}
		default:
			norm, ok := normTaxID(docType, num)
			if !ok {
				res.Warnings = append(res.Warnings, docType+" "+num+" no supera la validación (dígito verificador).")
				res.Missing = append(res.Missing, "customer")
				res.Customer = map[string]any{"status": "invalid", "document_type": docType, "tax_id": num}
				break
			}
			cu, err := scanCustomer(tx.QueryRow(r.Context(), "SELECT "+customerCols+" FROM commercial_customers WHERE document_type=$1 AND tax_id_norm=$2 AND status='active'", docType, norm))
			if err == nil {
				res.Draft.Customer = &CustomerRef{Kind: "local", ID: cu.ID}
				res.Customer = map[string]any{"status": "local", "id": cu.ID, "display_name": cu.DisplayName, "document_type": docType, "tax_id": num, "source": "Cliente registrado"}
				break
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			look := a.ruc()
			info, lerr := look.Lookup(r.Context(), num)
			if lerr == nil && info != nil && info.LegalName != "" {
				d := CustomerData{DisplayName: info.LegalName, LegalName: info.LegalName, DocumentType: docType, TaxID: num, Address: info.Address}
				res.Draft.Customer = &CustomerRef{Kind: "free", Data: &d}
				res.Customer = map[string]any{"status": "lookup", "display_name": info.LegalName, "document_type": docType, "tax_id": num, "source": info.Source}
				break
			}
			// unknown legal name: free customer with the document only, flagged; never invent the razón social
			d := CustomerData{DisplayName: docType + " " + num, DocumentType: docType, TaxID: num}
			res.Draft.Customer = &CustomerRef{Kind: "free", Data: &d}
			res.Customer = map[string]any{"status": "unverified", "display_name": d.DisplayName, "document_type": docType, "tax_id": num,
				"source": "Consulta RUC no configurada — razón social pendiente"}
			res.Missing = append(res.Missing, "legal_name")
		}

		// --- items from catalog ---
		svcs, err := a.loadServices(r.Context(), tx, false)
		if err != nil {
			return err
		}
		qty := "1"
		if m := qtyRe.FindStringSubmatch(text); m != nil {
			if n, _ := strconv.Atoi(m[1]); n > 0 {
				qty = m[1]
			}
		}
		ms := MatchServices(text, svcs)
		items := []ItemIn{}
		if len(ms) == 0 {
			res.Missing = append(res.Missing, "service")
			res.Warnings = append(res.Warnings, "No encontré el servicio en el catálogo. Indica el concepto y su precio (puedo guardarlo en el catálogo).")
		} else {
			m := ms[0]
			sv := m.Service
			it := ItemIn{LineIn: LineIn{Description: sv.Name, Unit: sv.Unit, Qty: qty, BillingMode: sv.BillingMode, TaxRule: sv.TaxRule}}
			match := map[string]any{"service_id": sv.ID, "code": sv.Code, "name": sv.Name, "description": sv.Description, "confidence": m.Confidence, "reason": m.Reason, "billing_mode": sv.BillingMode}
			if p := sv.Current; p != nil && p.Currency == cur {
				price := p.UnitPrice
				// keep the quote's IGV basis: convert only when the catalog basis differs (exact, rounded to 4 dp)
				if p.PriceIncludesIGV != inc {
					rate := MustDec(s.IGVRate)
					d := MustDec(p.UnitPrice)
					if p.PriceIncludesIGV {
						d = d.Div(one.Add(rate))
					} else {
						d = d.Mul(one.Add(rate))
					}
					price = trimDec(d.Fixed(4))
					res.Warnings = append(res.Warnings, "Precio de catálogo convertido a la base de IGV de la cotización.")
				}
				it.UnitPrice = price
				src, _ := jsonMarshal(map[string]any{"kind": "catalog", "service_id": sv.ID, "price_version_id": p.ID, "valid_from": p.ValidFrom, "valid_to": p.ValidTo,
					"verified": p.Verified, "origin": p.Source, "confidence": m.Confidence})
				it.PriceSource = src
				match["price"] = p
				if !p.Verified {
					res.Warnings = append(res.Warnings, "El precio de «"+sv.Name+"» proviene de notas y está pendiente de confirmar.")
				}
			} else {
				res.Missing = append(res.Missing, "price")
				match["price"] = nil
			}
			res.Matches = append(res.Matches, match)
			items = append(items, it)
			for _, alt := range ms[1:] {
				if len(res.Matches) >= 3 {
					break
				}
				res.Matches = append(res.Matches, map[string]any{"service_id": alt.Service.ID, "name": alt.Service.Name, "confidence": alt.Confidence, "alternative": true})
			}
		}
		res.Draft.Items = &items
		title := "Cotización"
		if len(ms) > 0 {
			title = ms[0].Service.Name
		}
		res.Draft.Title = &title
		if c, err := a.companyDefaults(r, tx); err == nil && c.DefaultTerms != "" {
			res.Draft.Terms = &c.DefaultTerms
		}

		// --- delivery (mail adapter disabled; only reported) ---
		to := mailRe.FindString(text)
		res.Send = map[string]any{"requested": sendRe.MatchString(text) || to != "", "to": strings.ToLower(to), "available": a.mailer().Enabled(), "adapter": a.mailer().Key()}
		return nil
	})
	if err != nil {
		a.internal(w, "intent", err)
		return
	}
	jsonOut(w, 200, res)
}
