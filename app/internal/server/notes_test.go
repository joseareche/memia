package server

import "testing"

func TestRecencyQuestion(t *testing.T) {
	cases := map[string]int{"Resume lo último que guardé": 1, "¿qué guardé hoy?": 1, "muéstrame mis últimas notas": 5, "las últimas 3 notas": 3,
		"lo ultimo que guarde": 1, "resume la última nota": 1, "¿cómo configuro nginx?": 0, "último error del dialer": 0, "qué pendientes tengo": 0}
	for q, want := range cases {
		if got := recencyQuestion(q); got != want {
			t.Errorf("%q: got %d want %d", q, got, want)
		}
	}
}

func TestFilterSQLKeepsNotes(t *testing.T) {
	s, args := filterSQL(queryIn{Project: "(sin proyecto)"}, 3)
	if len(args) != 1 || !contains(s, "d.source = 'note'") || !contains(s, "d.project_id IS NULL") {
		t.Fatalf("filter must keep notes and project-less docs: %s", s)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (len(sub) == 0 || indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
