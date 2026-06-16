package agent_test

import (
	"context"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/agent"
	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/tools"
)

/* stubStreamProvider returns a canned set of chunks on every ChatStream call. */
type stubStreamProvider struct {
	chunks []llm.StreamChunk
}

func (s *stubStreamProvider) Name() string { return "stub" }
func (s *stubStreamProvider) Chat(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{}, nil
}
func (s *stubStreamProvider) ChatStream(_ context.Context, _ llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	out := make(chan llm.StreamChunk, len(s.chunks)+1)
	for _, c := range s.chunks {
		out <- c
	}
	close(out)
	return out, nil
}

func TestRunStream_EmitsTextDeltasAndDone(t *testing.T) {
	p := &stubStreamProvider{chunks: []llm.StreamChunk{
		{Delta: "abc"},
		{Delta: "def"},
		{Done: true},
	}}
	a := agent.New(p, tools.NewRegistry(), "", 3, nil)

	var kinds []agent.StreamEventKind
	var content string
	sink := agent.SinkFunc(func(ev agent.StreamEvent) error {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == agent.StreamEventDone {
			content = ev.FinalContent
		}
		return nil
	})

	got, err := a.RunStream(context.Background(), agent.NewSession(), "hi", sink)
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	if got != "abcdef" {
		t.Fatalf("content: %q", got)
	}
	if content != "abcdef" {
		t.Fatalf("done.FinalContent: %q", content)
	}
	if len(kinds) < 3 || kinds[len(kinds)-1] != agent.StreamEventDone {
		t.Fatalf("ordering: %v", kinds)
	}
}

/* blockingProvider produces a channel that only respects ctx for cancellation. */
type blockingProvider struct{}

func (blockingProvider) Name() string { return "block" }
func (blockingProvider) Chat(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{}, nil
}
func (blockingProvider) ChatStream(ctx context.Context, _ llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	out := make(chan llm.StreamChunk)
	go func() {
		defer close(out)
		<-ctx.Done()
	}()
	return out, nil
}

func TestRunStream_ContextCancellationStopsLoop(t *testing.T) {
	a := agent.New(blockingProvider{}, tools.NewRegistry(), "", 3, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := a.RunStream(ctx, agent.NewSession(), "go", agent.SinkFunc(func(agent.StreamEvent) error { return nil }))
	if err == nil {
		t.Fatalf("expected error from cancelled context")
	}
}
