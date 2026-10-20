// Package redact removes secrets and personal data from text BEFORE it is sent
// to any external provider. It is deliberately conservative (prefers over-redaction).
// TODO(redact): credit cards (Luhn), national IDs, internal hostnames/IPs if required.
package redact

import "regexp"

type Stats map[string]int

func (s Stats) Total() int {
	n := 0
	for _, v := range s {
		n += v
	}
	return n
}

func (s Stats) Add(o Stats) {
	for k, v := range o {
		s[k] += v
	}
}

type rule struct {
	name string
	re   *regexp.Regexp
	repl string
}

// Order matters: specific credential formats first, generic patterns last.
var rules = []rule{
	{"private_key", regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----|\z)`), "[REDACTED:PRIVATE_KEY]"},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), "[REDACTED:JWT]"},
	{"api_key", regexp.MustCompile(`\bsk-(?:proj-|svcacct-|admin-|ant-)?[A-Za-z0-9_-]{20,}`), "[REDACTED:API_KEY]"},
	{"api_key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), "[REDACTED:API_KEY]"},
	{"api_key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`), "[REDACTED:API_KEY]"},
	{"token", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,})`), "[REDACTED:TOKEN]"},
	{"token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), "[REDACTED:TOKEN]"},
	{"token", regexp.MustCompile(`\bdbk_[0-9a-f]{16,}`), "[REDACTED:TOKEN]"},
	{"token", regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/-]{12,}=*`), "$1 [REDACTED:TOKEN]"},
	{"sip_credentials", regexp.MustCompile(`(?i)\b(sips?:[^\s:@]+):[^\s@]+@`), "$1:[REDACTED]@"},
	{"url_credentials", regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://[^\s:/@]+):[^\s@/]+@`), "$1:[REDACTED]@"},
	{"secret_assignment", regexp.MustCompile(`(?i)\b([a-z0-9_.-]*(?:password|passwd|pwd|secret|api[_-]?key|apikey|token|auth[_-]?key|private[_-]?key|access[_-]?key|client[_-]?secret))(["']?\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;]+)`), "$1$2[REDACTED]"},
	{"email", regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`), "[EMAIL]"},
	{"phone", regexp.MustCompile(`\+\d{1,3}[\s-]?\(?\d{1,4}\)?(?:[\s-]?\d{2,4}){2,4}\b|\b\d{3}[\s-]\d{3}[\s-]\d{3,4}\b`), "[PHONE]"},
}

// Redact returns the sanitized text and how many items of each kind were removed.
func Redact(s string) (string, Stats) {
	st := Stats{}
	for _, r := range rules {
		if n := len(r.re.FindAllStringIndex(s, -1)); n > 0 {
			st[r.name] += n
			s = r.re.ReplaceAllString(s, r.repl)
		}
	}
	return s, st
}
