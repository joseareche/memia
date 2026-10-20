package redact

import (
	"strings"
	"testing"
)

func TestRedactsSecrets(t *testing.T) {
	secrets := map[string]string{
		"openai":  "my key is sk-proj-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789",
		"aws":     "AKIAIOSFODNN7EXAMPLE",
		"github":  "ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789",
		"jwt":     "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		"bearer":  "Authorization: Bearer abcdefghijklmnop1234",
		"pem":     "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAA\n-----END OPENSSH PRIVATE KEY-----",
		"pw":      "password=hunter2xyz",
		"json":    `{"api_key": "s3cr3tvalue"}`,
		"sip":     "register sip:1001:SipPa55w0rd@pbx.example.com",
		"sipconf": "[1001]\nsecret=SipPa55w0rd\nmd5secret=abcdef0123",
		"url":     "postgres://admin:DbPa55w0rd@db.local/x",
		"dbk":     "token dbk_0123456789abcdef0123456789abcdef0123456789abcdef",
		"email":   "write to someone@example.com",
		"phone":   "call +51 987 654 321 or 987-654-321",
	}
	leaks := []string{"sk-proj-AbCd", "AKIAIOSFODNN7", "ghp_aBc", "eyJhbGci", "abcdefghijklmnop1234", "b3BlbnNzaC1rZXk", "hunter2xyz", "s3cr3tvalue", "SipPa55w0rd", "abcdef0123", "DbPa55w0rd", "dbk_0123", "someone@", "987 654 321", "987-654-321"}
	for name, in := range secrets {
		out, st := Redact(in)
		if st.Total() == 0 {
			t.Errorf("%s: nothing redacted: %q", name, out)
		}
		for _, l := range leaks {
			if strings.Contains(out, l) {
				t.Errorf("%s: leaked %q in %q", name, l, out)
			}
		}
	}
}

func TestKeepsHarmlessText(t *testing.T) {
	in := "Deploy on 2026-10-16 to 192.0.2.70 port 443; func main() { x := a + b } version 1.24.4"
	out, st := Redact(in)
	if out != in || st.Total() != 0 {
		t.Fatalf("changed harmless text: %q %v", out, st)
	}
}
