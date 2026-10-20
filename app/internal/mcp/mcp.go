// Package mcp implements an MCP server over Streamable HTTP (protocol 2025-06-18, also accepts 2025-03-26).
// POST carries one JSON-RPC message; responses are application/json (no server-initiated SSE stream, so GET is 405).
// Sessions: initialize issues Mcp-Session-Id bound to the caller's tenant+user; later requests must send it
// (400 if missing, 404 if unknown/expired); DELETE ends the session. Tools are authorized per scope.
package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"devbrain/internal/tenant"
)

const ProtocolVersion = "2025-06-18"

var supported = map[string]bool{"2025-06-18": true, "2025-03-26": true}

// Tool is one MCP tool. Scopes: the caller needs ANY of them.
type Tool struct {
	Name        string
	Title       string
	Description string
	InputSchema map[string]any
	Scopes      []string
	ReadOnly    bool
	Call        func(ctx context.Context, args json.RawMessage) (text string, structured any, err error)
}

// ToolError is returned to the client as a tool result with isError=true (not a protocol error).
type ToolError struct{ Msg string }

func (e ToolError) Error() string { return e.Msg }

type session struct {
	tenant, user, proto string
	last                time.Time
}

type Handler struct {
	version string
	origins map[string]bool
	tools   []Tool
	mu      sync.Mutex
	sess    map[string]*session
	TTL     time.Duration
}

func New(version string, allowedOrigins []string) *Handler {
	m := map[string]bool{}
	for _, o := range allowedOrigins {
		m[o] = true
	}
	return &Handler{version: version, origins: m, sess: map[string]*session{}, TTL: 12 * time.Hour}
}

func (h *Handler) Register(t ...Tool) { h.tools = append(h.tools, t...) }

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func write(w http.ResponseWriter, status int, r response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(r)
}

func errResp(w http.ResponseWriter, status, code int, msg string) {
	write(w, status, response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{code, msg}})
}

func allowed(info tenant.Info, scopes []string) bool {
	for _, have := range info.Scopes {
		for _, need := range scopes {
			if have == need {
				return true
			}
		}
	}
	return false
}

func (h *Handler) session(id string, info tenant.Info) (*session, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	for k, s := range h.sess { // lazy expiry
		if now.Sub(s.last) > h.TTL {
			delete(h.sess, k)
		}
	}
	s, ok := h.sess[id]
	if !ok || s.tenant != info.TenantID || s.user != info.UserID {
		return nil, false
	}
	s.last = now
	return s, true
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// DNS-rebinding protection: browsers send Origin; only allow listed ones.
	if o := r.Header.Get("Origin"); o != "" && !h.origins[o] {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	info, _ := tenant.From(r.Context())
	if pv := r.Header.Get("MCP-Protocol-Version"); pv != "" && !supported[pv] {
		errResp(w, http.StatusBadRequest, -32600, "unsupported MCP-Protocol-Version")
		return
	}
	sid := r.Header.Get("Mcp-Session-Id")
	switch r.Method {
	case http.MethodDelete:
		if _, ok := h.session(sid, info); !ok {
			http.Error(w, "unknown session", http.StatusNotFound)
			return
		}
		h.mu.Lock()
		delete(h.sess, sid)
		h.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	case http.MethodGet: // no server-initiated stream offered
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	case http.MethodPost:
	default:
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		errResp(w, http.StatusBadRequest, -32700, "parse error")
		return
	}
	if req.JSONRPC != "2.0" || (req.Method == "" && len(req.ID) == 0) {
		errResp(w, http.StatusBadRequest, -32600, "invalid request")
		return
	}
	if req.Method != "initialize" {
		if sid == "" {
			errResp(w, http.StatusBadRequest, -32600, "missing Mcp-Session-Id")
			return
		}
		if _, ok := h.session(sid, info); !ok {
			errResp(w, http.StatusNotFound, -32001, "session not found")
			return
		}
	}
	if req.Method == "" || len(req.ID) == 0 { // notification or client response
		w.WriteHeader(http.StatusAccepted)
		return
	}
	resp := response{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		pv := ProtocolVersion
		if supported[p.ProtocolVersion] {
			pv = p.ProtocolVersion
		}
		b := make([]byte, 24)
		_, _ = rand.Read(b)
		id := hex.EncodeToString(b)
		h.mu.Lock()
		h.sess[id] = &session{tenant: info.TenantID, user: info.UserID, proto: pv, last: time.Now()}
		h.mu.Unlock()
		w.Header().Set("Mcp-Session-Id", id)
		resp.Result = map[string]any{
			"protocolVersion": pv,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]string{"name": "devbrain", "title": "DevBrain (memoria de Memia)", "version": h.version},
			"instructions": "Memoria técnica privada: historial de conversaciones (Cursor), conocimiento destilado por proyecto y recetas. " +
				"Usa find_solution antes de resolver un error ya visto; get_project_rules al empezar en un proyecto. Las recetas nuevas quedan 'unverified' salvo evidencia + aprobación.",
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		list := []map[string]any{}
		for _, t := range h.tools {
			if allowed(info, t.Scopes) {
				list = append(list, map[string]any{"name": t.Name, "title": t.Title, "description": t.Description, "inputSchema": t.InputSchema,
					"annotations": map[string]any{"readOnlyHint": t.ReadOnly, "openWorldHint": false}})
			}
		}
		resp.Result = map[string]any{"tools": list}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(req.Params, &p) != nil {
			resp.Error = &rpcError{-32602, "invalid params"}
			break
		}
		var tool *Tool
		for i := range h.tools {
			if h.tools[i].Name == p.Name {
				tool = &h.tools[i]
			}
		}
		if tool == nil {
			resp.Error = &rpcError{-32602, "unknown tool: " + p.Name}
			break
		}
		if !allowed(info, tool.Scopes) {
			resp.Result = map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "forbidden: token lacks scope for " + p.Name}}}
			break
		}
		if len(p.Arguments) == 0 {
			p.Arguments = json.RawMessage("{}")
		}
		text, sc, err := tool.Call(r.Context(), p.Arguments)
		if err != nil {
			msg := "internal error"
			if te, ok := err.(ToolError); ok {
				msg = te.Msg
			}
			resp.Result = map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": msg}}}
			break
		}
		res := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
		if sc != nil {
			res["structuredContent"] = sc
		}
		resp.Result = res
	default:
		resp.Error = &rpcError{-32601, "method not found"}
	}
	write(w, http.StatusOK, resp)
}
