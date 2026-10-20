package provider

import "context"

// Ollama local embeddings adapter (/api/embed). Code ready; Ollama is NOT installed
// and the adapter is only registered if DEVBRAIN_OLLAMA_URL is set. External()=false,
// so it is allowed for local_only tenants and receives unredacted text.
type Ollama struct{ Base string }

func NewOllama(base string) *Ollama { return &Ollama{Base: base} }

func (o *Ollama) Name() string   { return "ollama" }
func (o *Ollama) External() bool { return false }

func (o *Ollama) Embed(ctx context.Context, model string, dims int, texts []string) ([][]float32, error) {
	body := map[string]any{"model": model, "input": texts}
	if dims > 0 {
		body["dimensions"] = dims
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := postJSON(ctx, o.Base+"/api/embed", nil, body, &out); err != nil {
		return nil, err
	}
	return out.Embeddings, checkDims(out.Embeddings, len(texts), dims)
}
