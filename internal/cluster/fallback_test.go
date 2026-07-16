package cluster

import (
	"context"
	"strings"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

func TestProviderFallbackStreaming(t *testing.T) {
	cfg := testConfig()
	cfg.Models[0].MinMemoryGB = 34 // Fits the coordinator's 44 GB safe local budget.
	/* No cluster runtimes installed in CI -> provider must fall back to local. */
	local := &fakeProvider{name: "ollama", tokens: []string{"patch ", "generated"}}

	p, err := New(context.Background(), cfg, local, discardLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close(context.Background())

	if p.Name() != "cluster" {
		t.Fatalf("provider name = %q, want cluster", p.Name())
	}

	t.Run("it_streams_tokens_through_the_local_fallback", func(t *testing.T) {
		ch, err := p.ChatStream(context.Background(), llm.ChatRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "fix this"}}})
		if err != nil {
			t.Fatalf("ChatStream: %v", err)
		}
		var got strings.Builder
		sawDone := false
		for c := range ch {
			if c.Err != nil {
				t.Fatalf("stream error: %v", c.Err)
			}
			got.WriteString(c.Delta)
			if c.Done {
				sawDone = true
			}
		}
		if got.String() != "patch generated" {
			t.Fatalf("streamed %q, want %q", got.String(), "patch generated")
		}
		if !sawDone {
			t.Error("stream never signaled Done")
		}
	})

	t.Run("it_returns_aggregated_content_for_non_streaming_chat", func(t *testing.T) {
		resp, err := p.Chat(context.Background(), llm.ChatRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "fix this"}}})
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if resp.Message.Content != "patch generated" {
			t.Fatalf("content = %q, want %q", resp.Message.Content, "patch generated")
		}
	})

	t.Run("it_records_the_selected_backend_in_metrics", func(t *testing.T) {
		_, _ = p.Chat(context.Background(), llm.ChatRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
		snap := p.Metrics().Snapshot()
		if snap.Backend != BackendLocal {
			t.Fatalf("selected backend metric = %q, want local", snap.Backend)
		}
	})
}

func TestChatMergesStreamedToolCallSnapshots(t *testing.T) {
	cfg := testConfig()
	cfg.Models[0].MinMemoryGB = 34 // This test exercises local stream aggregation.
	/*
		Simulate the transport's cumulative-snapshot semantics: one tool call
		whose arguments arrive across three fragments, each a fuller snapshot of
		the same id. Chat() must return exactly ONE merged tool call, not three.
	*/
	id := "call_1"
	local := &fakeProvider{name: "ollama", chunks: []llm.StreamChunk{
		{ToolCallDelta: &llm.ToolCall{ID: id, Name: "scan", Arguments: []byte(`{"pa`)}},
		{ToolCallDelta: &llm.ToolCall{ID: id, Name: "scan", Arguments: []byte(`{"path":"`)}},
		{ToolCallDelta: &llm.ToolCall{ID: id, Name: "scan", Arguments: []byte(`{"path":"./internal"}`)}},
	}}
	p, err := New(context.Background(), cfg, local, discardLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close(context.Background())

	resp, err := p.Chat(context.Background(), llm.ChatRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "scan"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1 (snapshots must merge by id)", len(resp.Message.ToolCalls))
	}
	tc := resp.Message.ToolCalls[0]
	if tc.Name != "scan" || string(tc.Arguments) != `{"path":"./internal"}` {
		t.Fatalf("merged tool call = %s %s, want scan {\"path\":\"./internal\"}", tc.Name, string(tc.Arguments))
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("finish reason = %q, want tool_calls", resp.FinishReason)
	}
}

func TestStrictClusterHasNoFallback(t *testing.T) {
	cfg := testConfig()
	cfg.Runtime.StrictCluster = true
	local := &fakeProvider{name: "ollama", tokens: []string{"x"}}
	p, err := New(context.Background(), cfg, local, discardLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close(context.Background())

	_, err = p.ChatStream(context.Background(), llm.ChatRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
	if err == nil {
		t.Fatal("strict_cluster with no cluster backend should error, not fall back")
	}
}
