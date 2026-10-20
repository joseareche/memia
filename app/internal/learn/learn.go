// Package learn detects request → correction(s) → final-solution sequences in conversations and turns them into
// reusable recipes (problem, solution, code, versions, files, commits). Pure heuristics: no LLM, no network.
package learn

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
)

type Msg struct{ Role, Text string }

type Recipe struct {
	Title, Problem, Solution, Code string
	Versions                       map[string]string
	Files, Commits                 []string
	Corrections                    int
	StartMsg, EndMsg               int
}

var (
	reCorrection = regexp.MustCompile(`(?i)\b(no funciona|no sirve|sigue (sin|fallando|dando|saliendo|igual|apareciendo)|sigue el error|todav[ií]a (falla|no)|a[uú]n (falla|no funciona|sale)|otra vez|mismo error|no (carga|anda|arranca|conecta|muestra|aparece|cambi[oó])|falla|error:|exception|traceback|fatal|not working|doesn'?t work|still (fails|failing|not|broken|getting)|same error|broke|wrong|incorrecto|est[aá] mal|no es (as[ií]|eso)|sale (un|el) error|da error)\b`)
	reSuccess    = regexp.MustCompile(`(?i)(\b(ya )?funcion(a|ó)\b|\bya (sale|carga|anda|qued[oó]|est[aá])\b|\bperfecto\b|\bexcelente\b|\bgenial\b|\blisto\b|\bresuelto\b|\bsolucionado\b|\bworks( now)?\b|\bfixed\b|\bthat did it\b|\bgracias,? (ya|funcion))`)
	reNegSuccess = regexp.MustCompile(`(?i)\bno (funciona|sale|carga|anda)\b|\bnot work`)
	reCode       = regexp.MustCompile("(?s)```[a-zA-Z0-9_+-]*\n.*?```")
	reVersion    = regexp.MustCompile(`(?i)\b(php|laravel|node(?:\.js)?|golang|go|python|asterisk|mysql|mariadb|postgres(?:ql)?|debian|ubuntu|nginx|apache|vue|react|next\.js|redis|docker|vicidial|freepbx|issabel|centos|rocky|almalinux|wordpress|composer|npm)[ /]?v?(\d+(?:\.\d+){0,2})\b`)
	reFile       = regexp.MustCompile(`(?:[\w.@-]+/)+[\w.@-]+\.(?:php|js|ts|tsx|jsx|go|py|vue|conf|cnf|sh|sql|json|ya?ml|env|ini|blade\.php|service|lua|rb|java|css|scss|html)\b`)
	reCommit     = regexp.MustCompile(`(?i)\bcommit\s+([0-9a-f]{7,40})\b`)
	reWord       = regexp.MustCompile(`[\pL\pN_]{3,}`)
)

func clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

func isCorrection(s string) bool {
	return reCorrection.MatchString(s) && !(reSuccess.MatchString(s) && !reNegSuccess.MatchString(s) && len(s) < 200)
}

func isSuccess(s string) bool {
	if reNegSuccess.MatchString(s) {
		return false
	}
	return reSuccess.MatchString(s) && len([]rune(s)) < 600
}

// Detect scans messages for request → ≥1 correction → user confirmation.
func Detect(msgs []Msg) []Recipe {
	var out []Recipe
	start, corr, lastCorr := -1, 0, -1
	for i, m := range msgs {
		if m.Role != "user" {
			continue
		}
		switch {
		case start >= 0 && isSuccess(m.Text) && corr > 0:
			if r, ok := build(msgs, start, lastCorr, i, corr); ok {
				out = append(out, r)
			}
			start, corr, lastCorr = -1, 0, -1
		case start >= 0 && isCorrection(m.Text):
			corr++
			lastCorr = i
		default:
			if corr == 0 || start < 0 { // new request (corrections chain broken only if none started)
				start, corr, lastCorr = i, 0, -1
			}
		}
	}
	return out
}

func build(msgs []Msg, start, lastCorr, end, corr int) (Recipe, bool) {
	var sol string
	var code []string
	for j := end - 1; j > lastCorr; j-- {
		if msgs[j].Role == "assistant" && strings.TrimSpace(msgs[j].Text) != "" {
			if sol == "" {
				sol = msgs[j].Text
			}
			for _, c := range reCode.FindAllString(msgs[j].Text, -1) {
				if len(code) < 4 {
					code = append([]string{c}, code...)
				}
			}
		}
	}
	if sol == "" {
		return Recipe{}, false
	}
	var prob strings.Builder
	prob.WriteString(clip(msgs[start].Text, 700))
	for j := start + 1; j <= lastCorr; j++ {
		if msgs[j].Role == "user" && isCorrection(msgs[j].Text) {
			prob.WriteString("\n- Corrección: " + clip(msgs[j].Text, 240))
		}
	}
	all := ""
	for j := start; j <= end; j++ {
		all += msgs[j].Text + "\n"
	}
	title := strings.SplitN(strings.TrimSpace(msgs[start].Text), "\n", 2)[0]
	r := Recipe{Title: clip(title, 110), Problem: prob.String(), Solution: clip(sol, 3500), Code: clip(strings.Join(code, "\n\n"), 6000),
		Versions: versions(all), Files: uniq(reFile.FindAllString(all, -1), 15), Corrections: corr, StartMsg: start, EndMsg: end}
	for _, m := range reCommit.FindAllStringSubmatch(all, -1) {
		r.Commits = append(r.Commits, m[1])
	}
	r.Commits = uniq(r.Commits, 10)
	return r, true
}

func versions(s string) map[string]string {
	v := map[string]string{}
	for _, m := range reVersion.FindAllStringSubmatch(s, -1) {
		k := strings.ToLower(m[1])
		if k == "go" && !strings.Contains(m[2], ".") {
			continue // "go 2" etc. too ambiguous
		}
		if _, ok := v[k]; !ok {
			v[k] = m[2]
		}
	}
	return v
}

func uniq(in []string, n int) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] && len(out) < n {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// Words returns the normalized word set used for near-duplicate detection.
func Words(s string) map[string]bool {
	w := map[string]bool{}
	for _, x := range reWord.FindAllString(strings.ToLower(s), -1) {
		w[x] = true
	}
	return w
}

func Jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	n := 0
	for k := range a {
		if b[k] {
			n++
		}
	}
	return float64(n) / float64(len(a)+len(b)-n)
}

// Fingerprint: project + sorted title words + normalized code (exact-dup key).
func Fingerprint(project string, r Recipe) string {
	ws := []string{}
	for k := range Words(r.Title) {
		ws = append(ws, k)
	}
	sort.Strings(ws)
	code := strings.Join(strings.Fields(r.Code), " ")
	h := sha256.Sum256([]byte(project + "|" + strings.Join(ws, " ") + "|" + code))
	return hex.EncodeToString(h[:16])
}

// ParseConversation splits the stored Markdown ("### Usuario"/"### Asistente" headers) into messages.
func ParseConversation(s string) []Msg {
	var out []Msg
	parts := regexp.MustCompile(`(?m)^### (Usuario|Asistente)\n`).Split(s, -1)
	heads := regexp.MustCompile(`(?m)^### (Usuario|Asistente)\n`).FindAllStringSubmatch(s, -1)
	for i, h := range heads {
		role := "assistant"
		if h[1] == "Usuario" {
			role = "user"
		}
		out = append(out, Msg{role, strings.TrimSpace(parts[i+1])})
	}
	return out
}
