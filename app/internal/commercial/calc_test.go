package commercial

import (
	"strings"
	"testing"
)

func TestRoundHalfUp(t *testing.T) {
	cases := map[string]string{"1.005": "1.01", "1.004": "1.00", "2.675": "2.68", "-1.005": "-1.01", "0.125": "0.13", "10": "10.00", "0.0049": "0.00", "999999999.995": "1000000000.00"}
	for in, want := range cases {
		if got := MustDec(in).String(); got != want {
			t.Errorf("round(%s)=%s want %s", in, got, want)
		}
	}
	if _, err := ParseDec("1e3"); err == nil {
		t.Error("scientific notation must be rejected")
	}
	if _, err := ParseDec("1/3"); err == nil {
		t.Error("fractions must be rejected")
	}
}

func line(desc, qty, price, mode, tax string) LineIn {
	return LineIn{Description: desc, Qty: qty, UnitPrice: price, BillingMode: mode, TaxRule: tax}
}

func calc(t *testing.T, in CalcIn) CalcOut {
	t.Helper()
	out, err := Calculate(in)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestIGVExcluded(t *testing.T) {
	// 90 anexos × 25.00 = 2250.00 ; IGV 18% = 405.00 ; total 2655.00
	out := calc(t, CalcIn{Currency: "PEN", IGVRate: "0.18", Lines: []LineIn{line("Anexo IP", "90", "25", "monthly", "gravado")}})
	c := out.Cycles["monthly"]
	if c.Gravada != "2250.00" || c.IGV != "405.00" || c.Total != "2655.00" {
		t.Fatalf("%+v", c)
	}
}

func TestIGVIncluded(t *testing.T) {
	// price 118.00 incl. IGV → base 100.00, IGV 18.00
	out := calc(t, CalcIn{Currency: "PEN", IGVRate: "0.18", PricesIncludeIGV: true, Lines: []LineIn{line("Licencia", "1", "118", "one_time", "gravado")}})
	c := out.Cycles["one_time"]
	if c.Gravada != "100.00" || c.IGV != "18.00" || c.Total != "118.00" {
		t.Fatalf("%+v", c)
	}
	// 3 × 9.99 incl. → total 29.97, base round(29.97/1.18)=25.40, igv 4.57
	out = calc(t, CalcIn{Currency: "PEN", IGVRate: "0.18", PricesIncludeIGV: true, Lines: []LineIn{line("x", "3", "9.99", "one_time", "gravado")}})
	c = out.Cycles["one_time"]
	if c.Gravada != "25.40" || c.IGV != "4.57" || c.Total != "29.97" {
		t.Fatalf("%+v", c)
	}
}

func TestExoneratedAndInafecto(t *testing.T) {
	out := calc(t, CalcIn{Currency: "PEN", IGVRate: "0.18", Lines: []LineIn{
		line("Servicio gravado", "1", "100", "one_time", "gravado"),
		line("Capacitación exonerada", "1", "50", "one_time", "exonerado"),
		line("Servicio inafecto", "2", "10", "one_time", "inafecto"),
	}})
	c := out.Cycles["one_time"]
	if c.Gravada != "100.00" || c.Exonerada != "50.00" || c.Inafecta != "20.00" || c.IGV != "18.00" || c.Total != "188.00" || c.Lines != 3 {
		t.Fatalf("%+v", c)
	}
	// included-IGV pricing never extracts IGV from exempt lines
	out = calc(t, CalcIn{Currency: "PEN", IGVRate: "0.18", PricesIncludeIGV: true, Lines: []LineIn{line("Exonerado", "1", "50", "one_time", "exonerado")}})
	if c := out.Cycles["one_time"]; c.Exonerada != "50.00" || c.IGV != "0.00" || c.Total != "50.00" {
		t.Fatalf("%+v", c)
	}
}

func TestLineAndGlobalDiscounts(t *testing.T) {
	l := line("Troncal SIP", "10", "40", "monthly", "gravado")
	l.DiscountPct = "10"                                                                                 // 400 → 360
	l.DiscountAmount = "10"                                                                              // → 350 (pct over gross, amount on top)
	out := calc(t, CalcIn{Currency: "USD", IGVRate: "0.18", GlobalDiscountPct: "5", Lines: []LineIn{l}}) // 350 × 0.95 = 332.50
	c := out.Cycles["monthly"]
	if c.Gravada != "332.50" || c.Discount != "67.50" || c.IGV != "59.85" || c.Total != "392.35" {
		t.Fatalf("%+v", c)
	}
	if out.Lines[0].Gross != "400.00" || out.Lines[0].Discount != "67.50" {
		t.Fatalf("%+v", out.Lines[0])
	}
	// 100% discount is allowed (free item); over 100% or above gross is not
	l2 := line("Regalo", "1", "10", "one_time", "gravado")
	l2.DiscountPct = "100"
	if c := calc(t, CalcIn{Currency: "PEN", IGVRate: "0.18", Lines: []LineIn{l2}}).Cycles["one_time"]; c.Total != "0.00" {
		t.Fatalf("%+v", c)
	}
	l2.DiscountAmount = "0.01"
	if _, err := Calculate(CalcIn{Currency: "PEN", IGVRate: "0.18", Lines: []LineIn{l2}}); err == nil {
		t.Fatal("discount above gross accepted")
	}
}

func TestCyclesSeparated(t *testing.T) {
	out := calc(t, CalcIn{Currency: "PEN", IGVRate: "0.18", Lines: []LineIn{
		line("Implementación", "1", "1500", "one_time", "gravado"),
		line("Anexos", "90", "25", "monthly", "gravado"),
		line("Soporte anual", "1", "1200", "annual", "gravado"),
		line("Minutos salientes", "10000", "0.035", "usage", "gravado"),
	}})
	want := map[string]string{"one_time": "1770.00", "monthly": "2655.00", "annual": "1416.00", "usage": "413.00"}
	for m, tot := range want {
		if out.Cycles[m].Total != tot {
			t.Errorf("%s total=%s want %s", m, out.Cycles[m].Total, tot)
		}
	}
	if len(out.Cycles) != 4 || len(out.Notes) != 2 || !strings.Contains(out.Notes[0], "no se suman") {
		t.Fatalf("cycles/notes: %+v", out)
	}
	// there is deliberately no grand total field
	if _, ok := out.Cycles["total"]; ok {
		t.Fatal("grand total must not exist")
	}
}

func TestNoSilentCurrencyMixing(t *testing.T) {
	l := line("Servicio en USD", "1", "10", "one_time", "gravado")
	l.Currency = "USD"
	_, err := Calculate(CalcIn{Currency: "PEN", IGVRate: "0.18", Lines: []LineIn{l}})
	if err == nil || !strings.Contains(err.Error(), "tipo de cambio") {
		t.Fatalf("mixing currencies must fail: %v", err)
	}
	l.Currency = "PEN"
	calc(t, CalcIn{Currency: "PEN", IGVRate: "0.18", Lines: []LineIn{l}})
	if _, err := Calculate(CalcIn{Currency: "EUR", IGVRate: "0.18"}); err == nil {
		t.Fatal("EUR accepted")
	}
}

func TestRoundingConsistency(t *testing.T) {
	// many small lines: totals are sums of rounded lines (what is printed adds up)
	var lines []LineIn
	for i := 0; i < 7; i++ {
		lines = append(lines, line("x", "1", "0.335", "one_time", "gravado"))
	}
	c := calc(t, CalcIn{Currency: "PEN", IGVRate: "0.18", Lines: lines}).Cycles["one_time"]
	// each line: base 0.34 (half-up), igv round(0.0612)=0.06 → 7×0.34=2.38, 7×0.06=0.42
	if c.Gravada != "2.38" || c.IGV != "0.42" || c.Total != "2.80" {
		t.Fatalf("%+v", c)
	}
	// fractional quantities
	c = calc(t, CalcIn{Currency: "PEN", IGVRate: "0.18", Lines: []LineIn{line("Horas", "2.5", "80", "one_time", "gravado")}}).Cycles["one_time"]
	if c.Gravada != "200.00" || c.Total != "236.00" {
		t.Fatalf("%+v", c)
	}
	// configurable IGV (e.g. 10% special regime) and 0%
	c = calc(t, CalcIn{Currency: "PEN", IGVRate: "0.10", Lines: []LineIn{line("x", "1", "100", "one_time", "gravado")}}).Cycles["one_time"]
	if c.IGV != "10.00" {
		t.Fatalf("%+v", c)
	}
}

func TestValidation(t *testing.T) {
	bad := []CalcIn{
		{Currency: "PEN", IGVRate: "1.5"},
		{Currency: "PEN", IGVRate: "-0.1"},
		{Currency: "PEN", IGVRate: "0.18", GlobalDiscountPct: "101"},
		{Currency: "PEN", IGVRate: "0.18", Lines: []LineIn{line("", "1", "1", "one_time", "gravado")}},
		{Currency: "PEN", IGVRate: "0.18", Lines: []LineIn{line("x", "0", "1", "one_time", "gravado")}},
		{Currency: "PEN", IGVRate: "0.18", Lines: []LineIn{line("x", "1", "-1", "one_time", "gravado")}},
		{Currency: "PEN", IGVRate: "0.18", Lines: []LineIn{line("x", "1", "", "one_time", "gravado")}},
		{Currency: "PEN", IGVRate: "0.18", Lines: []LineIn{line("x", "1", "1", "weekly", "gravado")}},
		{Currency: "PEN", IGVRate: "0.18", Lines: []LineIn{line("x", "1", "1", "one_time", "exento")}},
		{Currency: "PEN", IGVRate: "0.18", Lines: []LineIn{line("x", "abc", "1", "one_time", "gravado")}},
	}
	for i, in := range bad {
		if _, err := Calculate(in); err == nil {
			t.Errorf("case %d accepted: %+v", i, in)
		}
	}
}
