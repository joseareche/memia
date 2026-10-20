package pending

import "testing"

func TestFingerprintStable(t *testing.T) {
	a := Fingerprint("p", "Configurar el backup de Postgres en el servidor db")
	b := Fingerprint("p", "configurar backup postgres, en el servidor db!")
	if a != b {
		t.Fatal("fingerprint should ignore case/punct/stopwords")
	}
	if a == Fingerprint("q", "Configurar el backup de Postgres en el servidor db") {
		t.Fatal("project must be part of fingerprint")
	}
	if Jaccard(Words("Revisar logs de Asterisk AMI"), Words("revisar los logs del AMI de asterisk")) < 0.7 {
		t.Fatal("near duplicates should match")
	}
}

func TestIntent(t *testing.T) {
	for _, q := range []string{"¿qué pendientes tengo?", "Pendientes del proyecto pbx", "qué me falta por hacer", "lista los TODO", "tareas abiertas"} {
		if !IsPendingQuestion(q) {
			t.Errorf("should match: %q", q)
		}
	}
	for _, q := range []string{"explícame todo sobre nginx", "cómo configuro el trunk SIP", "todo el código"} {
		if IsPendingQuestion(q) {
			t.Errorf("should not match: %q", q)
		}
	}
}

func TestParseLLM(t *testing.T) {
	o, d := ParseLLM("```json\n{\"open\":[\"Agregar tests de integración al portal\",{\"text\":\"Rotar el token del dialer\"},\"x\"],\"done\":[\"Migrar nginx a Caddy\"]}\n```")
	if len(o) != 2 || len(d) != 1 {
		t.Fatalf("got %v %v", o, d)
	}
	if o, d := ParseLLM("sin json"); o != nil || d != nil {
		t.Fatal("expected nil")
	}
}
