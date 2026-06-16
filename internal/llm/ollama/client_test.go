package ollama_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/llm/ollama"
)

func TestNew_DefaultsBaseURL(t *testing.T) {
	c, err := ollama.New(ollama.Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Name() != "ollama" {
		t.Fatalf("Name: %q", c.Name())
	}
}

func TestChat_RoundTrip(t *testing.T) {
	var seen struct {
		Model    string                   `json:"model"`
		Stream   bool                     `json:"stream"`
		Messages []map[string]interface{} `json:"messages"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path: %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &seen)

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"message":{"role":"assistant","content":"hello"},
			"done":true,
			"prompt_eval_count":3,
			"eval_count":2
		}`)
	}))
	defer srv.Close()

	c, _ := ollama.New(ollama.Config{BaseURL: srv.URL, Model: "llama3.1"})
	resp, err := c.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if seen.Model != "llama3.1" {
		t.Errorf("model not propagated: %q", seen.Model)
	}
	if seen.Stream {
		t.Errorf("stream should be false for non-stream call")
	}
	if resp.Message.Content != "hello" {
		t.Errorf("content: %q", resp.Message.Content)
	}
	if resp.Usage.TotalTokens != 5 {
		t.Errorf("usage: %+v", resp.Usage)
	}
}

func TestChat_SendsNumCtx(t *testing.T) {
	var seen struct {
		Options *struct {
			NumCtx *int `json:"num_ctx"`
		} `json:"options"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &seen)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"ok"},"done":true}`)
	}))
	defer srv.Close()

	c, _ := ollama.New(ollama.Config{BaseURL: srv.URL, Model: "moe"})
	ctxTokens := 32768
	_, err := c.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		NumCtx:   &ctxTokens,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if seen.Options == nil || seen.Options.NumCtx == nil {
		t.Fatal("num_ctx not sent on the wire")
	}
	if *seen.Options.NumCtx != 32768 {
		t.Fatalf("num_ctx = %d, want 32768", *seen.Options.NumCtx)
	}
}

func TestChatStream_NDJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, _ := w.(http.Flusher)
		lines := []string{
			`{"message":{"role":"assistant","content":"Hel"},"done":false}` + "\n",
			`{"message":{"role":"assistant","content":"lo"},"done":false}` + "\n",
			`{"message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":1,"eval_count":2}` + "\n",
		}
		for _, l := range lines {
			_, _ = io.WriteString(w, l)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer srv.Close()

	c, _ := ollama.New(ollama.Config{BaseURL: srv.URL})
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
		t.Errorf("missing done")
	}
}
