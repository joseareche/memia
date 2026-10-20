package commercial

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func snapFixture(t *testing.T) Quote {
	items := []ItemOut{{LineIn: LineIn{Description: "Central <nube> & PBX", Unit: "mes", Qty: "1", UnitPrice: "35", BillingMode: "monthly", TaxRule: "gravado"}},
		{LineIn: LineIn{Description: "Instalación", Unit: "und", Qty: "1", UnitPrice: "100", BillingMode: "one_time", TaxRule: "gravado"}}}
	in := CalcIn{Currency: "PEN", IGVRate: "0.18"}
	for _, i := range items {
		in.Lines = append(in.Lines, i.LineIn)
	}
	out, err := Calculate(in)
	if err != nil {
		t.Fatal(err)
	}
	tb, _ := json.Marshal(out)
	n := "COT-2026-00001"
	cust, _ := json.Marshal(map[string]any{"display_name": "RUC 20100070970", "data": CustomerData{DisplayName: "RUC 20100070970", DocumentType: "RUC", TaxID: "20100070970"}})
	return Quote{Number: &n, Status: "approved", Currency: "PEN", IGVRate: "0.18", GlobalDiscountPct: "0", Version: 1, Totals: tb, Items: items, Customer: cust, Terms: "Línea 1\nLínea 2"}
}

func TestBuildDOCX(t *testing.T) {
	s := snapFixture(t)
	if err := VerifySnapshot(s); err != nil {
		t.Fatal(err)
	}
	b, err := BuildDOCX(company{LegalName: "EMPRESA EMISORA (configurar)"}, s, time.Date(2026, 10, 19, 20, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	var doc string
	for _, f := range z.File {
		if f.Name == "word/document.xml" {
			rc, _ := f.Open()
			x, _ := io.ReadAll(rc)
			doc = string(x)
		}
	}
	for _, want := range []string{"COTIZACIÓN COT-2026-00001", "Central &lt;nube&gt; &amp; PBX", "Total mensual: S/ 41.30", "Total único: S/ 118.00", "RUC: (configurar)", "Razón social pendiente", TemplateVersion} {
		if !strings.Contains(doc, want) {
			t.Errorf("docx missing %q", want)
		}
	}
	b2, _ := BuildDOCX(company{LegalName: "EMPRESA EMISORA (configurar)"}, s, time.Date(2026, 10, 19, 20, 0, 0, 0, time.UTC))
	if !bytes.Equal(b, b2) {
		t.Error("DOCX not deterministic")
	}
}

func TestVerifySnapshotTamper(t *testing.T) {
	s := snapFixture(t)
	s.Items[0].UnitPrice = "36"
	if VerifySnapshot(s) == nil {
		t.Fatal("tampered snapshot must fail verification")
	}
}

func TestMatchServicesAndIntent(t *testing.T) {
	svcs := []Service{{ID: "1", Code: "PBX-EMPRENDEDOR", Name: "Central telefónica en la nube – Plan Emprendedor", Aliases: []string{"plan emprendedor"}},
		{ID: "2", Code: "PBX-PYMES", Name: "Central telefónica en la nube – Plan Pymes", Aliases: []string{"plan pymes"}}}
	txt := "genérame una cotización del RUC 20100070970 de una central telefónica en la nube plan Emprendedor, envíala a admin@example.com"
	m := MatchServices(txt, svcs)
	if len(m) == 0 || m[0].Service.ID != "1" || m[0].Confidence < 0.9 {
		t.Fatalf("match %+v", m)
	}
	if !DetectQuoteIntent(txt) || DetectQuoteIntent("¿qué notas tengo de la PBX?") {
		t.Fatal("intent detection")
	}
	if rucRe.FindStringSubmatch(txt)[1] != "20100070970" || mailRe.FindString(txt) != "admin@example.com" || !sendRe.MatchString(txt) {
		t.Fatal("entity extraction")
	}
	if len(MatchServices("cotiza hosting web", svcs)) != 0 {
		t.Fatal("must not match unrelated service")
	}
}
