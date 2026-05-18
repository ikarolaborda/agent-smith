package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

/*
ModelInfo describes one model returned by Ollama's /api/tags. Family and
ParameterSize are reported when the local installation has the metadata; on
older Ollama versions both may be empty strings.
*/
type ModelInfo struct {
	Name          string `json:"name"`
	Family        string `json:"family"`
	ParameterSize string `json:"parameter_size"`
	Embedding     bool   `json:"embedding"`
}

type tagsResponse struct {
	Models []struct {
		Name    string `json:"name"`
		Details struct {
			Family        string `json:"family"`
			ParameterSize string `json:"parameter_size"`
		} `json:"details"`
	} `json:"models"`
}

/*
ListModels queries /api/tags and returns one entry per installed model. The
Embedding flag is a best-effort label based on family-name heuristics:
families containing "bert" or known embedding identifiers (e.g.
"nomic-bert", "minilm") are flagged. Callers can choose to filter or display
labels; ListModels never refuses to surface a model.
*/
func (c *Client) ListModels(ctx context.Context) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("ollama: new request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama: tags http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var tr tagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("ollama: decode tags: %w", err)
	}
	out := make([]ModelInfo, 0, len(tr.Models))
	for _, m := range tr.Models {
		out = append(out, ModelInfo{
			Name:          m.Name,
			Family:        m.Details.Family,
			ParameterSize: m.Details.ParameterSize,
			Embedding:     isEmbeddingFamily(m.Details.Family, m.Name),
		})
	}
	return out, nil
}

/*
isEmbeddingFamily returns true for known embedding-only model families. The
heuristic is conservative: when in doubt, return false so chat selectors do
not lose models. Callers that need certainty should feature-test instead.
*/
func isEmbeddingFamily(family, name string) bool {
	f := strings.ToLower(family)
	n := strings.ToLower(name)
	for _, marker := range []string{"bert", "minilm", "embed"} {
		if strings.Contains(f, marker) || strings.Contains(n, marker) {
			return true
		}
	}
	return false
}
