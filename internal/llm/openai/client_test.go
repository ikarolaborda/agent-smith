package openai_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/llm/openai"
)

func TestNew_RequiresAPIKey(t *testing.T) {
	if _, err := openai.New(openai.Config{}); err == nil {
		t.Fatalf("expected error when APIKey is missing")
	}
}

func TestNew_DefaultsBaseURL(t *testing.T) {
	c, err := openai.New(openai.Config{APIKey: "sk-test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Name() != "openai" {
		t.Fatalf("Name: %q", c.Name())
	}
}

func TestChat_RoundTrip(t *testing.T) {
	var seen struct {
		Model    string                   `json:"model"`
		Messages []map[string]interface{} `json:"messages"`
		Tools    []map[string]interface{} `json:"tools"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("auth header: %q", got)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path: %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &seen); err != nil {
			t.Fatalf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"choices":[{"message":{"role":"assistant","content":"hi there","tool_calls":[{"id":"call_1","type":"function","function":{"name":"shell","arguments":"{\"cmd\":\"ls\"}"}}]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
		}`)
	}))
	defer srv.Close()

	c, err := openai.New(openai.Config{APIKey: "sk-test", BaseURL: srv.URL, Model: "gpt-test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := c.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		Tools:    []llm.ToolDefinition{{Name: "shell", Description: "run a cmd", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if seen.Model != "gpt-test" {
		t.Errorf("model not propagated: %q", seen.Model)
	}
	if len(seen.Tools) != 1 {
		t.Errorf("tools not propagated: %v", seen.Tools)
	}
	if resp.Message.Content != "hi there" {
		t.Errorf("content: %q", resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Name != "shell" {
		t.Errorf("tool call lost: %+v", resp.Message.ToolCalls)
	}
	if resp.Usage.TotalTokens != 5 {
		t.Errorf("usage: %+v", resp.Usage)
	}
}

func TestChatStream_ParsesSSEAndDONE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		fragments := []string{
			`data: {"choices":[{"delta":{"content":"Hel"}}]}` + "\n\n",
			`data: {"choices":[{"delta":{"content":"lo"}}]}` + "\n\n",
			`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n",
			"data: [DONE]\n\n",
		}
		for _, f := range fragments {
			_, _ = io.WriteString(w, f)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer srv.Close()

	c, _ := openai.New(openai.Config{APIKey: "sk-test", BaseURL: srv.URL})
	ch, err := c.ChatStream(context.Background(), llm.ChatRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var combined strings.Builder
	doneSeen := false
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("stream err: %v", chunk.Err)
		}
		if chunk.Done {
			doneSeen = true
		}
		combined.WriteString(chunk.Delta)
	}
	if combined.String() != "Hello" {
		t.Errorf("delta accumulation: %q", combined.String())
	}
	if !doneSeen {
		t.Errorf("Done flag not seen")
	}
}

func TestChat_PropagatesErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"nope","type":"invalid_request_error"}}`)
	}))
	defer srv.Close()
	c, _ := openai.New(openai.Config{APIKey: "sk-test", BaseURL: srv.URL})
	_, err := c.Chat(context.Background(), llm.ChatRequest{})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("expected error containing 'nope', got %v", err)
	}
}
