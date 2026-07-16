package llamacpp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/*
fakeRuntime builds a Runtime whose BaseURL points at a test server without
launching a subprocess, so the provider's delegation and lifecycle guards can be
tested in isolation.
*/
func fakeRuntime(base string) *Runtime {
	return &Runtime{baseURL: base + "/v1", apiKey: "test-runtime-api-key-123456789", started: true}
}

func it_delegates_chat_to_the_local_endpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-runtime-api-key-123456789" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]any{"role": "assistant", "content": "hello from llama"},
				"finish_reason": "stop",
			}},
		})
	}))
	defer srv.Close()

	p, err := New(Config{Runtime: fakeRuntime(srv.URL), Model: "local"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.Name() != "llamacpp" {
		t.Fatalf("Name = %q", p.Name())
	}

	resp, err := p.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Message.Content != "hello from llama" {
		t.Fatalf("content = %q", resp.Message.Content)
	}
}

func TestProviderChat(t *testing.T) { it_delegates_chat_to_the_local_endpoint(t) }

func it_rejects_calls_after_close(t *testing.T) {
	p, err := New(Config{Runtime: fakeRuntime("http://127.0.0.1:1"), Model: "local"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = p.Close(context.Background())
	if _, err := p.Chat(context.Background(), llm.ChatRequest{}); err == nil {
		t.Fatal("Chat after Close should fail fast")
	}
	if _, err := p.ChatStream(context.Background(), llm.ChatRequest{}); err == nil {
		t.Fatal("ChatStream after Close should fail fast")
	}
	/* Double close must be safe. */
	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestProviderClosedGuard(t *testing.T) { it_rejects_calls_after_close(t) }

func it_requires_a_started_runtime(t *testing.T) {
	if _, err := New(Config{Runtime: &Runtime{}, Model: "x"}); err == nil {
		t.Fatal("New with unstarted runtime should error")
	}
	if _, err := New(Config{Model: "x"}); err == nil {
		t.Fatal("New with nil runtime should error")
	}
}

func TestProviderRequiresStarted(t *testing.T) { it_requires_a_started_runtime(t) }
