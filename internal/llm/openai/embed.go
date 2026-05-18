package openai

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
defaults to text-embedding-3-small (1536 dims). Dimensions may be set to
request a smaller representation (the API supports dimension truncation for
the 3-* family); when zero, the model's native dimension is used.
*/
type EmbedConfig struct {
	Model      string
	Dimensions int
}

/* Embedder is the OpenAI implementation of llm.Embedder built atop Client. */
type Embedder struct {
	client *Client
	model  string
	dim    int
}

/*
NewEmbedder constructs an Embedder using the same authenticated Client used
for chat completions. The dimension is fixed at construction time; mismatched
input dimensions are caught at search time by collection metadata.
*/
func NewEmbedder(c *Client, cfg EmbedConfig) (*Embedder, error) {
	if c == nil {
		return nil, errors.New("openai: nil client")
	}
	model := cfg.Model
	if model == "" {
		model = "text-embedding-3-small"
	}
	dim := cfg.Dimensions
	if dim == 0 {
		dim = defaultEmbedDim(model)
	}
	return &Embedder{client: c, model: model, dim: dim}, nil
}

/* Identity returns "openai:<model>". */
func (e *Embedder) Identity() string { return "openai:" + e.model }

/* Dim returns the embedding vector dimension. */
func (e *Embedder) Dim() int { return e.dim }

type embedRequest struct {
	Input          []string `json:"input"`
	Model          string   `json:"model"`
	Dimensions     int      `json:"dimensions,omitempty"`
	EncodingFormat string   `json:"encoding_format"`
}

type embedDatum struct {
	Embedding []float32 `json:"embedding"`
}

type embedResponse struct {
	Data []embedDatum `json:"data"`
}

/*
EmbedTexts batches inputs into a single OpenAI /v1/embeddings call and
returns one row per input. Rows preserve input order.
*/
func (e *Embedder) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body := embedRequest{
		Input:          texts,
		Model:          e.model,
		EncodingFormat: "float",
	}
	if e.dim > 0 && e.dim != defaultEmbedDim(e.model) {
		body.Dimensions = e.dim
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("openai: embed marshal: %w", err)
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, e.client.baseURL+"/embeddings", bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("openai: embed new request: %w", err)
	}
	r.Header.Set("Authorization", "Bearer "+e.client.apiKey)
	r.Header.Set("Content-Type", "application/json")

	resp, err := e.client.http.Do(r)
	if err != nil {
		return nil, fmt.Errorf("openai: embed http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai: embed http %d: %s", resp.StatusCode, string(raw))
	}
	var er embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, fmt.Errorf("openai: embed decode: %w", err)
	}
	if len(er.Data) != len(texts) {
		return nil, fmt.Errorf("openai: embed returned %d rows for %d inputs", len(er.Data), len(texts))
	}
	out := make([][]float32, len(er.Data))
	for i, d := range er.Data {
		if len(d.Embedding) != e.dim {
			return nil, fmt.Errorf("openai: embed row %d has dim %d, expected %d", i, len(d.Embedding), e.dim)
		}
		out[i] = d.Embedding
	}
	return out, nil
}

/* defaultEmbedDim returns the canonical native dimension for known models. */
func defaultEmbedDim(model string) int {
	switch model {
	case "text-embedding-3-small":
		return 1536
	case "text-embedding-3-large":
		return 3072
	case "text-embedding-ada-002":
		return 1536
	default:
		return 1536
	}
}
