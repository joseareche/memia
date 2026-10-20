package provider

import (
	"context"
	"errors"
	"fmt"
)

// OpenAI implements Embedder and Chatter. Implemented and in use.
type OpenAI struct {
	key  string
	Base string
}

func NewOpenAI(key string) *OpenAI { return &OpenAI{key: key, Base: "https://api.openai.com/v1"} }

func (o *OpenAI) Name() string   { return "openai" }
func (o *OpenAI) External() bool { return true }
func (o *OpenAI) hdr() map[string]string {
	return map[string]string{"Authorization": "Bearer " + o.key}
}

func (o *OpenAI) Embed(ctx context.Context, model string, dims int, texts []string) ([][]float32, error) {
	body := map[string]any{"model": model, "input": texts, "encoding_format": "float"}
	if dims > 0 {
		body["dimensions"] = dims
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := postJSON(ctx, o.Base+"/embeddings", o.hdr(), body, &out); err != nil {
		return nil, err
	}
	meterFrom(ctx).add(out.Usage.PromptTokens, 0)
	res := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(res) {
			return nil, fmt.Errorf("openai: bad index %d", d.Index)
		}
		res[d.Index] = d.Embedding
	}
	return res, checkDims(res, len(texts), dims)
}

func (o *OpenAI) Chat(ctx context.Context, model, system, user string) (string, error) {
	body := map[string]any{"model": model, "messages": []map[string]string{
		{"role": "system", "content": system}, {"role": "user", "content": user}}}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := postJSON(ctx, o.Base+"/chat/completions", o.hdr(), body, &out); err != nil {
		return "", err
	}
	meterFrom(ctx).add(out.Usage.PromptTokens, out.Usage.CompletionTokens)
	if len(out.Choices) == 0 {
		return "", errors.New("openai: empty response")
	}
	return out.Choices[0].Message.Content, nil
}
