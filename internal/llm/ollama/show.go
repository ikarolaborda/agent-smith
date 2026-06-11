package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type showRequest struct {
	Model string `json:"model"`
}

type showResponse struct {
	Capabilities []string `json:"capabilities"`
}

/*
Capabilities queries POST /api/show for a model and returns its advertised
capability list (e.g. "completion", "tools", "vision", "embedding"). Modern
Ollama reports "vision" for multimodal models; callers use that to decide
whether image input is meaningful. Older Ollama versions that omit the field
return an empty slice rather than an error.
*/
func (c *Client) Capabilities(ctx context.Context, model string) ([]string, error) {
	buf, err := json.Marshal(showRequest{Model: model})
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal show: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/show", bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("ollama: new show request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: show http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama: show http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var sr showResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("ollama: decode show: %w", err)
	}
	return sr.Capabilities, nil
}
