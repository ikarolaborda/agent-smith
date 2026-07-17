package server

import (
	"context"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/rag"
)

type stubProvider struct{}

func (stubProvider) Name() string { return "fake" }

func (stubProvider) Chat(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{}, nil
}

func (stubProvider) ChatStream(_ context.Context, _ llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	out := make(chan llm.StreamChunk)
	close(out)
	return out, nil
}

/*
graph_expand is opt-in: the hand-labeled cross-source benchmark
(eval/fixtures/README.md) showed negative recall lift, so the default tool
surface must NOT register it, while Options.GraphExpand must. rag_search stays
registered in both cases so agentic retrieval still works.
*/
func hasTool(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func newAgentServer(graphExpand bool) *Server {
	return &Server{
		cfg:         &config.Config{},
		providers:   map[string]llm.Provider{"fake": stubProvider{}},
		rag:         &rag.Service{},
		graphExpand: graphExpand,
	}
}

func TestGraphExpandOffByDefault(t *testing.T) {
	a, err := newAgentServer(false).newAgent("fake")
	if err != nil {
		t.Fatalf("newAgent: %v", err)
	}
	tools := a.Tools.List()
	if hasTool(tools, "graph_expand") {
		t.Errorf("graph_expand must be absent from the default tool surface, got %v", tools)
	}
	if !hasTool(tools, "rag_search") {
		t.Errorf("rag_search must stay registered when RAG is live, got %v", tools)
	}
}

func TestGraphExpandRegisteredWhenOptedIn(t *testing.T) {
	a, err := newAgentServer(true).newAgent("fake")
	if err != nil {
		t.Fatalf("newAgent: %v", err)
	}
	if tools := a.Tools.List(); !hasTool(tools, "graph_expand") {
		t.Errorf("graph_expand must be registered when Options.GraphExpand is set, got %v", tools)
	}
}
