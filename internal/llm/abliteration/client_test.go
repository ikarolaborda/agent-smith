package abliteration

import (
	"context"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/* captor records the request the wrapper forwarded so the temperature default can be asserted. */
type captor struct{ last llm.ChatRequest }

func (*captor) Name() string { return "captor" }

func (c *captor) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	c.last = req
	return &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"}}, nil
}

func (c *captor) ChatStream(_ context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	c.last = req
	ch := make(chan llm.StreamChunk)
	close(ch)
	return ch, nil
}

func TestDefaultsLowTemperatureWhenUnset(t *testing.T) {
	cap := &captor{}
	c := &Client{inner: cap, temp: DefaultTemperature}

	if _, err := c.Chat(context.Background(), llm.ChatRequest{Model: "m"}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if cap.last.Temperature == nil || *cap.last.Temperature != DefaultTemperature {
		t.Fatalf("expected temperature defaulted to %v, got %v", DefaultTemperature, cap.last.Temperature)
	}
}

func TestPreservesExplicitTemperature(t *testing.T) {
	cap := &captor{}
	c := &Client{inner: cap, temp: DefaultTemperature}

	explicit := 0.9
	if _, err := c.Chat(context.Background(), llm.ChatRequest{Model: "m", Temperature: &explicit}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if cap.last.Temperature == nil || *cap.last.Temperature != explicit {
		t.Fatalf("explicit temperature must be preserved, got %v", cap.last.Temperature)
	}
}
