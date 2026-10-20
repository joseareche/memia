// Server-side conversation history (per account). Stored as a single JSON file in the
// service's systemd StateDirectory (/var/lib/devbrain-chat, owned by the dynamic user,
// 0600), written atomically. History is per verified email; the owner's history keeps the legacy key a.user
// (shared by Google login and the password fallback).
package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxConvs     = 500
	maxMsgs      = 1000
	maxMsgBytes  = 128 << 10
	maxTitleRune = 80
)

type convMsg struct {
	At   time.Time       `json:"at"`
	Data json.RawMessage `json:"data"` // UI message object {role,text,kind,hits,sources,by,reason,meta,error}
}

type conv struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Created  time.Time `json:"created_at"`
	Updated  time.Time `json:"updated_at"`
	Messages []convMsg `json:"messages"`
}

type histStore struct {
	mu   sync.Mutex
	path string
	data map[string][]*conv // owner -> conversations
}

func openHistory(dir string) (*histStore, error) {
	h := &histStore{path: filepath.Join(dir, "conversations.json"), data: map[string][]*conv{}}
	b, err := os.ReadFile(h.path)
	if errors.Is(err, os.ErrNotExist) {
		return h, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &h.data); err != nil {
			return nil, err
		}
	}
	return h, nil
}

func (h *histStore) flush() error { // caller holds mu
	b, err := json.Marshal(h.data)
	if err != nil {
		return err
	}
	tmp := h.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, h.path)
}

func (h *histStore) find(owner, id string) *conv {
	for _, c := range h.data[owner] {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func cleanTitle(s string) string {
	s = strings.Join(strings.Fields(strings.TrimLeft(s, "📝🔎 ")), " ")
	if utf8.RuneCountInString(s) > maxTitleRune {
		s = string([]rune(s)[:maxTitleRune-1]) + "…"
	}
	if s == "" {
		s = "Nueva conversación"
	}
	return s
}

func jsonOut(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// guard: session + (for writes) exact Origin and JSON content type.
func (a *app) guard(w http.ResponseWriter, r *http.Request, write bool) (string, bool) {
	if !a.authed(r) {
		jsonOut(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return "", false
	}
	if write && (!a.sameOrigin(r) || (r.Method != http.MethodDelete && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json"))) {
		jsonOut(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return "", false
	}
	if a.hist == nil {
		jsonOut(w, http.StatusServiceUnavailable, map[string]string{"error": "El historial no está disponible."})
		return "", false
	}
	if !a.reqEnt(r).has(modChat) {
		jsonOut(w, http.StatusForbidden, map[string]string{"error": "El módulo Chat no está habilitado para tu usuario.", "code": "module_disabled"})
		return "", false
	}
	return a.histOwner(a.sessionEmail(r)), true
}

type convSummary struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Created time.Time `json:"created_at"`
	Updated time.Time `json:"updated_at"`
	Count   int       `json:"messages"`
}

func (a *app) convList(w http.ResponseWriter, r *http.Request) {
	owner, ok := a.guard(w, r, false)
	if !ok {
		return
	}
	h := a.hist
	h.mu.Lock()
	out := make([]convSummary, 0, len(h.data[owner]))
	for _, c := range h.data[owner] {
		out = append(out, convSummary{c.ID, c.Title, c.Created, c.Updated, len(c.Messages)})
	}
	h.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	jsonOut(w, http.StatusOK, map[string]any{"conversations": out})
}

func (a *app) convCreate(w http.ResponseWriter, r *http.Request) {
	owner, ok := a.guard(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Title string `json:"title"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in)
	now := time.Now().UTC()
	c := &conv{ID: randHex(12), Title: cleanTitle(in.Title), Created: now, Updated: now, Messages: []convMsg{}}
	h := a.hist
	h.mu.Lock()
	defer h.mu.Unlock()
	list := h.data[owner]
	if len(list) >= maxConvs { // drop the least recently updated
		sort.Slice(list, func(i, j int) bool { return list[i].Updated.After(list[j].Updated) })
		list = list[:maxConvs-1]
	}
	h.data[owner] = append(list, c)
	if err := h.flush(); err != nil {
		a.log.Error("history write", "err", err)
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "No pude guardar la conversación."})
		return
	}
	jsonOut(w, http.StatusCreated, convSummary{c.ID, c.Title, c.Created, c.Updated, 0})
}

func (a *app) convGet(w http.ResponseWriter, r *http.Request) {
	owner, ok := a.guard(w, r, false)
	if !ok {
		return
	}
	h := a.hist
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.find(owner, r.PathValue("id"))
	if c == nil {
		jsonOut(w, http.StatusNotFound, map[string]string{"error": "Conversación no encontrada."})
		return
	}
	msgs := make([]json.RawMessage, 0, len(c.Messages))
	for _, m := range c.Messages {
		msgs = append(msgs, m.Data)
	}
	jsonOut(w, http.StatusOK, map[string]any{"id": c.ID, "title": c.Title, "created_at": c.Created, "updated_at": c.Updated, "messages": msgs})
}

func (a *app) convRename(w http.ResponseWriter, r *http.Request) {
	owner, ok := a.guard(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Title string `json:"title"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil || strings.TrimSpace(in.Title) == "" {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "Título no válido."})
		return
	}
	h := a.hist
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.find(owner, r.PathValue("id"))
	if c == nil {
		jsonOut(w, http.StatusNotFound, map[string]string{"error": "Conversación no encontrada."})
		return
	}
	c.Title = cleanTitle(in.Title)
	if err := h.flush(); err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "No pude guardar."})
		return
	}
	jsonOut(w, http.StatusOK, map[string]string{"id": c.ID, "title": c.Title})
}

func (a *app) convDelete(w http.ResponseWriter, r *http.Request) {
	owner, ok := a.guard(w, r, true)
	if !ok {
		return
	}
	h := a.hist
	h.mu.Lock()
	defer h.mu.Unlock()
	id, list := r.PathValue("id"), h.data[owner]
	for i, c := range list {
		if c.ID == id {
			h.data[owner] = append(list[:i:i], list[i+1:]...)
			if err := h.flush(); err != nil {
				jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "No pude borrar."})
				return
			}
			jsonOut(w, http.StatusOK, map[string]bool{"deleted": true})
			return
		}
	}
	jsonOut(w, http.StatusNotFound, map[string]string{"error": "Conversación no encontrada."})
}

