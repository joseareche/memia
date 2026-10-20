// Package commercial implements the Cotizador IA backend (Phase A: local customers, quotes,
// deterministic money engine, state machine, numbering, versions, audit, minimal RBAC).
package commercial

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// Dec is an exact decimal backed by big.Rat. Never use float64 for money.
type Dec struct{ r big.Rat }

func ParseDec(s string) (Dec, error) {
	var d Dec
	s = strings.TrimSpace(s)
	if s == "" {
		return d, nil
	}
	if strings.ContainsAny(s, "eE/") { // only plain decimal notation
		return d, fmt.Errorf("número no válido: %q", s)
	}
	if _, ok := d.r.SetString(s); !ok {
		return d, fmt.Errorf("número no válido: %q", s)
	}
	return d, nil
}

func MustDec(s string) Dec {
	d, err := ParseDec(s)
	if err != nil {
		panic(err)
	}
	return d
}

func (a Dec) Add(b Dec) Dec { var d Dec; d.r.Add(&a.r, &b.r); return d }
func (a Dec) Sub(b Dec) Dec { var d Dec; d.r.Sub(&a.r, &b.r); return d }
func (a Dec) Mul(b Dec) Dec { var d Dec; d.r.Mul(&a.r, &b.r); return d }
func (a Dec) Div(b Dec) Dec { var d Dec; d.r.Quo(&a.r, &b.r); return d }
func (a Dec) Cmp(b Dec) int { return a.r.Cmp(&b.r) }
func (a Dec) Sign() int     { return a.r.Sign() }
func (a Dec) IsZero() bool  { return a.r.Sign() == 0 }

