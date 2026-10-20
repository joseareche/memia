package learn

import "testing"

func TestDetectSequence(t *testing.T) {
	conv := "### Usuario\nEl login de Laravel 10 da 419 al enviar el formulario en app/Http/Kernel.php\n\n" +
		"### Asistente\nPrueba limpiar caché.\n\n" +
		"### Usuario\nSigue dando el mismo error 419\n\n" +
		"### Asistente\nEl problema es SESSION_DOMAIN. Ajusta config/session.php:\n```php\n'domain' => env('SESSION_DOMAIN', null),\n```\nY ejecuta commit abc1234f.\n\n" +
		"### Usuario\nPerfecto, ya funciona\n\n" +
		"### Usuario\nAhora otra cosa: agrega un botón\n\n### Asistente\nHecho.\n\n### Usuario\ngracias\n"
	ms := ParseConversation(conv)
	if len(ms) != 8 {
		t.Fatalf("parse: %d", len(ms))
	}
	rs := Detect(ms)
	if len(rs) != 1 {
		t.Fatalf("want 1 recipe, got %d", len(rs))
	}
	r := rs[0]
	if r.Corrections != 1 || r.Versions["laravel"] != "10" || r.Code == "" || len(r.Commits) != 1 || r.Commits[0] != "abc1234f" {
		t.Fatalf("bad recipe: %+v", r)
	}
	if len(r.Files) < 2 {
		t.Fatalf("files: %v", r.Files)
	}
}

func TestNoCorrectionNoRecipe(t *testing.T) {
	ms := []Msg{{"user", "haz X"}, {"assistant", "hecho"}, {"user", "perfecto, funciona"}}
	if n := len(Detect(ms)); n != 0 {
		t.Fatalf("got %d", n)
	}
}

func TestNegatedSuccessIsCorrection(t *testing.T) {
	ms := []Msg{{"user", "configura nginx"}, {"assistant", "a"}, {"user", "no funciona, sale error 502"}, {"assistant", "```nginx\nproxy_pass http://127.0.0.1:8080;\n```"}, {"user", "listo, ya funciona"}}
	rs := Detect(ms)
	if len(rs) != 1 || rs[0].Corrections != 1 {
		t.Fatalf("%+v", rs)
	}
}

func TestDedupe(t *testing.T) {
	a := Recipe{Title: "Error 419 login Laravel", Code: "x  y"}
	b := Recipe{Title: "login Laravel error 419", Code: "x y"}
	if Fingerprint("p", a) != Fingerprint("p", b) {
		t.Fatal("same recipe should share fingerprint")
	}
	if Jaccard(Words("error 419 en login laravel"), Words("login laravel error 419")) < 0.8 {
		t.Fatal("near-dup")
	}
}
