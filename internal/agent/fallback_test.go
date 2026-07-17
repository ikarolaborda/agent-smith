package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/rag"
	"github.com/ikarolaborda/agent-smith/internal/tools"
)

/* scriptedProvider returns a canned assistant message per Chat call. */
type scriptedProvider struct {
	calls     int
	responses []llm.Message
}

func (p *scriptedProvider) Name() string { return "scripted" }

func (p *scriptedProvider) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	i := p.calls
	p.calls++
	if i < len(p.responses) {
		return &llm.ChatResponse{Message: p.responses[i]}, nil
	}
	return &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "grounded-default"}}, nil
}

func (p *scriptedProvider) ChatStream(context.Context, llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	return nil, errors.New("no stream")
}

func emptyRAG(t *testing.T) *rag.Service {
	t.Helper()
	svc, err := rag.NewService(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("rag.NewService: %v", err)
	}
	return svc
}

func newAgentFor(p *scriptedProvider, agentic bool, ragSvc *rag.Service) *Agent {
	a := New(p, tools.NewRegistry(), "", 4, nil)
	a.Agentic = agentic
	a.RAG = ragSvc
	return a
}

/* Agentic answer with no retrieval must trigger exactly one classic grounded pass. */
func TestFallbackFiresWhenNoRetrieval(t *testing.T) {
	p := &scriptedProvider{responses: []llm.Message{{Role: llm.RoleAssistant, Content: "ungrounded"}}}
	a := newAgentFor(p, true, emptyRAG(t))

	out, err := a.Run(context.Background(), NewSession(), "what does the manual say?")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if p.calls != 2 {
		t.Fatalf("expected exactly one fallback pass (2 Chat calls), got %d", p.calls)
	}
	if out != "grounded-default" {
		t.Fatalf("expected the grounded fallback answer, got %q", out)
	}
}

/* When the model actually retrieved, no fallback should fire. */
func TestNoFallbackWhenRetrievalUsed(t *testing.T) {
	p := &scriptedProvider{responses: []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "1", Name: "rag_search"}}},
		{Role: llm.RoleAssistant, Content: "answer-from-evidence"},
	}}
	a := newAgentFor(p, true, emptyRAG(t))

	out, err := a.Run(context.Background(), NewSession(), "q")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "answer-from-evidence" {
		t.Fatalf("retrieval-backed answer must be returned as-is, got %q", out)
	}
	if p.calls != 2 {
		t.Fatalf("expected 2 Chat calls (tool round + answer), got %d — a fallback would make it 3", p.calls)
	}
}

/* Classic mode (Agentic off) must never fall back. */
func TestNoFallbackWhenNotAgentic(t *testing.T) {
	p := &scriptedProvider{responses: []llm.Message{{Role: llm.RoleAssistant, Content: "ungrounded"}}}
	a := newAgentFor(p, false, emptyRAG(t))

	out, _ := a.Run(context.Background(), NewSession(), "q")
	if p.calls != 1 || out != "ungrounded" {
		t.Fatalf("classic mode must not fall back: calls=%d out=%q", p.calls, out)
	}
}

/* Agentic with no RAG service must never fall back (nothing to ground with). */
func TestNoFallbackWhenNoRAG(t *testing.T) {
	p := &scriptedProvider{responses: []llm.Message{{Role: llm.RoleAssistant, Content: "ungrounded"}}}
	a := newAgentFor(p, true, nil)

	out, _ := a.Run(context.Background(), NewSession(), "q")
	if p.calls != 1 || out != "ungrounded" {
		t.Fatalf("no-RAG agentic must not fall back: calls=%d out=%q", p.calls, out)
	}
}

/*
	When the classic fallback pass returns empty, Run must keep the original

answer (ok=false path) rather than emit a blank response.
*/
func TestFallbackEmptyKeepsOriginal(t *testing.T) {
	p := &scriptedProvider{responses: []llm.Message{
		{Role: llm.RoleAssistant, Content: "ungrounded"},
		{Role: llm.RoleAssistant, Content: "   "},
	}}
	a := newAgentFor(p, true, emptyRAG(t))

	out, err := a.Run(context.Background(), NewSession(), "q")
	if err != nil {
		t.Fatal(err)
	}
	if p.calls != 2 {
		t.Fatalf("fallback should be attempted exactly once, calls=%d", p.calls)
	}
	if out != "ungrounded" {
		t.Fatalf("empty fallback must preserve the original answer, got %q", out)
	}
}