// Round rounds half away from zero ("half-up" for money) to n decimals.
func (a Dec) Round(n int) Dec {
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
	num := new(big.Int).Mul(a.r.Num(), scale)
	den := a.r.Denom()
	q, m := new(big.Int).QuoRem(num, den, new(big.Int))
	// compare 2*|m| with den
	m2 := new(big.Int).Mul(new(big.Int).Abs(m), big.NewInt(2))
	if m2.Cmp(den) >= 0 {
		if num.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	var d Dec
	d.r.SetFrac(q, scale)
	return d
}

// String renders with exactly n=2 decimals after rounding (money presentation).
func (a Dec) String() string { r := a.Round(2); return r.r.FloatString(2) }

// Fixed renders with n decimals (exact rounding, used for qty/rates).
func (a Dec) Fixed(n int) string { r := a.Round(n); return r.r.FloatString(n) }

var (
	hundred = MustDec("100")
	one     = MustDec("1")
)

// ---------------- engine ----------------

var BillingModes = []string{"one_time", "monthly", "annual", "usage"}

var validMode = map[string]bool{"one_time": true, "monthly": true, "annual": true, "usage": true}
var validTax = map[string]bool{"gravado": true, "exonerado": true, "inafecto": true}

type LineIn struct {
	Description    string `json:"description"`
	Unit           string `json:"unit"`
	Qty            string `json:"qty"`
	UnitPrice      string `json:"unit_price"`
	UnitCost       string `json:"unit_cost,omitempty"`
	DiscountPct    string `json:"discount_pct,omitempty"`
	DiscountAmount string `json:"discount_amount,omitempty"`
	BillingMode    string `json:"billing_mode"`
	TaxRule        string `json:"tax_rule"`
	Currency       string `json:"currency,omitempty"` // optional; must equal the quote currency (no silent mixing)
}

type CalcIn struct {
	Currency          string   `json:"currency"`
	PricesIncludeIGV  bool     `json:"prices_include_igv"`
	IGVRate           string   `json:"igv_rate"`
	GlobalDiscountPct string   `json:"global_discount_pct,omitempty"`
	Lines             []LineIn `json:"lines"`
}

type LineOut struct {
	N        int    `json:"n"`
	Gross    string `json:"gross"`    // qty × unit price (as entered)
	Discount string `json:"discount"` // line + global discount, same basis as the entered price
	Base     string `json:"base"`     // taxable/non-taxable base, without IGV
	IGV      string `json:"igv"`
	Total    string `json:"total"`
	Mode     string `json:"billing_mode"`
	TaxRule  string `json:"tax_rule"`
}

type CycleTotals struct {
	Gravada   string `json:"op_gravada"`
	Exonerada string `json:"op_exonerada"`
	Inafecta  string `json:"op_inafecta"`
	Discount  string `json:"discount"`
	IGV       string `json:"igv"`
	Total     string `json:"total"`
	Lines     int    `json:"lines"`
}

type CalcOut struct {
	Currency         string                 `json:"currency"`
	PricesIncludeIGV bool                   `json:"prices_include_igv"`
	IGVRate          string                 `json:"igv_rate"`
	Lines            []LineOut              `json:"lines"`
	Cycles           map[string]CycleTotals `json:"cycles"` // one_time | monthly | annual | usage — never summed together
	Notes            []string               `json:"notes,omitempty"`
}

type CalcError struct{ Msg string }

func (e *CalcError) Error() string { return e.Msg }

func cerr(f string, a ...any) error { return &CalcError{fmt.Sprintf(f, a...)} }

func opt(s string) (Dec, error) { return ParseDec(s) }

// Calculate is deterministic: per line, amounts are rounded to 2 decimals (half-up) and cycle
// totals are sums of rounded line amounts, so printed lines always add up to printed totals.
func Calculate(in CalcIn) (CalcOut, error) {
	out := CalcOut{Currency: in.Currency, PricesIncludeIGV: in.PricesIncludeIGV, Cycles: map[string]CycleTotals{}}
	if in.Currency != "PEN" && in.Currency != "USD" {
		return out, cerr("moneda no soportada: %q (PEN o USD)", in.Currency)
	}
	rate, err := opt(in.IGVRate)
	if err != nil || rate.Sign() < 0 || rate.Cmp(one) >= 0 {
		return out, cerr("tasa de IGV no válida: %q (fracción, p. ej. 0.18)", in.IGVRate)
	}
	out.IGVRate = rate.Fixed(4)
	gpct, err := opt(in.GlobalDiscountPct)
	if err != nil || gpct.Sign() < 0 || gpct.Cmp(hundred) > 0 {
		return out, cerr("descuento global no válido: %q", in.GlobalDiscountPct)
	}
	if len(in.Lines) > 500 {
		return out, cerr("demasiadas líneas (máx. 500)")
	}
	type acc struct {
		grav, exo, ina, disc, igv, tot Dec
		n                              int
	}
	sums := map[string]*acc{}
	for i, l := range in.Lines {
		n := i + 1
		if strings.TrimSpace(l.Description) == "" {
			return out, cerr("línea %d: falta la descripción", n)
		}
		if l.Currency != "" && l.Currency != in.Currency {
			return out, cerr("línea %d: moneda %s distinta de la cotización (%s); convierta explícitamente con tipo de cambio", n, l.Currency, in.Currency)
		}
		if !validMode[l.BillingMode] {
			return out, cerr("línea %d: modalidad no válida %q", n, l.BillingMode)
		}
		if !validTax[l.TaxRule] {
			return out, cerr("línea %d: régimen tributario no válido %q", n, l.TaxRule)
		}
		qty, err := ParseDec(l.Qty)
		if err != nil || qty.Sign() <= 0 {
			return out, cerr("línea %d: cantidad no válida", n)
		}
		price, err := ParseDec(l.UnitPrice)
		if err != nil || price.Sign() < 0 || strings.TrimSpace(l.UnitPrice) == "" {
			return out, cerr("línea %d: precio unitario no válido", n)
		}
		pct, err := opt(l.DiscountPct)
		if err != nil || pct.Sign() < 0 || pct.Cmp(hundred) > 0 {
			return out, cerr("línea %d: descuento %% no válido", n)
		}
		damt, err := opt(l.DiscountAmount)
		if err != nil || damt.Sign() < 0 {
			return out, cerr("línea %d: descuento por importe no válido", n)
		}
		gross := qty.Mul(price).Round(2)
		disc := gross.Mul(pct).Div(hundred).Add(damt)
		if disc.Cmp(gross) > 0 {
			return out, cerr("línea %d: el descuento supera el importe", n)
		}
		net := gross.Sub(disc)
		gdisc := net.Mul(gpct).Div(hundred)
		net = net.Sub(gdisc).Round(2)
		discTotal := gross.Sub(net) // rounded consistently
		var base, igv, total Dec
		switch {
		case l.TaxRule != "gravado":
			base, total = net, net
		case in.PricesIncludeIGV:
			total = net
			base = net.Div(one.Add(rate)).Round(2)
			igv = total.Sub(base)
		default:
			base = net
			igv = base.Mul(rate).Round(2)
			total = base.Add(igv)
		}
		a := sums[l.BillingMode]
		if a == nil {
			a = &acc{}
			sums[l.BillingMode] = a
		}
		switch l.TaxRule {
		case "gravado":
			a.grav = a.grav.Add(base)
		case "exonerado":
			a.exo = a.exo.Add(base)
		default:
			a.ina = a.ina.Add(base)
		}
		a.disc, a.igv, a.tot, a.n = a.disc.Add(discTotal), a.igv.Add(igv), a.tot.Add(total), a.n+1
		out.Lines = append(out.Lines, LineOut{N: n, Gross: gross.String(), Discount: discTotal.String(), Base: base.String(), IGV: igv.String(), Total: total.String(), Mode: l.BillingMode, TaxRule: l.TaxRule})
	}
	for _, m := range BillingModes {
		if a := sums[m]; a != nil {
			out.Cycles[m] = CycleTotals{a.grav.String(), a.exo.String(), a.ina.String(), a.disc.String(), a.igv.String(), a.tot.String(), a.n}
		}
	}
	if len(out.Cycles) > 1 {
		out.Notes = append(out.Notes, "Los importes de pago único, recurrentes y de consumo se presentan por separado y no se suman entre sí.")
	}
	if _, ok := out.Cycles["usage"]; ok {
		out.Notes = append(out.Notes, "Consumo variable: importe estimado según la cantidad indicada; se factura según uso real.")
	}
	return out, nil
}

var ErrNotFound = errors.New("not found")
