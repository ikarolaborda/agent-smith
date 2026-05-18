package agent_test

import (
	"context"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/agent"
	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/tools"
)

/*
fakeProvider is a minimal llm.Provider used to exercise the agent loop. It
returns a queue of pre-recorded responses, one per Chat call.
*/
type fakeProvider struct {
	responses []llm.ChatResponse
	idx       int
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	if f.idx >= len(f.responses) {
		return &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}}, nil
	}
	r := f.responses[f.idx]
	f.idx++
	return &r, nil
}

func (f *fakeProvider) ChatStream(_ context.Context, _ llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk)
	close(ch)
	return ch, nil
}

func TestAgent_RunReturnsAssistantContentWhenNoToolCalls(t *testing.T) {
	p := &fakeProvider{
		responses: []llm.ChatResponse{
			{Message: llm.Message{Role: llm.RoleAssistant, Content: "hello back"}},
		},
	}
	a := agent.New(p, tools.NewRegistry(), "", 3, nil)
	got, err := a.Run(context.Background(), agent.NewSession(), "hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != "hello back" {
		t.Fatalf("got %q want %q", got, "hello back")
	}
}

func TestAgent_RunHitsMaxIterationsOnToolLoop(t *testing.T) {
	/*
		The provider always returns a tool call; the loop should terminate
		with ErrMaxIterations rather than spinning forever.
	*/
	p := &fakeProvider{
		responses: []llm.ChatResponse{
			{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "1", Name: "missing"}}}},
			{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "2", Name: "missing"}}}},
		},
	}
	a := agent.New(p, tools.NewRegistry(), "", 2, nil)
	_, err := a.Run(context.Background(), agent.NewSession(), "loop")
	if err == nil {
		t.Fatalf("expected ErrMaxIterations, got nil")
	}
}