// convAppend stores UI message objects (rendered client-side with textContent only).
func (a *app) convAppend(w http.ResponseWriter, r *http.Request) {
	owner, ok := a.guard(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAPIBody)).Decode(&in) != nil || len(in.Messages) == 0 || len(in.Messages) > 10 {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "Mensajes no válidos."})
		return
	}
	now := time.Now().UTC()
	add := make([]convMsg, 0, len(in.Messages))
	firstUser := ""
	for _, m := range in.Messages {
		var probe struct {
			Role string `json:"role"`
			Text string `json:"text"`
		}
		if len(m) > maxMsgBytes || json.Unmarshal(m, &probe) != nil || (probe.Role != "user" && probe.Role != "bot") {
			jsonOut(w, http.StatusBadRequest, map[string]string{"error": "Mensaje no válido."})
			return
		}
		if probe.Role == "user" && firstUser == "" {
			firstUser = probe.Text
		}
		add = append(add, convMsg{At: now, Data: m})
	}
	h := a.hist
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.find(owner, r.PathValue("id"))
	if c == nil {
		jsonOut(w, http.StatusNotFound, map[string]string{"error": "Conversación no encontrada."})
		return
	}
	if len(c.Messages) == 0 && c.Title == "Nueva conversación" && firstUser != "" {
		c.Title = cleanTitle(firstUser) // auto-title from the first question
	}
	c.Messages = append(c.Messages, add...)
	if len(c.Messages) > maxMsgs {
		c.Messages = c.Messages[len(c.Messages)-maxMsgs:]
	}
	c.Updated = now
	if err := h.flush(); err != nil {
		a.log.Error("history write", "err", err)
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "No pude guardar el mensaje."})
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"id": c.ID, "title": c.Title, "messages": len(c.Messages)})
}
