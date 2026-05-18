package anthropic_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/llm/anthropic"
)

func TestNew_RequiresAPIKey(t *testing.T) {
	if _, err := anthropic.New(anthropic.Config{}); err == nil {
		t.Fatalf("expected error when APIKey is missing")
	}
}

func TestChat_SplitsSystemPromptAndParsesContent(t *testing.T) {
	var seen struct {
		Model    string `json:"model"`
		System   string `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
		MaxTokens int `json:"max_tokens"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "sk-ant-test" {
			t.Errorf("x-api-key: %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got == "" {
			t.Errorf("anthropic-version missing")
		}
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path: %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &seen)

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"content":[
				{"type":"text","text":"hello"},
				{"type":"tool_use","id":"toolu_1","name":"shell","input":{"cmd":"ls"}}
			],
			"stop_reason":"tool_use",
			"usage":{"input_tokens":4,"output_tokens":7}
		}`)
	}))
	defer srv.Close()

	c, _ := anthropic.New(anthropic.Config{APIKey: "sk-ant-test", BaseURL: srv.URL, Model: "claude-test"})
	resp, err := c.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "be helpful"},
			{Role: llm.RoleUser, Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if seen.System != "be helpful" {
		t.Errorf("system not lifted: %q", seen.System)
	}
	if seen.Model != "claude-test" {
		t.Errorf("model not propagated: %q", seen.Model)
	}
	if seen.MaxTokens == 0 {
		t.Errorf("max_tokens not set; Anthropic requires it")
	}
	if len(seen.Messages) != 1 || seen.Messages[0].Role != "user" {
		t.Errorf("messages wrong: %+v", seen.Messages)
	}
	if resp.Message.Content != "hello" {
		t.Errorf("content: %q", resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Name != "shell" {
		t.Errorf("tool calls: %+v", resp.Message.ToolCalls)
	}
	if resp.Usage.TotalTokens != 11 {
		t.Errorf("usage: %+v", resp.Usage)
	}
}

func TestChatStream_AccumulatesTextDelta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`event: message_start` + "\n" + `data: {"type":"message_start"}` + "\n\n",
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}` + "\n\n",
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}` + "\n\n",
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}` + "\n\n",
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}` + "\n\n",
		}
		for _, e := range events {
			_, _ = io.WriteString(w, e)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer srv.Close()

	c, _ := anthropic.New(anthropic.Config{APIKey: "sk-ant-test", BaseURL: srv.URL})
	ch, err := c.ChatStream(context.Background(), llm.ChatRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var combined strings.Builder
	done := false
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("err: %v", chunk.Err)
		}
		combined.WriteString(chunk.Delta)
		if chunk.Done {
			done = true
		}
	}
	if combined.String() != "Hello" {
		t.Errorf("delta: %q", combined.String())
	}
	if !done {
		t.Errorf("missing message_stop")
	}
}
