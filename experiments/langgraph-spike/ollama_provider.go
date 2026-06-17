package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

/*
ollamaProvider is a minimal real Provider backed by Ollama's /api/chat. It
stands in for any agent-smith provider so the adapter is driven by an actual
local model rather than a stub. Non-streaming only — that is all the spike's
graph needs.
*/
type ollamaProvider struct {
	baseURL string
	model   string
	http    *http.Client
}

func newOllamaProvider(baseURL, model string) *ollamaProvider {
	if baseURL == "" {
		baseURL = "http://127.0.0.1:11434"
	}
	return &ollamaProvider{baseURL: baseURL, model: model, http: &http.Client{Timeout: 5 * time.Minute}}
}

func (*ollamaProvider) Name() string { return "ollama" }

func (p *ollamaProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	type wireMsg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	body := struct {
		Model    string    `json:"model"`
		Messages []wireMsg `json:"messages"`
		Stream   bool      `json:"stream"`
		Options  any       `json:"options,omitempty"`
	}{Model: p.model, Stream: false}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, wireMsg{Role: string(m.Role), Content: m.Content})
	}
	if req.Temperature != nil {
		body.Options = map[string]any{"temperature": *req.Temperature}
	}

	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/chat", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := p.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama chat: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama chat: status %d", resp.StatusCode)
	}

	var out struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		DoneReason string `json:"done_reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &ChatResponse{
		Message:      Message{Role: Role(out.Message.Role), Content: out.Message.Content},
		FinishReason: out.DoneReason,
	}, nil
}
