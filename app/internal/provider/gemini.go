package provider

import "context"

// Gemini embeddings adapter (batchEmbedContents). Implemented and unit-tested against
// a fake server, but NOT configured in production (no GEMINI_API_KEY).
// TODO(gemini): validate with a real key; chat not implemented.
type Gemini struct {
	key  string
	Base string
}

func NewGemini(key string) *Gemini {
	return &Gemini{key: key, Base: "https://generativelanguage.googleapis.com/v1beta"}
}

func (g *Gemini) Name() string   { return "gemini" }
func (g *Gemini) External() bool { return true }

func (g *Gemini) Embed(ctx context.Context, model string, dims int, texts []string) ([][]float32, error) {
	reqs := make([]map[string]any, len(texts))
	for i, t := range texts {
		r := map[string]any{"model": "models/" + model, "content": map[string]any{"parts": []map[string]string{{"text": t}}}}
		if dims > 0 {
			r["outputDimensionality"] = dims
		}
		reqs[i] = r
	}
	var out struct {
		Embeddings []struct {
			Values []float32 `json:"values"`
		} `json:"embeddings"`
	}
	if err := postJSON(ctx, g.Base+"/models/"+model+":batchEmbedContents",
		map[string]string{"x-goog-api-key": g.key}, map[string]any{"requests": reqs}, &out); err != nil {
		return nil, err
	}
	res := make([][]float32, len(out.Embeddings))
	for i, e := range out.Embeddings {
		res[i] = e.Values
	}
	return res, checkDims(res, len(texts), dims)
}
