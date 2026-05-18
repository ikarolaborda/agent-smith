package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

/*
EmbedConfig customizes an Embedder built on top of an existing Client. Model
defaults to nomic-embed-text (768 dims). The actual dimension is detected
from the first embedding response.
*/
type EmbedConfig struct {
	Model string
}

/* Embedder is the Ollama implementation of llm.Embedder. */
type Embedder struct {
	client *Client
	model  string
	dim    int
}

/*
NewEmbedder constructs an Embedder. The first call to EmbedTexts probes the
dimension; explicit Dim() is zero until the probe lands.
*/
func NewEmbedder(c *Client, cfg EmbedConfig) (*Embedder, error) {
	if c == nil {
		return nil, errors.New("ollama: nil client")
	}
	model := cfg.Model
	if model == "" {
		model = "nomic-embed-text"
	}
	return &Embedder{client: c, model: model}, nil
}

/* Identity returns "ollama:<model>". */
func (e *Embedder) Identity() string { return "ollama:" + e.model }

/* Dim returns the embedding vector dimension; zero until the first call. */
func (e *Embedder) Dim() int { return e.dim }

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

/*
EmbedTexts calls Ollama's /api/embed (batch endpoint) and returns one row per
input, preserving order. The dimension is locked on the first call.
*/
func (e *Embedder) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body := embedRequest{Model: e.model, Input: texts}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ollama: embed marshal: %w", err)
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, e.client.baseURL+"/api/embed", bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("ollama: embed new request: %w", err)
	}
	r.Header.Set("Content-Type", "application/json")

	resp, err := e.client.http.Do(r)
	if err != nil {
		return nil, fmt.Errorf("ollama: embed http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama: embed http %d: %s", resp.StatusCode, string(raw))
	}
	var er embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, fmt.Errorf("ollama: embed decode: %w", err)
	}
	if len(er.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama: embed returned %d rows for %d inputs", len(er.Embeddings), len(texts))
	}
	if e.dim == 0 && len(er.Embeddings) > 0 {
		e.dim = len(er.Embeddings[0])
	}
	for i, row := range er.Embeddings {
		if len(row) != e.dim {
			return nil, fmt.Errorf("ollama: embed row %d has dim %d, expected %d", i, len(row), e.dim)
		}
	}
	return er.Embeddings, nil
}
