package commercial

import "testing"

func TestTaxIDValidation(t *testing.T) {
	good := map[string][2]string{"RUC 20100070970": {"RUC", "20100070970"}, "RUC spaced": {"RUC", "20-10007097-0"}, "DNI": {"DNI", "12345678"}, "CE": {"CE", "001234567"}}
	for name, c := range good {
		if _, ok := normTaxID(c[0], c[1]); !ok {
			t.Errorf("%s rejected", name)
		}
	}
	bad := [][2]string{{"RUC", "20100070971"}, {"RUC", "12345678901"}, {"RUC", "2010007097"}, {"DNI", "1234567"}, {"DNI", "1234567A"}, {"XYZ", "1"}, {"", "123"}}
	for _, c := range bad {
		if _, ok := normTaxID(c[0], c[1]); ok {
			t.Errorf("%v accepted", c)
		}
	}
	d := CustomerData{LegalName: "  ACME SAC ", DocumentType: "ruc", TaxID: "20100070970", Email: "x@y.pe"}
	if n, msg := d.Validate(); msg != "" || n == nil || *n != "20100070970" || d.DisplayName != "ACME SAC" || d.DocumentType != "RUC" {
		t.Fatalf("validate: %v %q %+v", n, msg, d)
	}
	if _, msg := (&CustomerData{DisplayName: "x", TaxID: "123"}).Validate(); msg == "" {
		t.Fatal("tax id without type accepted")
	}
	if _, msg := (&CustomerData{}).Validate(); msg == "" {
		t.Fatal("empty name accepted")
	}
	if FormatNumber("{CODE}-{YYYY}-{N5}", "COT", 2026, 42) != "COT-2026-00042" {
		t.Fatal("number format")
	}
}
